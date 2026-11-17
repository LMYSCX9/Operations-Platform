package scheduler

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"operations-platform/internal/plugin"
	"operations-platform/internal/plugin/authz"
	"operations-platform/internal/plugin/instances"
	"operations-platform/internal/plugin/manifest"
	"operations-platform/internal/plugin/registry"
)

// List 返回全部任务。
func (s *Service) List(ctx context.Context, subj authz.Subject) ([]Task, error) {
	if err := s.authorize(ctx, subj, authz.ActionTaskRead, authz.Target{Type: "task"}, "无任务查看权限"); err != nil {
		return nil, err
	}
	return s.store.List(ctx)
}

// Get 返回单个任务。
func (s *Service) Get(ctx context.Context, subj authz.Subject, id string) (Task, error) {
	if err := s.authorize(ctx, subj, authz.ActionTaskRead, authz.Target{Type: "task", ID: id}, "无任务查看权限"); err != nil {
		return Task{}, err
	}
	t, err := s.store.Get(ctx, id)
	if err != nil {
		return Task{}, taskNotFound(err, id)
	}
	return t, nil
}

// Create 新建任务；默认暂停，需显式启用后才进入调度（OQ-042）。
func (s *Service) Create(ctx context.Context, subj authz.Subject, in TaskInput) (Task, error) {
	if err := s.authorize(ctx, subj, authz.ActionTaskWrite, authz.Target{Type: "task"}, "无任务创建权限"); err != nil {
		return Task{}, err
	}
	if err := in.Validate(); err != nil {
		return Task{}, err
	}
	if err := s.validateTarget(ctx, in.InstanceID, in.PluginID, in.CapabilityID); err != nil {
		return Task{}, err
	}
	if err := s.validateInterval(in); err != nil {
		return Task{}, err
	}

	t := Task{
		ID:              "task-" + newToken(),
		Name:            in.Name,
		Description:     in.Description,
		InstanceID:      resolveInstanceID(in.InstanceID, in.PluginID),
		PluginID:        resolvePluginID(in.InstanceID, in.PluginID),
		CapabilityID:    in.CapabilityID,
		Input:           normalizeInput(in.Input),
		TriggerType:     in.TriggerType,
		CronExpr:        in.CronExpr,
		IntervalSeconds: in.IntervalSeconds,
		Timezone:        in.Timezone,
		TimeoutSeconds:  s.resolveTimeout(in.TimeoutSeconds),
		Retries:         s.resolveRetries(in.Retries),
		OverlapPolicy:   OverlapSkip,
		State:           in.State,
		CreatedBy:       subj.ID,
	}
	if t.State == "" {
		t.State = StatePaused
	}
	if t.State == StateEnabled {
		next, err := NextRun(t, s.now())
		if err != nil {
			return Task{}, &Error{Code: CodeInputInvalid, Message: err.Error()}
		}
		t.NextRunAt = &next
	}
	if err := s.store.Create(ctx, t); err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return Task{}, &Error{Code: CodeConflict, Message: "任务名称已存在", Details: map[string]any{"field": "name"}}
		}
		return Task{}, err
	}
	s.recordAudit(ctx, subj, authz.ActionTaskWrite, authz.Target{Type: "task", ID: t.ID}, "success", "创建任务")
	return s.store.Get(ctx, t.ID)
}

// Update 编辑任务定义；启用状态下会重算下次触发时间。
func (s *Service) Update(ctx context.Context, subj authz.Subject, id string, in TaskInput) (Task, error) {
	target := authz.Target{Type: "task", ID: id}
	if err := s.authorize(ctx, subj, authz.ActionTaskWrite, target, "无任务编辑权限"); err != nil {
		return Task{}, err
	}
	if err := in.Validate(); err != nil {
		return Task{}, err
	}
	if err := s.validateTarget(ctx, in.InstanceID, in.PluginID, in.CapabilityID); err != nil {
		return Task{}, err
	}
	if err := s.validateInterval(in); err != nil {
		return Task{}, err
	}
	existing, err := s.store.Get(ctx, id)
	if err != nil {
		return Task{}, taskNotFound(err, id)
	}
	existing.Name = in.Name
	existing.Description = in.Description
	existing.InstanceID = resolveInstanceID(in.InstanceID, in.PluginID)
	existing.PluginID = resolvePluginID(in.InstanceID, in.PluginID)
	existing.CapabilityID = in.CapabilityID
	existing.Input = normalizeInput(in.Input)
	existing.TriggerType = in.TriggerType
	existing.CronExpr = in.CronExpr
	existing.IntervalSeconds = in.IntervalSeconds
	existing.Timezone = in.Timezone
	existing.TimeoutSeconds = s.resolveTimeout(in.TimeoutSeconds)
	existing.Retries = s.resolveRetries(in.Retries)
	if in.State != "" {
		existing.State = in.State
	}
	if err := s.store.Update(ctx, existing); err != nil {
		return Task{}, taskNotFound(err, id)
	}

	// 编辑后重新计算下次触发；暂停/不可用任务不排期。
	var nextPtr *time.Time
	if existing.State == StateEnabled {
		next, nerr := NextRun(existing, s.now())
		if nerr != nil {
			return Task{}, &Error{Code: CodeInputInvalid, Message: nerr.Error()}
		}
		nextPtr = &next
	}
	if err := s.store.SetState(ctx, id, existing.State, nextPtr); err != nil {
		return Task{}, err
	}
	s.recordAudit(ctx, subj, authz.ActionTaskWrite, target, "success", "编辑任务")
	return s.store.Get(ctx, id)
}

