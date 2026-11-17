// 运维平台底座（Operations Platform）的单二进制入口。
//
// M0 只提供基础设施骨架：配置加载、数据目录自检、SQLite 接入、内嵌 UI、
// 健康检查与版本信息、结构化日志与优雅关闭。
package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"operations-platform/internal/api"
	"operations-platform/internal/config"
	"operations-platform/internal/host"
	"operations-platform/internal/identity"
	"operations-platform/internal/observability"
	"operations-platform/internal/pki"
	"operations-platform/internal/plugin"
	"operations-platform/internal/plugin/authz"
	"operations-platform/internal/plugin/discovery"
	"operations-platform/internal/plugin/instances"
	"operations-platform/internal/plugin/registry"
	"operations-platform/internal/plugin/remote"
	"operations-platform/internal/scheduler"
	"operations-platform/internal/secret"
	"operations-platform/internal/store"
)

// 以下变量由 scripts/build.ps1 / scripts/build.sh 通过 -ldflags -X 注入。
var (
	version   = "dev"
	commit    = "none"
	buildTime = "unknown"
)

// hostLocator 让插件服务按主机标识取地址，同时避免 plugin 包依赖 host 包。
// service 在 hostSvc 装配后回填：探测阶段才需要它检查 unit，启动装配阶段不依赖。
type hostLocator struct {
	store   *host.Store
	service *host.Service
}

func (l hostLocator) HostAddress(ctx context.Context, hostID string) (string, error) {
	h, err := l.store.Get(ctx, hostID)
	if err != nil {
		return "", err
	}
	return h.Address, nil
}

func (l hostLocator) UnitPresent(ctx context.Context, hostID, pluginID string) (bool, error) {
	if l.service == nil {
		return false, errors.New("主机服务尚未装配")
	}
	return l.service.UnitPresent(ctx, hostID, pluginID)
}

