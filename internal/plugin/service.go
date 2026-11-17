// Package plugin 编排插件发现、注册表、授权、调用与执行记录（M1）。
//
// 本包是插件能力的唯一入口：HTTP 层只做编解码，不直接触碰 runtime 或 SQL。
// 所有默认值来自 config，禁止在本包内写死（OQ-039 实现基线）。
package plugin

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"

	"operations-platform/internal/pki"
	"operations-platform/internal/plugin/authz"
	"operations-platform/internal/plugin/discovery"
	"operations-platform/internal/plugin/instances"
	"operations-platform/internal/plugin/manifest"
	"operations-platform/internal/plugin/registry"
	"operations-platform/internal/plugin/remote"
	"operations-platform/internal/plugin/runtime"
	"operations-platform/internal/secret"
	"operations-platform/internal/store"
)

// Error 是插件服务的结构化错误，由 HTTP 层映射状态码。
type Error struct {
	Code    string         `json:"code"`
	Message string         `json:"message"`
	Details map[string]any `json:"details,omitempty"`
}

func (e *Error) Error() string { return e.Code + ": " + e.Message }

// Plugin 是对外暴露的插件摘要。
type Plugin struct {
	ID             string    `json:"plugin_id"`
	Version        string    `json:"plugin_version"`
	Form           string    `json:"form"`
	State          string    `json:"state"`
	ContractOK     bool      `json:"contract_ok"`
	ContractReason string    `json:"contract_reason,omitempty"`
	Capabilities   []string  `json:"capabilities"`
	InstallPath    string    `json:"install_path"`
	UpdatedAt      time.Time `json:"updated_at"`
}

// PluginDetail 额外包含清单原文与校验结果。
type PluginDetail struct {
	Plugin
	ManifestYAML string                `json:"manifest_yaml"`
	Manifest     *manifest.Manifest    `json:"manifest,omitempty"`
	Validation   []manifest.FieldError `json:"validation,omitempty"`
}

// InvokeResult 是一次能力调用的结果。
type InvokeResult struct {
	InvocationID string `json:"invocation_id"`
	// InstanceID 是 M6 的调用目标；PluginID 保留为类型标识以兼容既有界面。
	InstanceID    string             `json:"instance_id,omitempty"`
	PluginID      string             `json:"plugin_id"`
	CapabilityID  string             `json:"capability_id"`
	Status        string             `json:"status"`
	Output        json.RawMessage    `json:"output,omitempty"`
	Error         *runtime.HostError `json:"error,omitempty"`
	PluginVersion string             `json:"plugin_version,omitempty"`
	DurationMS    int64              `json:"duration_ms"`
}

// Options 是服务依赖；全部显式注入，便于测试替换。
type Options struct {
	Registry       *registry.Registry
	Scanner        *discovery.Scanner
	Authorizer     authz.Authorizer
	Queue          *store.BatchQueue
	Logger         *slog.Logger
	InvokeTimeout  time.Duration
	MaxResultBytes int64
	MaxConcurrency int
	// Audit 是审计钩子；M1 未接审计表，M2 注入真实实现。
	Audit func(ctx context.Context, subj authz.Subject, action authz.Action, target authz.Target, result, reason string)

	// 以下为 M6 实例与远端能力；全部可空，为空时远端/实例特性显式不可用。
	Instances    *instances.Store
	PKI          *pki.Manager
	Remote       *remote.Client
	Hosts        HostLocator
	Box          *secret.Box
	DataDir      string
	ProbeTTL     time.Duration
	DialTimeout  time.Duration
	ReadyTimeout time.Duration
}

// Service 汇总插件运行时所需的全部协作者。
type Service struct {
	registry   *registry.Registry
	scanner    *discovery.Scanner
	authorizer authz.Authorizer
	queue      *store.BatchQueue
	logger     *slog.Logger
	audit      func(ctx context.Context, subj authz.Subject, action authz.Action, target authz.Target, result, reason string)

	timeout    time.Duration
	maxBytes   int64
	sem        chan struct{}
	serialMu   sync.Mutex
	serialLock map[string]*sync.Mutex

	instances    *instances.Store
	pki          *pki.Manager
	remote       *remote.Client
	hosts        HostLocator
	box          *secret.Box
	dataDir      string
	probeTTL     time.Duration
	dialTimeout  time.Duration
	readyTimeout time.Duration

	reportMu   sync.RWMutex
	lastReport discovery.Report
}