// Delete 删除任务；历史执行记录保留。
func (s *Service) Delete(ctx context.Context, subj authz.Subject, id string) error {
	target := authz.Target{Type: "task", ID: id}
	if err := s.authorize(ctx, subj, authz.ActionTaskWrite, target, "无任务删除权限"); err != nil {
		return err
	}
	if err := s.store.Delete(ctx, id); err != nil {
		return taskNotFound(err, id)
	}
	s.recordAudit(ctx, subj, authz.ActionTaskWrite, target, "success", "删除任务")
	return nil
}

// Enable 启用任务并排期。
func (s *Service) Enable(ctx context.Context, subj authz.Subject, id string) (Task, error) {
	target := authz.Target{Type: "task", ID: id}
	if err := s.authorize(ctx, subj, authz.ActionTaskWrite, target, "无任务启用权限"); err != nil {
		return Task{}, err
	}
	t, err := s.store.Get(ctx, id)
	if err != nil {
		return Task{}, taskNotFound(err, id)
	}
	if err := s.validateTarget(ctx, t.InstanceID, t.PluginID, t.CapabilityID); err != nil {
		return Task{}, err
	}
	next, err := NextRun(t, s.now())
	if err != nil {
		return Task{}, &Error{Code: CodeInputInvalid, Message: err.Error()}
	}
	if err := s.store.SetState(ctx, id, StateEnabled, &next); err != nil {
		return Task{}, err
	}
	s.recordAudit(ctx, subj, authz.ActionTaskWrite, target, "success", "启用任务")
	return s.store.Get(ctx, id)
}

// Pause 暂停任务并清空排期。
func (s *Service) Pause(ctx context.Context, subj authz.Subject, id string) (Task, error) {
	target := authz.Target{Type: "task", ID: id}
	if err := s.authorize(ctx, subj, authz.ActionTaskWrite, target, "无任务暂停权限"); err != nil {
		return Task{}, err
	}
	if err := s.store.SetState(ctx, id, StatePaused, nil); err != nil {
		return Task{}, taskNotFound(err, id)
	}
	s.recordAudit(ctx, subj, authz.ActionTaskWrite, target, "success", "暂停任务")
	return s.store.Get(ctx, id)
}

// RunNow 由用户立即执行一次，source=manual，并保留 task_id。
func (s *Service) RunNow(ctx context.Context, subj authz.Subject, id string) (plugin.InvokeResult, error) {
	target := authz.Target{Type: "task", ID: id}
	if err := s.authorize(ctx, subj, authz.ActionTaskRun, target, "无任务执行权限"); err != nil {
		return plugin.InvokeResult{}, err
	}
	t, err := s.store.Get(ctx, id)
	if err != nil {
		return plugin.InvokeResult{}, taskNotFound(err, id)
	}
	if t.State == StateUnavailable {
		return plugin.InvokeResult{}, &Error{Code: CodeConflict, Message: "任务已不可用，请先启用"}
	}
	if err := s.validateTarget(ctx, t.InstanceID, t.PluginID, t.CapabilityID); err != nil {
		return plugin.InvokeResult{}, err
	}
	// 手动执行同样受「同一任务不并发」约束。
	if !s.markRunning(t.ID) {
		return plugin.InvokeResult{}, &Error{Code: CodeConflict, Message: "任务正在执行中，请稍后再试"}
	}
	defer s.unmarkRunning(t.ID)

	res, err := s.executeWithSubject(ctx, t, registry.SourceManual, subj)
	if err != nil {
		return res, err
	}
	s.recordAudit(ctx, subj, authz.ActionTaskRun, target, "success", "手动执行任务")
	return res, nil
}