func main() {
	if err := run(); err != nil {
		// 启动失败必须给出可读原因并以非零码退出（NFR-RUN-01）。
		fmt.Fprintf(os.Stderr, "启动失败: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load(os.Args[1:], os.Getenv)
	if err != nil {
		return err
	}

	logger, err := observability.NewLogger(cfg.LogLevel, os.Stdout)
	if err != nil {
		return fmt.Errorf("初始化日志失败: %w", err)
	}
	logger.Info("运维平台底座启动中",
		"version", version,
		"commit", commit,
		"build_time", buildTime,
		"config", cfg.Redacted(),
	)

	startupCtx := context.Background()

	// 启动自检顺序固定：目录 → 存储 → HTTP。任一步失败都不带病启动。
	if err := store.InitDataDir(startupCtx, logger, cfg.DataDir, store.NoopFilesystemChecker{}); err != nil {
		return fmt.Errorf("数据目录自检失败: %w", err)
	}

	st, err := store.Open(startupCtx, logger, cfg.DataDir)
	if err != nil {
		return fmt.Errorf("存储自检失败: %w", err)
	}
	defer func() {
		if err := st.Close(); err != nil {
			logger.Error("关闭数据库失败", "error", err)
		}
	}()

	// 批量写队列：插件执行记录与审计事件共用一条队列，按记录类型分发。
	// 队列不依赖业务服务，因此可以先于服务创建，避免构造期循环依赖。
	queue := store.NewBatchQueue(st, logger, cfg.ExecutionQueueBatch, cfg.ExecutionQueueInterval,
		func(ctx context.Context, tx *sql.Tx, recs []store.Record) error {
			for _, rec := range recs {
				switch rec.Kind {
				case store.RecordKindPluginExecution:
					if err := registry.WriteExecution(tx, rec.Payload); err != nil {
						return err
					}
				case store.RecordKindAudit:
					if err := observability.WriteAuditEvent(tx, rec.Payload); err != nil {
						return err
					}
				default:
					return fmt.Errorf("未知的记录类型 %q", rec.Kind)
				}
			}
			return nil
		})
	defer func() {
		// 队列必须先于数据库关闭：Close 会把已入队但未落盘的记录写完。
		closeCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
		defer cancel()
		if err := queue.Close(closeCtx); err != nil {
			logger.Error("关闭批量写队列失败", "error", err)
		}
	}()
	auditRecorder := observability.NewAuditRecorder(queue, logger)

	pluginRegistry := registry.New(st)
	scanner := discovery.New(cfg.PluginDir, pluginRegistry, logger)
	instanceStore := instances.NewStore(st)
	hostStore := host.NewStore(st)

	// M3/M6：凭据与证书主密钥。缺失时不阻断底座启动，但凭据操作、内置 CA 与远端实例显式不可用。
	var box *secret.Box
	if len(cfg.CredentialKey) > 0 {
		box, err = secret.New(cfg.CredentialKey)
		if err != nil {
			return fmt.Errorf("初始化凭据加密失败: %w", err)
		}
	}
	var pkiMgr *pki.Manager
	var remoteClient *remote.Client
	if box != nil {
		pkiMgr, err = pki.New(pki.Options{
			DataDir:      cfg.DataDir,
			Box:          box,
			Store:        instanceStore,
			BaseID:       cfg.PKIBaseID,
			CAValidity:   cfg.PKICAValidity,
			LeafValidity: cfg.PKILeafValidity,
		})
		if err != nil {
			return fmt.Errorf("初始化内置 CA 失败: %w", err)
		}
		remoteClient = remote.New(remote.Options{
			TLS:          remote.NewTLSProvider(pkiMgr),
			DialTimeout:  cfg.RemoteDialTimeout,
			ReadyTimeout: cfg.RemoteReadyTimeout,
		})
	} else {
		logger.Warn("credential_key 未配置：内置 CA、证书与远端实例能力不可用")
	}

	locator := hostLocator{store: hostStore}
	pluginSvc, err := plugin.NewService(plugin.Options{
		Registry:       pluginRegistry,
		Scanner:        scanner,
		Authorizer:     identity.RoleAuthorizer{},
		Queue:          queue,
		Logger:         logger,
		InvokeTimeout:  cfg.PluginInvokeTimeout,
		MaxResultBytes: cfg.PluginMaxResultSize,
		MaxConcurrency: cfg.PluginMaxConcurrency,
		Instances:      instanceStore,
		PKI:            pkiMgr,
		Remote:         remoteClient,
		Hosts:          &locator,
		Box:            box,
		DataDir:        cfg.DataDir,
		ProbeTTL:       cfg.PluginStateTTL,
		DialTimeout:    cfg.RemoteDialTimeout,
		ReadyTimeout:   cfg.RemoteReadyTimeout,
		Audit: func(ctx context.Context, subj authz.Subject, action authz.Action, target authz.Target, result, reason string) {
			auditRecorder.Record(ctx, observability.AuditEvent{
				Subject:    subj.ID,
				Action:     string(action),
				TargetType: target.Type,
				TargetID:   target.ID,
				Result:     result,
				Reason:     reason,
			})
		},
	})
	if err != nil {
		return fmt.Errorf("装配插件服务失败: %w", err)
	}

	identitySvc := identity.NewService(identity.Options{
		Repository:   identity.NewRepository(st),
		Audit:        auditRecorder,
		Logger:       logger,
		SessionTTL:   cfg.SessionTTL,
		MaxAttempts:  cfg.LoginMaxAttempts,
		Lockout:      cfg.LoginLockout,
		BootstrapPwd: cfg.AdminPassword,
	})
	if err := identitySvc.Bootstrap(startupCtx); err != nil {
		return fmt.Errorf("初始化内置管理员失败: %w", err)
	}

	dialer := &host.SSHDialer{ConnectTimeout: cfg.SSHConnectTimeout}
	distributor := host.NewDistributor(host.DistributorOptions{
		Store:          hostStore,
		Dialer:         dialer,
		Logger:         logger,
		RemoteRoot:     cfg.DistributionRemoteRoot,
		UploadTimeout:  cfg.SSHUploadTimeout,
		StartTimeout:   cfg.DistributionStartTimeout,
		ProbeTimeout:   30 * time.Second,
		MaxConcurrency: cfg.DistributionMaxConcurrency,
		Instances:      instanceStore,
		PKI:            pkiMgr,
		Probe:          pluginSvc.ProbeSystem,
		OnState: func(d host.Distribution, actor string) {
			if actor == "" {
				actor = "system"
			}
			auditRecorder.Record(context.Background(), observability.AuditEvent{
				Subject:    actor,
				Action:     string(authz.ActionHostDistribute),
				TargetType: "distribution",
				TargetID:   d.ID,
				Result:     d.State,
				Reason:     d.ErrorCategory,
				Details: map[string]any{
					"host_id":        d.HostID,
					"plugin_id":      d.PluginID,
					"plugin_version": d.PluginVersion,
					"stage":          d.Stage,
				},
			})
		},
	})
	hostSvc, err := host.NewService(host.Options{
		Store:       hostStore,
		Box:         box,
		Dialer:      dialer,
		Distributor: distributor,
		Plugins:     pluginRegistry,
		Authorizer:  identity.RoleAuthorizer{},
		Logger:      logger,
		Instances:   instanceStore,
		PKI:         pkiMgr,
		ConfigRoot:  cfg.DistributionConfigRoot,
		DataRoot:    cfg.DistributionDataRoot,
		UnitDir:     cfg.DistributionUnitDir,
		ServiceUser: cfg.DistributionServiceUser,
		Audit: func(ctx context.Context, subj authz.Subject, action authz.Action, target authz.Target, result, reason string) {
			auditRecorder.Record(ctx, observability.AuditEvent{
				Subject:    subj.ID,
				Action:     string(action),
				TargetType: target.Type,
				TargetID:   target.ID,
				Result:     result,
				Reason:     reason,
			})
		},
	})
	if err != nil {
		return fmt.Errorf("装配主机服务失败: %w", err)
	}
	locator.service = hostSvc
	if n, err := hostSvc.RecoverInterrupted(startupCtx); err != nil {
		logger.Warn("恢复中断的下发任务失败", "error", err)
	} else if n > 0 {
		logger.Info("已将未完成的下发标记为中断", "count", n)
	}

	// 启动时扫描一次；插件目录不可读只告警，不阻断底座启动。
	if rep, err := pluginSvc.Scan(startupCtx); err != nil {
		logger.Warn("启动扫描插件目录失败", "error", err)
	} else {
		logger.Info("插件扫描完成", "inserted", rep.Inserted, "updated", rep.Updated,
			"unchanged", rep.Unchanged, "rejected", rep.Rejected)
	}

	taskSvc, err := scheduler.NewService(scheduler.Options{
		Store:          scheduler.NewStore(st),
		Plugins:        pluginSvc,
		Registry:       pluginRegistry,
		Instances:      instanceStore,
		Authorizer:     identity.RoleAuthorizer{},
		Logger:         logger,
		ScanInterval:   cfg.TaskScanInterval,
		MinInterval:    cfg.TaskMinInterval,
		DefaultTimeout: cfg.TaskDefaultTimeout,
		DefaultRetries: cfg.TaskDefaultRetries,
		Audit: func(ctx context.Context, subj authz.Subject, action authz.Action, target authz.Target, result, reason string) {
			auditRecorder.Record(ctx, observability.AuditEvent{
				Subject:    subj.ID,
				Action:     string(action),
				TargetType: target.Type,
				TargetID:   target.ID,
				Result:     result,
				Reason:     reason,
			})
		},
	})
	if err != nil {
		return fmt.Errorf("装配定时任务服务失败: %w", err)
	}
	if err := taskSvc.Start(startupCtx); err != nil {
		return fmt.Errorf("启动定时任务调度器失败: %w", err)
	}

	srv := api.NewServer(*cfg, logger, st, pluginSvc, hostSvc, taskSvc, identitySvc, api.BuildInfo{
		Version:   version,
		Commit:    commit,
		BuildTime: buildTime,
	})

	// 收到 SIGINT/SIGTERM 后停止接收新请求，并在超时内等待在途请求完成（NFR-RUN-02）。
	signalCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// 周期探测与状态过期（OQ-049 基线）：只探测启用的远端实例，本地实例按需校验漂移。
	go func() {
		ticker := time.NewTicker(cfg.PluginProbeInterval)
		defer ticker.Stop()
		for {
			select {
			case <-signalCtx.Done():
				return
			case <-ticker.C:
				pluginSvc.ProbeEnabled(context.Background())
				pluginSvc.Sweep(context.Background())
			}
		}
	}()

	// 热加载默认关闭（ADR-0003）；开启后按 plugin_scan_interval 周期扫描。
	if cfg.PluginHotReload {
		go func() {
			ticker := time.NewTicker(cfg.PluginScanInterval)
			defer ticker.Stop()
			for {
				select {
				case <-signalCtx.Done():
					return
				case <-ticker.C:
					if _, err := pluginSvc.Scan(context.Background()); err != nil {
						logger.Warn("热加载扫描失败", "error", err)
					}
				}
			}
		}()
	}

	serveErr := make(chan error, 1)
	go func() {
		logger.Info("HTTP 服务监听中", "listen_addr", cfg.ListenAddr)
		serveErr <- srv.ListenAndServe()
	}()

	select {
	case err := <-serveErr:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("HTTP 服务异常退出: %w", err)
		}
		return nil
	case <-signalCtx.Done():
		logger.Info("收到终止信号，开始优雅关闭", "timeout", cfg.ShutdownTimeout.String())
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		logger.Error("优雅关闭超时，强制关闭", "error", err)
		if closeErr := srv.Close(); closeErr != nil {
			logger.Error("强制关闭 HTTP 服务失败", "error", closeErr)
		}
		return fmt.Errorf("优雅关闭失败: %w", err)
	}

	logger.Info("HTTP 服务已关闭", "version", version)

	// 先停调度器，再等待在途下发与执行记录落盘。
	if err := taskSvc.Stop(shutdownCtx); err != nil {
		logger.Error("等待定时任务结束超时", "error", err)
	}
	if err := hostSvc.WaitDistributions(shutdownCtx); err != nil {
		logger.Error("等待下发任务结束超时", "error", err)
	}
	return nil
}