// NewService 创建插件服务；缺少注册表或扫描器视为装配错误。
func NewService(o Options) (*Service, error) {
	if o.Registry == nil {
		return nil, errors.New("plugin: Registry 不能为空")
	}
	if o.Scanner == nil {
		return nil, errors.New("plugin: Scanner 不能为空")
	}
	if o.Logger == nil {
		return nil, errors.New("plugin: Logger 不能为空")
	}
	if o.InvokeTimeout <= 0 {
		o.InvokeTimeout = runtime.DefaultTimeout
	}
	if o.MaxResultBytes <= 0 {
		o.MaxResultBytes = runtime.DefaultMaxResultBytes
	}
	if o.MaxConcurrency <= 0 {
		o.MaxConcurrency = 1
	}
	if o.ProbeTTL <= 0 {
		o.ProbeTTL = 5 * time.Minute
	}
	s := &Service{
		registry:     o.Registry,
		scanner:      o.Scanner,
		authorizer:   o.Authorizer,
		queue:        o.Queue,
		logger:       o.Logger,
		audit:        o.Audit,
		timeout:      o.InvokeTimeout,
		maxBytes:     o.MaxResultBytes,
		sem:          make(chan struct{}, o.MaxConcurrency),
		serialLock:   make(map[string]*sync.Mutex),
		instances:    o.Instances,
		pki:          o.PKI,
		remote:       o.Remote,
		hosts:        o.Hosts,
		box:          o.Box,
		dataDir:      o.DataDir,
		probeTTL:     o.ProbeTTL,
		dialTimeout:  o.DialTimeout,
		readyTimeout: o.ReadyTimeout,
	}
	if s.authorizer == nil {
		// 默认拒绝：未装配授权器时不放行任何调用。
		s.authorizer = denyAll{}
	}
	return s, nil
}

type denyAll struct{}

func (denyAll) Authorize(context.Context, authz.Subject, authz.Action, authz.Target) authz.Decision {
	return authz.Decision{Allowed: false, Reason: "no matching rule"}
}

// Scan 触发一次插件目录扫描，并保留最近一次报告供列表接口展示。
func (s *Service) Scan(ctx context.Context) (discovery.Report, error) {
	rep, err := s.scanner.Scan(ctx)
	if err != nil {
		return rep, err
	}
	s.reportMu.Lock()
	s.lastReport = rep
	s.reportMu.Unlock()
	// 目录扫描只产生类型候选与本机实例候选；不自动创建远端实例、不自动启用（DOC-17 §4.1）。
	s.syncTypes(ctx)
	return rep, nil
}

// LastScanReport 返回最近一次扫描报告。
func (s *Service) LastScanReport() discovery.Report {
	s.reportMu.RLock()
	defer s.reportMu.RUnlock()
	return s.lastReport
}

// List 返回插件摘要列表。
func (s *Service) List(ctx context.Context, subj authz.Subject) ([]Plugin, error) {
	if d := s.authorizer.Authorize(ctx, subj, authz.ActionPluginRead, authz.Target{Type: "plugin"}); !d.Allowed {
		s.recordAudit(ctx, subj, authz.ActionPluginRead, authz.Target{Type: "plugin"}, "denied", d.Reason)
		return nil, &Error{Code: runtime.CodePermissionDenied, Message: "无插件查看权限", Details: map[string]any{"reason": d.Reason}}
	}
	rows, err := s.registry.List(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]Plugin, 0, len(rows))
	for _, row := range rows {
		out = append(out, toPlugin(row))
	}
	return out, nil
}

