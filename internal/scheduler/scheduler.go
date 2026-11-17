package scheduler

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"operations-platform/internal/plugin"
	"operations-platform/internal/plugin/authz"
	"operations-platform/internal/plugin/instances"
	"operations-platform/internal/plugin/registry"
)

// Invoker 是调度器对插件调用能力的唯一依赖；由 plugin.Service 实现。
type Invoker interface {
	InvokeWithTask(ctx context.Context, subj authz.Subject, pluginID, capabilityID string,
		input json.RawMessage, source, taskID string) (plugin.InvokeResult, error)
	// InvokeInstanceWithTask 是 M6 的实例目标调用；定时任务以此为准。
	InvokeInstanceWithTask(ctx context.Context, subj authz.Subject, instanceID, capabilityID string,
		input json.RawMessage, source, taskID string) (plugin.InvokeResult, error)
	RecordExecution(e registry.Execution)
}

// Options 是调度服务的显式依赖。
type Options struct {
	Store          *Store
	Plugins        Invoker
	Registry       *registry.Registry
	Instances      *instances.Store
	Authorizer     authz.Authorizer
	Logger         *slog.Logger
	ScanInterval   time.Duration
	MinInterval    time.Duration
	DefaultTimeout time.Duration
	DefaultRetries int
	Now            func() time.Time
	Audit          func(ctx context.Context, subj authz.Subject, action authz.Action, target authz.Target, result, reason string)
}

// Service 负责任务 CRUD 与进程内调度。
type Service struct {
	store          *Store
	plugins        Invoker
	registry       *registry.Registry
	instances      *instances.Store
	authorizer     authz.Authorizer
	logger         *slog.Logger
	scanInterval   time.Duration
	minInterval    time.Duration
	defaultTimeout time.Duration
	defaultRetries int
	now            func() time.Time
	audit          func(ctx context.Context, subj authz.Subject, action authz.Action, target authz.Target, result, reason string)

	mu      sync.Mutex
	running map[string]bool
	wg      sync.WaitGroup
}

// systemSubject 是定时触发的执行主体；任务本身已由具备 task.write 的人创建。
var systemSubject = authz.Subject{ID: "system:scheduler", Roles: []string{"admin"}}

// NewService 构造调度服务；参数缺省时使用配置注入值，不在包内硬编码。
func NewService(o Options) (*Service, error) {
	if o.Store == nil || o.Plugins == nil || o.Logger == nil {
		return nil, errors.New("scheduler: Store/Plugins/Logger 不能为空")
	}
	if o.ScanInterval <= 0 {
		o.ScanInterval = 30 * time.Second
	}
	if o.MinInterval <= 0 {
		o.MinInterval = time.Minute
	}
	if o.DefaultTimeout <= 0 {
		o.DefaultTimeout = 5 * time.Minute
	}
	if o.DefaultRetries < 0 {
		o.DefaultRetries = 0
	}
	if o.Now == nil {
		o.Now = func() time.Time { return time.Now().UTC() }
	}
	if o.Authorizer == nil {
		o.Authorizer = denyAll{}
	}
	return &Service{
		store:          o.Store,
		plugins:        o.Plugins,
		registry:       o.Registry,
		instances:      o.Instances,
		authorizer:     o.Authorizer,
		logger:         o.Logger,
		scanInterval:   o.ScanInterval,
		minInterval:    o.MinInterval,
		defaultTimeout: o.DefaultTimeout,
		defaultRetries: o.DefaultRetries,
		now:            o.Now,
		audit:          o.Audit,
		running:        make(map[string]bool),
	}, nil
}

type denyAll struct{}

func (denyAll) Authorize(context.Context, authz.Subject, authz.Action, authz.Target) authz.Decision {
	return authz.Decision{Allowed: false, Reason: "no matching rule"}
}

func (s *Service) authorize(ctx context.Context, subj authz.Subject, action authz.Action, target authz.Target, message string) error {
	d := s.authorizer.Authorize(ctx, subj, action, target)
	if d.Allowed {
		return nil
	}
	s.recordAudit(ctx, subj, action, target, "denied", d.Reason)
	return &Error{Code: CodePermissionDenied, Message: message, Details: map[string]any{"reason": d.Reason}}
}