func (s *Service) executeWithSubject(ctx context.Context, t Task, source string, subj authz.Subject) (plugin.InvokeResult, error) {
	timeout := time.Duration(t.TimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = s.defaultTimeout
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if t.InstanceID != "" {
		return s.plugins.InvokeInstanceWithTask(runCtx, subj, t.InstanceID, t.CapabilityID, t.Input, source, t.ID)
	}
	return s.plugins.InvokeWithTask(runCtx, subj, t.PluginID, t.CapabilityID, t.Input, source, t.ID)
}

// MissedCounts 返回近 7 天每个任务的错过次数（DOC-10 §8.3）。
func (s *Service) MissedCounts(ctx context.Context, tasks []Task) map[string]int {
	out := make(map[string]int, len(tasks))
	if s.registry == nil {
		return out
	}
	since := s.now().Add(-7 * 24 * time.Hour)
	for _, t := range tasks {
		n, err := s.registry.CountByTask(ctx, t.ID, registry.StatusMissed, since)
		if err != nil {
			s.logger.Warn("统计错过次数失败", "task_id", t.ID, "error", err)
			continue
		}
		if n > 0 {
			out[t.ID] = n
		}
	}
	return out
}

func (s *Service) resolveTimeout(v int) int {
	if v > 0 {
		return v
	}
	return int(s.defaultTimeout.Seconds())
}

func (s *Service) resolveRetries(v *int) int {
	if v != nil {
		return *v
	}
	return s.defaultRetries
}

func (s *Service) validateInterval(in TaskInput) error {
	if in.TriggerType != TriggerInterval {
		return nil
	}
	minSeconds := int(s.minInterval.Seconds())
	if in.IntervalSeconds < minSeconds {
		return &Error{
			Code:    CodeInputInvalid,
			Message: fmt.Sprintf("固定间隔不得小于 %d 秒", minSeconds),
			Details: map[string]any{"field": "interval_seconds", "minimum": minSeconds},
		}
	}
	return nil
}

// validatePluginCapability 要求插件存在、已启用且声明了目标能力（DOC-10 §8.4）。
// validateTarget 校验执行目标：有实例存库时按实例→类型→能力判定，否则退回注册表。
func (s *Service) validateTarget(ctx context.Context, instanceID, pluginID, capabilityID string) error {
	instanceID = strings.TrimSpace(instanceID)
	if s.instances != nil && instanceID != "" {
		inst, err := s.instances.GetInstance(ctx, instanceID)
		if errors.Is(err, instances.ErrNotFound) {
			return &Error{Code: CodeDependency, Message: fmt.Sprintf("实例 %s 未登记", instanceID),
				Details: map[string]any{"field": "instance_id"}}
		}
		if err != nil {
			return err
		}
		caps, err := s.instances.ListCapabilities(ctx, inst.TypeID)
		if err != nil {
			return err
		}
		for _, c := range caps {
			if c.CapabilityID == capabilityID {
				return nil
			}
		}
		return &Error{Code: CodeInputInvalid,
			Message: fmt.Sprintf("实例 %s 的类型 %s 未声明能力 %s", instanceID, inst.TypeID, capabilityID),
			Details: map[string]any{"field": "capability_id"}}
	}
	if strings.TrimSpace(pluginID) == "" {
		return &Error{Code: CodeInputInvalid, Message: "缺少执行目标", Details: map[string]any{"field": "instance_id"}}
	}
	return s.validatePluginCapability(ctx, pluginID, capabilityID)
}

func (s *Service) validatePluginCapability(ctx context.Context, pluginID, capabilityID string) error {
	if s.registry == nil {
		return nil
	}
	row, err := s.registry.Get(ctx, pluginID)
	if errors.Is(err, registry.ErrNotFound) {
		return &Error{Code: CodeDependency, Message: fmt.Sprintf("插件 %s 未登记", pluginID), Details: map[string]any{"field": "plugin_id"}}
	}
	if err != nil {
		return err
	}
	if row.State != registry.StateEnabled {
		return &Error{Code: CodeDependency, Message: fmt.Sprintf("插件 %s 当前为 %s，需先启用", pluginID, row.State), Details: map[string]any{"field": "plugin_id"}}
	}
	var m manifest.Manifest
	if err := yaml.Unmarshal([]byte(row.ManifestYAML), &m); err != nil {
		return &Error{Code: CodeDependency, Message: "插件清单无法解析，无法校验能力"}
	}
	if _, ok := m.Capability(capabilityID); !ok {
		return &Error{Code: CodeInputInvalid, Message: fmt.Sprintf("插件 %s 未声明能力 %s", pluginID, capabilityID), Details: map[string]any{"field": "capability_id"}}
	}
	return nil
}

// resolveInstanceID 未显式给实例时，把插件标识作为本机实例别名。
func resolveInstanceID(instanceID, pluginID string) string {
	instanceID = strings.TrimSpace(instanceID)
	if instanceID != "" {
		return instanceID
	}
	pluginID = strings.TrimSpace(pluginID)
	if pluginID == "" {
		return ""
	}
	return instances.LocalInstanceID(pluginID)
}

// resolvePluginID 从实例标识反推类型标识，供列表/审计展示与旧口径兼容。
func resolvePluginID(instanceID, pluginID string) string {
	if strings.TrimSpace(pluginID) != "" {
		return strings.TrimSpace(pluginID)
	}
	instanceID = strings.TrimSpace(instanceID)
	if strings.HasPrefix(instanceID, "local:") {
		return strings.TrimPrefix(instanceID, "local:")
	}
	if strings.HasPrefix(instanceID, "remote:") {
		rest := strings.TrimPrefix(instanceID, "remote:")
		if idx := strings.Index(rest, ":"); idx >= 0 {
			return rest[idx+1:]
		}
	}
	return ""
}

func normalizeInput(raw []byte) []byte {
	if len(raw) == 0 {
		return []byte("{}")
	}
	return raw
}

func taskNotFound(err error, id string) error {
	if errors.Is(err, ErrNotFound) {
		return &Error{Code: CodeNotFound, Message: fmt.Sprintf("任务 %s 不存在", id)}
	}
	return err
}