// Get 返回插件详情，包含清单原文与重新校验的结果。
func (s *Service) Get(ctx context.Context, subj authz.Subject, id string) (PluginDetail, error) {
	if d := s.authorizer.Authorize(ctx, subj, authz.ActionPluginRead, authz.Target{Type: "plugin", ID: id}); !d.Allowed {
		s.recordAudit(ctx, subj, authz.ActionPluginRead, authz.Target{Type: "plugin", ID: id}, "denied", d.Reason)
		return PluginDetail{}, &Error{Code: runtime.CodePermissionDenied, Message: "无插件查看权限", Details: map[string]any{"reason": d.Reason}}
	}
	row, err := s.registry.Get(ctx, id)
	if errors.Is(err, registry.ErrNotFound) {
		return PluginDetail{}, &Error{Code: "not_found", Message: fmt.Sprintf("插件 %s 未登记", id)}
	}
	if err != nil {
		return PluginDetail{}, err
	}
	detail := PluginDetail{Plugin: toPlugin(row), ManifestYAML: row.ManifestYAML}
	if m, errs := manifest.Decode([]byte(row.ManifestYAML), row.InstallPath); len(errs) > 0 {
		detail.Validation = errs
	} else if err == nil {
		detail.Manifest = m
	}
	return detail, nil
}

// Enable 启用插件；启用前复核清单与契约版本。
func (s *Service) Enable(ctx context.Context, subj authz.Subject, id string) (Plugin, error) {
	target := authz.Target{Type: "plugin", ID: id}
	if d := s.authorizer.Authorize(ctx, subj, authz.ActionPluginEnable, target); !d.Allowed {
		s.recordAudit(ctx, subj, authz.ActionPluginEnable, target, "denied", d.Reason)
		return Plugin{}, &Error{Code: runtime.CodePermissionDenied, Message: "无插件启用权限", Details: map[string]any{"reason": d.Reason}}
	}
	row, err := s.registry.Get(ctx, id)
	if errors.Is(err, registry.ErrNotFound) {
		return Plugin{}, &Error{Code: "not_found", Message: fmt.Sprintf("插件 %s 未登记", id)}
	}
	if err != nil {
		return Plugin{}, err
	}

	// 启用前必须重新读取磁盘清单：注册表登记后目录可能已被替换。
	m, errs, err := manifest.Load(row.InstallPath)
	if err != nil {
		return Plugin{}, fmt.Errorf("读取插件 %s 清单失败: %w", id, err)
	}
	if len(errs) > 0 {
		return Plugin{}, &Error{
			Code:    runtime.CodeVersionIncompatible,
			Message: "插件清单已失效，拒绝启用",
			Details: map[string]any{"validation": errs},
		}
	}
	if !row.ContractOK {
		return Plugin{}, &Error{
			Code:    runtime.CodeVersionIncompatible,
			Message: "插件声明的契约版本与底座不兼容，拒绝启用",
			Details: map[string]any{"reason": row.ContractReason},
		}
	}

	// 清单有变更时先同步注册表，保持 list/detail 与磁盘一致。
	if _, err := s.registry.Upsert(ctx, registry.Plugin{
		ID:             m.Identity.ID,
		Version:        m.Version.Plugin,
		Form:           m.Form,
		InstallPath:    row.InstallPath,
		ManifestYAML:   string(m.Raw),
		ManifestSHA256: m.SHA256,
		ContractOK:     true,
	}); err != nil {
		return Plugin{}, err
	}
	if err := s.registry.SetState(ctx, id, registry.StateEnabled); err != nil {
		return Plugin{}, err
	}
	// 实例是授权与调用的实际目标；本机别名必须与实例启用状态一致。
	if s.instances != nil {
		if _, _, err := s.instances.UpsertLocalInstance(ctx, id, m.Version.Plugin); err != nil {
			return Plugin{}, err
		}
		if err := s.instances.SetEnabled(ctx, instances.LocalInstanceID(id), true); err != nil {
			return Plugin{}, err
		}
	}
	s.recordAudit(ctx, subj, authz.ActionPluginEnable, target, "success", "")
	row.State = registry.StateEnabled
	return toPlugin(row), nil
}

