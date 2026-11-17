package host

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"

	"operations-platform/internal/pki"
	"operations-platform/internal/plugin/instances"
)

// Package 是待下发的插件包（内存中的 tar.gz）。
type Package struct {
	Archive     []byte
	ArchiveSHA  string
	ManifestSHA string
	PluginID    string
	Version     string
}

// ArchiveDir 把插件目录打包为内存 tar.gz，并计算归档与 plugin.yaml 的 SHA-256。
func ArchiveDir(root string) (Package, error) {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)

	var manifestSHA string
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if path == root {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if info.IsDir() {
			return tw.WriteHeader(&tar.Header{
				Name:     rel + "/",
				Typeflag: tar.TypeDir,
				Mode:     0o755,
				ModTime:  info.ModTime(),
			})
		}
		if !info.Mode().IsRegular() {
			// 符号链接、设备文件等不进入插件包，避免跨边界写入。
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("读取 %s 失败: %w", rel, err)
		}
		if rel == "plugin.yaml" {
			sum := sha256.Sum256(data)
			manifestSHA = hex.EncodeToString(sum[:])
		}
		if err := tw.WriteHeader(&tar.Header{
			Name:     rel,
			Typeflag: tar.TypeReg,
			Mode:     int64(info.Mode().Perm()),
			Size:     int64(len(data)),
			ModTime:  info.ModTime(),
		}); err != nil {
			return err
		}
		_, err = tw.Write(data)
		return err
	})
	if err != nil {
		return Package{}, fmt.Errorf("打包插件目录失败: %w", err)
	}
	if manifestSHA == "" {
		return Package{}, errors.New("插件目录缺少 plugin.yaml，无法下发")
	}
	if err := tw.Close(); err != nil {
		return Package{}, err
	}
	if err := gz.Close(); err != nil {
		return Package{}, err
	}
	sum := sha256.Sum256(buf.Bytes())
	return Package{Archive: buf.Bytes(), ArchiveSHA: hex.EncodeToString(sum[:]), ManifestSHA: manifestSHA}, nil
}

// Distributor 执行下发状态机；同一主机串行、跨主机受全局并发限制（DIST-C-01/02）。
type Distributor struct {
	store  *Store
	dialer Dialer
	logger *slog.Logger

	remoteRoot    string
	uploadTimeout time.Duration
	startTimeout  time.Duration
	probeTimeout  time.Duration

	instances *instances.Store
	pki       *pki.Manager
	// probe 由插件服务注入：对实例做 TCP + mTLS + Ready + GetVersion 探测。
	probe func(ctx context.Context, instanceID string) (bool, string)

	sem       chan struct{}
	mu        sync.Mutex
	hostLocks map[string]*sync.Mutex
	wg        sync.WaitGroup

	// onState 在每个阶段变更后回调，用于审计与日志。
	onState func(d Distribution, actor string)
}

// DistributorOptions 是 Distributor 的显式依赖。
type DistributorOptions struct {
	Store          *Store
	Dialer         Dialer
	Logger         *slog.Logger
	RemoteRoot     string
	UploadTimeout  time.Duration
	StartTimeout   time.Duration
	ProbeTimeout   time.Duration
	MaxConcurrency int
	OnState        func(d Distribution, actor string)
	Instances      *instances.Store
	PKI            *pki.Manager
	Probe          func(ctx context.Context, instanceID string) (bool, string)
}

func NewDistributor(o DistributorOptions) *Distributor {
	if o.MaxConcurrency <= 0 {
		o.MaxConcurrency = 1
	}
	if o.UploadTimeout <= 0 {
		o.UploadTimeout = 5 * time.Minute
	}
	if o.StartTimeout <= 0 {
		o.StartTimeout = 60 * time.Second
	}
	if o.ProbeTimeout <= 0 {
		o.ProbeTimeout = 30 * time.Second
	}
	return &Distributor{
		store:         o.Store,
		dialer:        o.Dialer,
		logger:        o.Logger,
		remoteRoot:    strings.TrimRight(o.RemoteRoot, "/"),
		uploadTimeout: o.UploadTimeout,
		startTimeout:  o.StartTimeout,
		probeTimeout:  o.ProbeTimeout,
		instances:     o.Instances,
		pki:           o.PKI,
		probe:         o.Probe,
		sem:           make(chan struct{}, o.MaxConcurrency),
		hostLocks:     make(map[string]*sync.Mutex),
		onState:       o.OnState,
	}
}