func (s *Service) recordAudit(ctx context.Context, subj authz.Subject, action authz.Action, target authz.Target, result, reason string) {
	if s.audit != nil {
		s.audit(ctx, subj, action, target, result, reason)
	}
}

// Start 恢复崩溃遗留认领、记录错过的执行，然后进入扫描循环。
func (s *Service) Start(ctx context.Context) error {
	if err := s.recoverClaims(ctx); err != nil {
		return err
	}
	if err := s.markMissed(ctx); err != nil {
		s.logger.Warn("记录错过执行失败", "error", err)
	}
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		ticker := time.NewTicker(s.scanInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case now := <-ticker.C:
				s.scan(ctx, now.UTC())
			}
		}
	}()
	s.logger.Info("定时任务调度器已启动", "scan_interval", s.scanInterval.String(), "min_interval", s.minInterval.String())
	return nil
}

// Stop 等待在执行的任务结束；超时返回错误，由调用方决定是否记录 interrupted。
func (s *Service) Stop(ctx context.Context) error {
	done := make(chan struct{})
	go func() {
		s.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// recoverClaims 处理进程崩溃留下的认领：写 interrupted 记录、清空认领并重算下次触发。
func (s *Service) recoverClaims(ctx context.Context) error {
	claimed, err := s.store.ListClaimed(ctx)
	if err != nil {
		return fmt.Errorf("读取遗留认领失败: %w", err)
	}
	now := s.now()
	for _, t := range claimed {
		s.recordSynthetic(t, registry.StatusInterrupted, "interrupted", now)
		next, nerr := NextRun(t, now)
		if nerr != nil {
			s.logger.Warn("中断任务无法计算下次触发时间，已暂停", "task_id", t.ID, "error", nerr)
			_ = s.store.SetState(ctx, t.ID, StatePaused, nil)
			continue
		}
		if err := s.store.SetState(ctx, t.ID, StateEnabled, &next); err != nil {
			return err
		}
		s.logger.Warn("检测到崩溃遗留的任务认领，已记录中断并重算下次触发", "task_id", t.ID)
	}
	return s.store.ClearClaims(ctx)
}

// syncUnavailable 在每次扫描时检查插件可用性，停用/卸载后把任务置 unavailable 并停止触发。
func (s *Service) syncUnavailable(ctx context.Context) {
	if s.registry == nil {
		return
	}
	tasks, err := s.store.List(ctx)
	if err != nil {
		s.logger.Warn("检查任务插件可用性失败", "error", err)
		return
	}
	for _, t := range tasks {
		if t.State == StateUnavailable {
			// 插件恢复后由用户显式启用，不自动回到 enabled。
			continue
		}
		if t.State != StateEnabled {
			continue
		}
		available := false
		if t.InstanceID != "" && s.instances != nil {
			if inst, ierr := s.instances.GetInstance(ctx, t.InstanceID); ierr == nil {
				available = inst.Enabled && inst.Callable && inst.Drift == instances.DriftNone
			}
		} else if row, rerr := s.registry.Get(ctx, t.PluginID); rerr == nil {
			available = row.State == registry.StateEnabled
		}
		if !available {
			if err := s.store.SetState(ctx, t.ID, StateUnavailable, nil); err != nil {
				s.logger.Warn("置任务为不可用失败", "task_id", t.ID, "error", err)
				continue
			}
			s.logger.Warn("执行目标不可用，任务已停止触发", "task_id", t.ID,
				"instance_id", t.InstanceID, "plugin_id", t.PluginID)
		}
	}
}

// markMissed 对启动时已过期的启用任务写一条 missed 记录并推进下次触发时间，不补跑。
func (s *Service) markMissed(ctx context.Context) error {
	now := s.now()
	due, err := s.store.ListDue(ctx, now)
	if err != nil {
		return err
	}
	for _, t := range due {
		next, err := NextRun(t, now)
		if err != nil {
			s.logger.Warn("任务无法计算下次触发时间，已暂停", "task_id", t.ID, "error", err)
			_ = s.store.SetState(ctx, t.ID, StatePaused, nil)
			continue
		}
		s.recordSynthetic(t, registry.StatusMissed, "missed", now)
		if err := s.store.AdvanceMissed(ctx, t.ID, next); err != nil {
			return err
		}
		s.logger.Warn("检测到错过的执行，已记录但不补跑", "task_id", t.ID, "next_run_at", next.Format(time.RFC3339))
	}
	return nil
}

func (s *Service) scan(ctx context.Context, now time.Time) {
	s.syncUnavailable(ctx)
	due, err := s.store.ListDue(ctx, now)
	if err != nil {
		s.logger.Error("扫描到期任务失败", "error", err)
		return
	}
	for _, t := range due {
		next, err := NextRun(t, now)
		if err != nil {
			s.logger.Error("计算下次触发时间失败，任务转为不可用", "task_id", t.ID, "error", err)
			_ = s.store.SetState(ctx, t.ID, StateUnavailable, nil)
			continue
		}
		// 重叠策略 skip：本任务仍在执行时跳过本次触发并留痕。
		if !s.markRunning(t.ID) {
			s.recordSynthetic(t, registry.StatusSkipped, "overlap_skip", now)
			_ = s.store.SetNextRun(ctx, t.ID, next, now)
			continue
		}
		token := newToken()
		claimed, err := s.store.Claim(ctx, t.ID, token, now, *t.NextRunAt)
		if err != nil {
			s.unmarkRunning(t.ID)
			s.logger.Error("认领任务失败", "task_id", t.ID, "error", err)
			continue
		}
		if !claimed {
			s.unmarkRunning(t.ID)
			continue
		}
		if err := s.store.SetNextRun(ctx, t.ID, next, now); err != nil {
			s.logger.Error("推进任务触发时间失败", "task_id", t.ID, "error", err)
		}
		s.wg.Add(1)
		go func(task Task) {
			defer s.wg.Done()
			defer s.unmarkRunning(task.ID)
			s.execute(task, registry.SourceScheduled)
			releaseCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := s.store.ReleaseClaim(releaseCtx, task.ID); err != nil {
				s.logger.Error("释放任务认领失败", "task_id", task.ID, "error", err)
			}
		}(t)
	}
}

// execute 按重试策略调用插件能力；结果进入统一执行记录。
func (s *Service) execute(t Task, source string) (plugin.InvokeResult, error) {
	attempts := t.Retries + 1
	if attempts < 1 {
		attempts = 1
	}
	var last plugin.InvokeResult
	for i := 0; i < attempts; i++ {
		res, err := s.executeWithSubject(context.Background(), t, source, systemSubject)
		last = res
		if err != nil {
			// 插件服务已负责写入执行记录；这里只记录调度器侧错误，避免重复。
			s.logger.Error("定时任务调用失败", "task_id", t.ID, "error", err)
			return last, err
		}
		if res.Status == registry.StatusOK {
			return res, nil
		}
		s.logger.Warn("定时任务执行失败", "task_id", t.ID, "attempt", i+1, "status", res.Status)
	}
	return last, nil
}

// recordSynthetic 写入 missed/skipped 等没有经过插件调用的执行记录。
func (s *Service) recordSynthetic(t Task, status, category string, at time.Time) {
	invocationID := "task-" + newToken()
	s.plugins.RecordExecution(registry.Execution{
		InvocationID:  invocationID,
		PluginID:      t.PluginID,
		CapabilityID:  t.CapabilityID,
		Source:        registry.SourceScheduled,
		Subject:       systemSubject.ID,
		StartedAt:     at,
		FinishedAt:    at,
		DurationMS:    0,
		Status:        status,
		ErrorCategory: category,
		TaskID:        t.ID,
	})
}

func (s *Service) markRunning(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.running[id] {
		return false
	}
	s.running[id] = true
	return true
}

func (s *Service) unmarkRunning(id string) {
	s.mu.Lock()
	delete(s.running, id)
	s.mu.Unlock()
}

func newToken() string {
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(buf)
}