// Disable 停用插件。
func (s *Service) Disable(ctx context.Context, subj authz.Subject, id string) (Plugin, error) {
	target := authz.Target{Type: "plugin", ID: id}
	if d := s.authorizer.Authorize(ctx, subj, authz.ActionPluginDisable, target); !d.Allowed {
		s.recordAudit(ctx, subj, authz.ActionPluginDisable, target, "denied", d.Reason)
		return Plugin{}, &Error{Code: runtime.CodePermissionDenied, Message: "无插件停用权限", Details: map[string]any{"reason": d.Reason}}
	}
	if err := s.registry.SetState(ctx, id, registry.StateDisabled); err != nil {
		if errors.Is(err, registry.ErrNotFound) {
			return Plugin{}, &Error{Code: "not_found", Message: fmt.Sprintf("插件 %s 未登记", id)}
		}
		return Plugin{}, err
	}
	if s.instances != nil {
		if err := s.instances.SetEnabled(ctx, instances.LocalInstanceID(id), false); err != nil {
			return Plugin{}, err
		}
	}
	s.recordAudit(ctx, subj, authz.ActionPluginDisable, target, "success", "")
	row, err := s.registry.Get(ctx, id)
	if err != nil {
		return Plugin{}, err
	}
	return toPlugin(row), nil
}

// Invoke 执行一次能力调用，并无论成败都写一条执行记录。
func (s *Service) Invoke(ctx context.Context, subj authz.Subject, pluginID, capabilityID string, input json.RawMessage, source string) (InvokeResult, error) {
	return s.InvokeWithTask(ctx, subj, pluginID, capabilityID, input, source, "")
}

// InvokeWithTask 在 Invoke 基础上携带定时任务标识（M4 复用）。
//
// M6 起调用目标是实例：插件标识是本机实例 local:<plugin> 的别名（DOC-17 §8）。
func (s *Service) InvokeWithTask(ctx context.Context, subj authz.Subject, pluginID, capabilityID string, input json.RawMessage, source, taskID string) (InvokeResult, error) {
	if s.instances != nil {
		return s.invokeInstanceWithTask(ctx, subj, instances.LocalInstanceID(pluginID), capabilityID, input, source, taskID)
	}
	return s.invokeLegacy(ctx, subj, pluginID, capabilityID, input, source, taskID)
}