func (d *Distributor) hostLock(hostID string) *sync.Mutex {
	d.mu.Lock()
	defer d.mu.Unlock()
	lock, ok := d.hostLocks[hostID]
	if !ok {
		lock = &sync.Mutex{}
		d.hostLocks[hostID] = lock
	}
	return lock
}

// Start 异步执行一条已持久化的下发任务（仅安装，不启动；命令式插件使用）。
func (d *Distributor) Start(dist Distribution, h Host, cred Credential, pkg Package, actor string) {
	d.StartWithDeployment(dist, h, cred, pkg, actor, nil)
}

// StartWithDeployment 在安装后继续写入证书/配置/unit 并启动远端 daemon（DOC-09 §6）。
func (d *Distributor) StartWithDeployment(dist Distribution, h Host, cred Credential, pkg Package, actor string, dep *Deployment) {
	d.wg.Add(1)
	go func() {
		defer d.wg.Done()
		lock := d.hostLock(dist.HostID)
		lock.Lock()
		defer lock.Unlock()

		d.sem <- struct{}{}
		defer func() { <-d.sem }()

		d.process(dist, h, cred, pkg, actor, dep)
	}()
}

// Wait 等待在途下发结束，供优雅关闭使用。
func (d *Distributor) Wait(ctx context.Context) error {
	done := make(chan struct{})
	go func() {
		d.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (d *Distributor) process(dist Distribution, h Host, cred Credential, pkg Package, actor string, dep *Deployment) {
	deadline := d.uploadTimeout
	ctx, cancel := context.WithTimeout(context.Background(), deadline)
	defer cancel()

	now := time.Now().UTC()
	dist.StartedAt = &now
	d.setState(dist, DistConnecting, "connecting", "", "")

	conn, err := d.dialer.Dial(ctx, h, cred)
	if err != nil {
		d.fail(dist, err)
		return
	}
	defer conn.Close()

	// 首次连接即锁存指纹（TOFU）；已信任主机的指纹已由 Dial 校验。
	if !h.HostKeyTrusted && conn.Fingerprint() != "" {
		if err := d.store.TrustFingerprint(ctx, h.ID, conn.Fingerprint()); err != nil {
			d.fail(dist, err)
			return
		}
	}

	dir := RemoteDir(d.remoteRoot, pkg.PluginID, pkg.Version)
	dist.RemotePath = dir
	tmp := fmt.Sprintf("%s/%s-%s.tar.gz.tmp", strings.TrimRight(d.remoteRoot, "/"),
		sanitizeSegment(pkg.PluginID), sanitizeSegment(pkg.Version))

	d.setState(dist, DistUploading, "uploading", "", "")
	uploadCmd := fmt.Sprintf("mkdir -p %s && cat > %s", shellQuote(filepath.Dir(tmp)), shellQuote(tmp))
	if _, stderr, err := conn.Run(ctx, uploadCmd, bytes.NewReader(pkg.Archive)); err != nil {
		d.failWith(dist, CatUploadFailed, "上传插件包失败: "+sanitizeRemoteError(err, stderr))
		return
	}

	d.setState(dist, DistVerifying, "verifying", "", "")
	out, stderr, err := conn.Run(ctx, "sha256sum "+shellQuote(tmp), nil)
	if err != nil {
		d.failWith(dist, CatVerifyFailed, "目标机摘要校验命令失败: "+sanitizeRemoteError(err, stderr))
		return
	}
	if got := firstField(string(out)); got != pkg.ArchiveSHA {
		d.failWith(dist, CatVerifyFailed, fmt.Sprintf("目标机摘要不一致：本地 %s，远端 %s", pkg.ArchiveSHA, got))
		return
	}

	d.setState(dist, DistInstalling, "installing", "", "")
	staging := dir + ".tmp-" + sanitizeSegment(dist.ID)
	installCmd := fmt.Sprintf(
		"set -e; rm -rf %[1]s; mkdir -p %[1]s; tar -xzf %[2]s -C %[1]s; "+
			"digest=$(sha256sum %[1]s/plugin.yaml | cut -d' ' -f1); "+
			"if [ \"$digest\" != %[3]s ]; then rm -rf %[1]s; exit 20; fi; "+
			"rm -rf %[4]s; mv %[1]s %[4]s; rm -f %[2]s",
		shellQuote(staging), shellQuote(tmp), shellQuote(pkg.ManifestSHA), shellQuote(dir))
	if _, stderr, err := conn.Run(ctx, installCmd, nil); err != nil {
		// 尽力清理半装目录（DIST-F-02）。
		cleanup := fmt.Sprintf("rm -rf %s %s", shellQuote(staging), shellQuote(tmp))
		_, _, _ = conn.Run(context.Background(), cleanup, nil)
		d.failWith(dist, CatInstallFailed, "目标机安装失败: "+sanitizeRemoteError(err, stderr))
		return
	}

	dist.Installed = true
	if dep == nil {
		// 命令式插件没有远端 daemon：安装即终点，已安装 ≠ 可调用（DIST-05）。
		finish := time.Now().UTC()
		dist.State = DistInstalled
		dist.Stage = "installed"
		dist.ErrorCategory = ""
		dist.ErrorMessage = ""
		dist.FinishedAt = &finish
		d.persist(dist)
		return
	}
	d.setState(dist, DistInstalled, "installed", "", "")
	d.deployRemote(ctx, conn, dist, dep)
}

// deployRemote 写入证书、按主机配置与 unit，daemon-reload 后 enable --now 并探测。
func (d *Distributor) deployRemote(ctx context.Context, conn Conn, dist Distribution, dep *Deployment) {
	cfg, err := renderRemoteConfig(dep)
	if err != nil {
		d.failDeployment(ctx, conn, dist, dep, "config", err)
		return
	}
	files := []struct {
		path string
		mode string
		data []byte
	}{
		{dep.ConfigPath(), "0600", cfg},
		{dep.TLSDir() + "/server.crt", "0644", dep.ServerCertPEM},
		{dep.TLSDir() + "/server.key", "0600", dep.ServerKeyPEM},
		{dep.TLSDir() + "/ca.crt", "0644", dep.CACertPEM},
		{dep.UnitPath(), "0644", unitFile(dep)},
	}
	for _, f := range files {
		if err := d.writeRemoteFile(ctx, conn, f.path, f.mode, f.data); err != nil {
			d.failDeployment(ctx, conn, dist, dep, "artifacts", err)
			return
		}
	}
	if _, stderr, err := conn.Run(ctx, fmt.Sprintf("mkdir -p %s && chmod 0700 %s",
		shellQuote(dep.DataDir()), shellQuote(dep.DataDir())), nil); err != nil {
		d.failDeployment(ctx, conn, dist, dep, "artifacts", errors.New("创建数据目录失败: "+sanitizeRemoteError(err, stderr)))
		return
	}

	d.setState(dist, DistStarting, "starting", "", "")
	startCtx, cancel := context.WithTimeout(context.Background(), d.startTimeout)
	defer cancel()
	cmd := fmt.Sprintf("systemctl daemon-reload && systemctl enable --now %s && systemctl is-active %s",
		shellQuote(dep.UnitName()), shellQuote(dep.UnitName()))
	out, stderr, err := conn.Run(startCtx, cmd, nil)
	if err != nil || strings.TrimSpace(string(out)) != "active" {
		detail := "systemd 启动失败"
		if err != nil {
			detail = sanitizeRemoteError(err, stderr)
		}
		d.failDeployment(ctx, conn, dist, dep, "starting", errors.New(detail))
		return
	}
	dist.StartedOK = true
	dist.ListenPort = dep.Port
	d.setState(dist, DistStarted, "started", "", "")
	if d.instances != nil {
		_ = d.instances.UpdateStatus(ctx, dep.InstanceID, instances.StateStarted, "systemd unit active",
			instances.DriftNone, "", true, true, false)
	}

	// 三态第三层：探测通过才可调用；失败保留「已安装/已启动」，只标记 start_failed。
	ok, detail := d.probeWithRetry(dep.InstanceID)
	if ok {
		finish := time.Now().UTC()
		dist.State = DistCallable
		dist.Stage = "callable"
		dist.Callable = true
		dist.ErrorCategory = ""
		dist.ErrorMessage = ""
		dist.FinishedAt = &finish
		d.persist(dist)
		return
	}
	finish := time.Now().UTC()
	dist.State = DistStartFailed
	dist.Stage = "probing_failed"
	dist.Callable = false
	dist.ErrorCategory = CatInstallFailed
	dist.ErrorMessage = "已启动但探测未通过: " + detail
	dist.FinishedAt = &finish
	d.persist(dist)
	if d.instances != nil {
		_ = d.instances.UpdateStatus(ctx, dep.InstanceID, instances.StateStartFailed,
			"已启动但探测未通过: "+truncateText(detail, 256), instances.DriftNone, truncateText(detail, 512), true, true, false)
	}
}

// probeWithRetry 在 probeTimeout 内重试 Ready；systemd active 之后插件可能仍在初始化。
func (d *Distributor) probeWithRetry(instanceID string) (bool, string) {
	if d.probe == nil {
		return false, "探测组件未装配"
	}
	deadline := time.Now().Add(d.probeTimeout)
	detail := ""
	for {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		ok, msg := d.probe(ctx, instanceID)
		cancel()
		detail = msg
		if ok {
			return true, msg
		}
		if time.Now().After(deadline) {
			return false, detail
		}
		time.Sleep(2 * time.Second)
	}
}

func (d *Distributor) writeRemoteFile(ctx context.Context, conn Conn, path, mode string, data []byte) error {
	cmd := fmt.Sprintf("mkdir -p %s && printf '%%s' %s | base64 -d > %s && chmod %s %s",
		shellQuote(filepath.Dir(path)), shellQuote(base64.StdEncoding.EncodeToString(data)),
		shellQuote(path), mode, shellQuote(path))
	if _, stderr, err := conn.Run(ctx, cmd, nil); err != nil {
		return fmt.Errorf("写入 %s 失败: %s", path, sanitizeRemoteError(err, stderr))
	}
	return nil
}

// failDeployment 做尽力回滚：停止并移除 unit、删除证书与配置，吊销证书，实例标记 start_failed。
func (d *Distributor) failDeployment(ctx context.Context, conn Conn, dist Distribution, dep *Deployment, stage string, cause error) {
	rollback := fmt.Sprintf("systemctl disable --now %s >/dev/null 2>&1 || true; rm -f %s; systemctl daemon-reload >/dev/null 2>&1 || true; rm -rf %s",
		shellQuote(dep.UnitName()), shellQuote(dep.UnitPath()), shellQuote(dep.ConfigDir()))
	rollbackCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	if _, stderr, err := conn.Run(rollbackCtx, rollback, nil); err != nil {
		d.logger.Warn("下发失败回滚命令未完全成功", "instance_id", dep.InstanceID, "error", err, "stderr", stderr)
	}
	cancel()
	if d.pki != nil && dist.CertSerial != "" {
		if err := d.pki.Revoke(ctx, dist.CertSerial, "distribution_failed"); err != nil {
			d.logger.Warn("吊销下发失败实例证书失败", "serial", dist.CertSerial, "error", err)
		}
	}
	finish := time.Now().UTC()
	dist.Installed = false
	dist.StartedOK = false
	dist.Callable = false
	dist.State = DistFailed
	dist.Stage = stage
	dist.ErrorCategory = CatInstallFailed
	dist.ErrorMessage = cause.Error()
	dist.FinishedAt = &finish
	d.persist(dist)
	if d.instances != nil {
		_ = d.instances.UpdateStatus(ctx, dep.InstanceID, instances.StateStartFailed,
			"下发失败已回滚: "+truncateText(cause.Error(), 256), instances.DriftNone, truncateText(cause.Error(), 512), false, false, false)
	}
}

func truncateText(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "…"
}

// renderRemoteConfig 生成 plugin-spec/03 §3.2 的按主机配置。
func renderRemoteConfig(dep *Deployment) ([]byte, error) {
	body := map[string]any{
		"deployment":  "remote",
		"plugin_id":   dep.PluginID,
		"instance_id": dep.InstanceID,
		"listen": map[string]any{
			// 监听地址是基线（OQ-048）；端口为生效值。
			"address": "0.0.0.0",
			"port":    dep.Port,
		},
		"tls": map[string]any{
			"cert_file":       dep.TLSDir() + "/server.crt",
			"key_file":        dep.TLSDir() + "/server.key",
			"ca_file":         dep.TLSDir() + "/ca.crt",
			"expected_client": "base:" + dep.BaseID,
		},
		"data_dir": dep.DataDir(),
	}
	if len(dep.ConfigValues) > 0 {
		body["config"] = dep.ConfigValues
	}
	return yaml.Marshal(body)
}

// unitFile 生成 systemd unit；ExecStart 只含非敏感参数（ADR-0018 §2.2）。
func unitFile(dep *Deployment) []byte {
	serviceUser := dep.ServiceUser
	if serviceUser == "" {
		serviceUser = "root"
	}
	exec := fmt.Sprintf("%s --ops-plugin-id %s --ops-instance-id %s --ops-config %s --ops-data-dir %s --ops-contract-version %s",
		dep.Executable, shellQuote(dep.PluginID), shellQuote(dep.InstanceID),
		shellQuote(dep.ConfigPath()), shellQuote(dep.DataDir()), shellQuote(dep.ContractVersion))
	unit := fmt.Sprintf(`[Unit]
Description=Ops Platform plugin %s on %s
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=%s
ExecStart=%s
Restart=on-failure
RestartSec=5s
StartLimitBurst=5
StartLimitIntervalSec=300
TimeoutStopSec=30
NoNewPrivileges=true
PrivateTmp=true

[Install]
WantedBy=multi-user.target
`, dep.PluginID, dep.HostID, serviceUser, exec)
	return []byte(unit)
}

func (d *Distributor) setState(dist Distribution, state, stage, category, message string) {
	dist.State = state
	dist.Stage = stage
	dist.ErrorCategory = category
	dist.ErrorMessage = message
	d.persist(dist)
}

func (d *Distributor) fail(dist Distribution, err error) {
	var ce *ConnectError
	if errors.As(err, &ce) {
		d.failWith(dist, ce.Category, err.Error())
		return
	}
	d.failWith(dist, CatUnreachable, err.Error())
}

func (d *Distributor) failWith(dist Distribution, category, message string) {
	finish := time.Now().UTC()
	dist.State = DistFailed
	dist.ErrorCategory = category
	dist.ErrorMessage = message
	dist.FinishedAt = &finish
	d.persist(dist)
}

func (d *Distributor) persist(dist Distribution) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := d.store.UpdateDistribution(ctx, dist); err != nil {
		d.logger.Error("更新下发任务状态失败", "distribution_id", dist.ID, "state", dist.State, "error", err)
	}
	if d.onState != nil {
		d.onState(dist, "")
	}
}

func firstField(s string) string {
	fields := strings.Fields(s)
	if len(fields) == 0 {
		return ""
	}
	return fields[0]
}

// sanitizeRemoteError 只保留命令失败的可读摘要，避免把整个 stderr（可能含路径细节）无界写入库。
func sanitizeRemoteError(err error, stderr []byte) string {
	msg := strings.TrimSpace(string(stderr))
	if len(msg) > 512 {
		msg = msg[:512] + "…"
	}
	if msg == "" {
		return err.Error()
	}
	return err.Error() + "；远端输出: " + msg
}

// sanitizeSegment 把已校验的标识再收敛为安全字符集，防止拼进 shell 命令时出现意外。
func sanitizeSegment(s string) string {
	var b strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' || r == '.' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// shellQuote 用单引号包裹，供 sh 使用。
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