// invokeLegacy 是未装配实例存储时的降级路径，仅用于兼容既有单元测试与最小部署。
func (s *Service) invokeLegacy(ctx context.Context, subj authz.Subject, pluginID, capabilityID string, input json.RawMessage, source, taskID string) (InvokeResult, error) {
	started := time.Now().UTC()
	invocationID := newInvocationID()
	target := authz.Target{Type: "capability", ID: capabilityID}
	result := InvokeResult{InvocationID: invocationID, PluginID: pluginID, CapabilityID: capabilityID}
	if source == "" {
		source = registry.SourceAPI
	}

	finish := func(status, category string) {
		result.Status = status
		result.DurationMS = time.Since(started).Milliseconds()
		s.enqueueExecution(registry.Execution{
			InvocationID:  invocationID,
			PluginID:      pluginID,
			CapabilityID:  capabilityID,
			Source:        source,
			Subject:       subj.ID,
			StartedAt:     started,
			FinishedAt:    time.Now().UTC(),
			DurationMS:    result.DurationMS,
			Status:        status,
			ErrorCategory: category,
			TaskID:        taskID,
			InputSummary:  summarizeJSON(input),
			OutputSummary: summarizeJSON(result.Output),
		})
	}

	// 1. 授权：默认拒绝，拒绝也要留执行记录（spec §5.8）。
	if d := s.authorizer.Authorize(ctx, subj, authz.ActionPluginInvoke, target); !d.Allowed {
		s.recordAudit(ctx, subj, authz.ActionPluginInvoke, target, "denied", d.Reason)
		result.Error = &runtime.HostError{Code: runtime.CodePermissionDenied, Message: "无插件调用权限"}
		finish(registry.StatusError, runtime.CodePermissionDenied)
		return result, nil
	}

	// 2. 注册表与状态校验。
	row, err := s.registry.Get(ctx, pluginID)
	if errors.Is(err, registry.ErrNotFound) {
		result.Error = &runtime.HostError{Code: runtime.CodeCapabilityUnknown, Message: fmt.Sprintf("插件 %s 未登记", pluginID)}
		finish(registry.StatusError, runtime.CodeCapabilityUnknown)
		return result, nil
	}
	if err != nil {
		return result, err
	}
	if row.State != registry.StateEnabled {
		result.Error = &runtime.HostError{
			Code:    runtime.CodeNotReady,
			Message: fmt.Sprintf("插件 %s 当前为 %s，需显式启用后才能调用", pluginID, row.State),
		}
		finish(registry.StatusError, runtime.CodeNotReady)
		return result, nil
	}

	// 3. 解析清单：能力必须已声明，且取 concurrency 决定是否串行。
	var m manifest.Manifest
	if err := yaml.Unmarshal([]byte(row.ManifestYAML), &m); err != nil {
		result.Error = &runtime.HostError{Code: runtime.CodeInternalError, Message: "已登记清单无法解析: " + err.Error()}
		finish(registry.StatusError, runtime.CodeInternalError)
		return result, nil
	}
	capability, ok := m.Capability(capabilityID)
	if !ok {
		result.Error = &runtime.HostError{
			Code:    runtime.CodeCapabilityUnknown,
			Message: fmt.Sprintf("插件 %s 未声明能力 %s", pluginID, capabilityID),
		}
		finish(registry.StatusError, runtime.CodeCapabilityUnknown)
		return result, nil
	}
	if !row.ContractOK {
		result.Error = &runtime.HostError{
			Code:    runtime.CodeVersionIncompatible,
			Message: "插件契约版本与底座不兼容: " + row.ContractReason,
		}
		finish(registry.StatusError, runtime.CodeVersionIncompatible)
		return result, nil
	}

	// 4. 并发闸门：全局信号量 + 能力级串行锁。
	select {
	case s.sem <- struct{}{}:
		defer func() { <-s.sem }()
	case <-ctx.Done():
		result.Error = &runtime.HostError{Code: runtime.CodeCancelled, Message: "调用方已取消"}
		finish(registry.StatusError, runtime.CodeCancelled)
		return result, ctx.Err()
	}

	unlock := func() {}
	if capability.Concurrency == "serial" {
		mu := s.serialMutex(capabilityID)
		mu.Lock()
		unlock = mu.Unlock
	}
	defer unlock()

	// 5. 执行。
	res := runtime.Invoke(ctx, runtime.InvokeSpec{
		Root:           row.InstallPath,
		Executable:     m.Entry.Executable,
		Capability:     capabilityID,
		Input:          input,
		Subject:        subj.ID,
		InvocationID:   invocationID,
		Deadline:       s.timeout,
		MaxResultBytes: s.maxBytes,
	})
	result.Output = res.Output
	result.PluginVersion = res.PluginVersion

	if res.Status == registry.StatusOK {
		finish(registry.StatusOK, "")
		s.recordAudit(ctx, subj, authz.ActionPluginInvoke, target, "success", "")
		return result, nil
	}

	status := registry.StatusError
	category := ""
	if res.Error != nil {
		category = res.Error.Code
		if category == runtime.CodeTimeout {
			status = registry.StatusTimeout
		}
	}
	result.Error = res.Error
	finish(status, category)
	s.recordAudit(ctx, subj, authz.ActionPluginInvoke, target, "failure", category)
	if len(res.StderrTail) > 0 {
		// stderr 只进日志，绝不混入结果（plugin-spec/02）。
		s.logger.Warn("插件 stderr", "plugin_id", pluginID, "capability_id", capabilityID,
			"invocation_id", invocationID, "stderr", truncate(res.StderrTail, 4096))
	}
	return result, nil
}

// ListExecutions 查询执行记录。
func (s *Service) ListExecutions(ctx context.Context, subj authz.Subject, f registry.ExecutionFilter) ([]registry.Execution, error) {
	if d := s.authorizer.Authorize(ctx, subj, authz.ActionExecutionRead, authz.Target{Type: "execution"}); !d.Allowed {
		s.recordAudit(ctx, subj, authz.ActionExecutionRead, authz.Target{Type: "execution"}, "denied", d.Reason)
		return nil, &Error{Code: runtime.CodePermissionDenied, Message: "无执行记录查看权限", Details: map[string]any{"reason": d.Reason}}
	}
	return s.registry.ListExecutions(ctx, f)
}

// AuthorizeOverview 统一校验总览权限（overview.read），供 M5 总览接口复用。
func (s *Service) AuthorizeOverview(ctx context.Context, subj authz.Subject) error {
	if d := s.authorizer.Authorize(ctx, subj, authz.ActionOverviewRead, authz.Target{Type: "overview"}); !d.Allowed {
		s.recordAudit(ctx, subj, authz.ActionOverviewRead, authz.Target{Type: "overview"}, "denied", d.Reason)
		return &Error{Code: runtime.CodePermissionDenied, Message: "无总览查看权限", Details: map[string]any{"reason": d.Reason}}
	}
	return nil
}

// CountExecutions 统计时间窗口内的执行总数与失败数（总览 KPI，OQ-043）。
func (s *Service) CountExecutions(ctx context.Context, from, to time.Time) (total, failed int, err error) {
	return s.registry.CountExecutions(ctx, from, to)
}

func (s *Service) enqueueExecution(e registry.Execution) {
	if s.queue == nil {
		s.logger.Warn("执行记录未入队：队列未装配", "invocation_id", e.InvocationID)
		return
	}
	payload, err := e.EnqueuePayload()
	if err != nil {
		s.logger.Error("序列化执行记录失败", "invocation_id", e.InvocationID, "error", err)
		return
	}
	// 使用独立上下文：即使请求被取消，已完成的调用也必须留下记录。
	if err := s.queue.Enqueue(context.Background(), store.Record{
		Kind:    store.RecordKindPluginExecution,
		Payload: payload,
	}); err != nil {
		s.logger.Error("执行记录入队失败", "invocation_id", e.InvocationID, "error", err)
	}
}

// RecordExecution 供 M4 调度器写入 missed/skipped 等非调用产生的执行记录。
func (s *Service) RecordExecution(e registry.Execution) {
	s.enqueueExecution(e)
}

func (s *Service) serialMutex(capabilityID string) *sync.Mutex {
	s.serialMu.Lock()
	defer s.serialMu.Unlock()
	mu, ok := s.serialLock[capabilityID]
	if !ok {
		mu = &sync.Mutex{}
		s.serialLock[capabilityID] = mu
	}
	return mu
}

func (s *Service) recordAudit(ctx context.Context, subj authz.Subject, action authz.Action, target authz.Target, result, reason string) {
	if s.audit == nil {
		return
	}
	s.audit(ctx, subj, action, target, result, reason)
}

func toPlugin(row registry.Plugin) Plugin {
	p := Plugin{
		ID:             row.ID,
		Version:        row.Version,
		Form:           row.Form,
		State:          row.State,
		ContractOK:     row.ContractOK,
		ContractReason: row.ContractReason,
		InstallPath:    row.InstallPath,
		UpdatedAt:      row.UpdatedAt,
		Capabilities:   []string{},
	}
	var m manifest.Manifest
	if err := yaml.Unmarshal([]byte(row.ManifestYAML), &m); err == nil {
		for _, c := range m.Capabilities {
			p.Capabilities = append(p.Capabilities, c.ID)
		}
	}
	return p
}

func newInvocationID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand 失败属于不可恢复的环境故障；用时间戳兜底保证记录可关联。
		return fmt.Sprintf("inv-%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
}

func truncate(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	return s[:limit] + "…(已截断)"
}

// summaryLimit 是执行记录中单侧摘要的上限（OQ-046 实现基线）。
const summaryLimit = 2048

// summarizeJSON 生成可展示的输入/输出摘要：空值与 null 返回空串，
// 超限截断并显式标注，避免界面把截断内容误读成完整结果。
func summarizeJSON(raw []byte) string {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		return ""
	}
	return truncate(trimmed, summaryLimit)
}
