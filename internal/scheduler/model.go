// Package scheduler 实现 M4 定时任务：cron 解析、持久化、认领调度与执行记录。
//
// 设计约束（DOC-10、ADR-0015）：
//   - 进程内调度，单节点；任务先持久化、先认领再执行；
//   - 同一任务默认不并发（overlap=skip）；错过执行不补跑，但写一条 missed 记录；
//   - 只调度本机插件能力，不执行任意 shell，不接受凭据类参数。
package scheduler

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// 触发类型。
const (
	TriggerCron     = "cron"
	TriggerInterval = "interval"
)

// 任务状态。
const (
	StateEnabled     = "enabled"
	StatePaused      = "paused"
	StateUnavailable = "unavailable"
)

// 重叠策略；v1 固定 skip（OQ-042）。
const OverlapSkip = "skip"

// 错误码。
const (
	CodeInputInvalid     = "input_invalid"
	CodePermissionDenied = "permission_denied"
	CodeNotFound         = "not_found"
	CodeConflict         = "conflict"
	CodeDependency       = "dependency_unavailable"
)

// Error 是调度服务的结构化错误。
type Error struct {
	Code    string         `json:"code"`
	Message string         `json:"message"`
	Details map[string]any `json:"details,omitempty"`
}

func (e *Error) Error() string { return e.Code + ": " + e.Message }

// ErrNotFound 表示任务不存在。
var ErrNotFound = errors.New("任务不存在")

// Task 是对外可见的任务定义。
type Task struct {
	ID          string `json:"task_id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	// InstanceID 是 M6 起的执行目标；PluginID 保留为该实例类型的别名（兼容旧任务）。
	InstanceID      string          `json:"instance_id,omitempty"`
	PluginID        string          `json:"plugin_id"`
	CapabilityID    string          `json:"capability_id"`
	Input           json.RawMessage `json:"input"`
	TriggerType     string          `json:"trigger_type"`
	CronExpr        string          `json:"cron_expr,omitempty"`
	IntervalSeconds int             `json:"interval_seconds,omitempty"`
	Timezone        string          `json:"timezone"`
	TimeoutSeconds  int             `json:"timeout_seconds"`
	Retries         int             `json:"retries"`
	OverlapPolicy   string          `json:"overlap_policy"`
	State           string          `json:"state"`
	NextRunAt       *time.Time      `json:"next_run_at,omitempty"`
	LastRunAt       *time.Time      `json:"last_run_at,omitempty"`
	CreatedBy       string          `json:"created_by"`
	CreatedAt       time.Time       `json:"created_at"`
	UpdatedAt       time.Time       `json:"updated_at"`
}

// TaskInput 是新建/编辑请求体；未填的默认值由 Service 补齐。
type TaskInput struct {
	Name            string          `json:"name"`
	Description     string          `json:"description"`
	InstanceID      string          `json:"instance_id"`
	PluginID        string          `json:"plugin_id"`
	CapabilityID    string          `json:"capability_id"`
	Input           json.RawMessage `json:"input"`
	TriggerType     string          `json:"trigger_type"`
	CronExpr        string          `json:"cron_expr"`
	IntervalSeconds int             `json:"interval_seconds"`
	Timezone        string          `json:"timezone"`
	TimeoutSeconds  int             `json:"timeout_seconds"`
	Retries         *int            `json:"retries"`
	State           string          `json:"state"`
}

// Validate 做与默认值无关的语法校验；数值默认值在 Service 中补齐。
func (in TaskInput) Validate() error {
	if strings.TrimSpace(in.Name) == "" {
		return &Error{Code: CodeInputInvalid, Message: "任务名称不能为空", Details: map[string]any{"field": "name"}}
	}
	if strings.TrimSpace(in.InstanceID) == "" && strings.TrimSpace(in.PluginID) == "" {
		return &Error{Code: CodeInputInvalid, Message: "instance_id 不能为空（plugin_id 仅作本机别名兼容）",
			Details: map[string]any{"field": "instance_id"}}
	}
	if strings.TrimSpace(in.CapabilityID) == "" {
		return &Error{Code: CodeInputInvalid, Message: "capability_id 不能为空", Details: map[string]any{"field": "capability_id"}}
	}
	switch in.TriggerType {
	case TriggerCron:
		if strings.TrimSpace(in.CronExpr) == "" {
			return &Error{Code: CodeInputInvalid, Message: "cron 触发必须提供 cron_expr", Details: map[string]any{"field": "cron_expr"}}
		}
		if _, err := ParseCron(in.CronExpr); err != nil {
			return &Error{Code: CodeInputInvalid, Message: "cron 表达式非法: " + err.Error(), Details: map[string]any{"field": "cron_expr"}}
		}
	case TriggerInterval:
		if in.IntervalSeconds <= 0 {
			return &Error{Code: CodeInputInvalid, Message: "固定间隔触发必须提供 interval_seconds", Details: map[string]any{"field": "interval_seconds"}}
		}
	default:
		return &Error{Code: CodeInputInvalid, Message: "trigger_type 只能是 cron 或 interval", Details: map[string]any{"field": "trigger_type"}}
	}
	if in.Timezone == "" {
		return &Error{Code: CodeInputInvalid, Message: "时区不能为空（如 Asia/Shanghai）", Details: map[string]any{"field": "timezone"}}
	}
	if _, err := time.LoadLocation(in.Timezone); err != nil {
		return &Error{Code: CodeInputInvalid, Message: "时区无效: " + in.Timezone, Details: map[string]any{"field": "timezone"}}
	}
	if len(in.Input) > 0 {
		var v any
		if err := json.Unmarshal(in.Input, &v); err != nil {
			return &Error{Code: CodeInputInvalid, Message: "input 必须是合法 JSON", Details: map[string]any{"field": "input"}}
		}
		if key, found := findCredentialKey(v); found {
			return &Error{
				Code:    CodeInputInvalid,
				Message: "任务参数禁止包含凭据字段（" + key + "）；凭据只能存在于主机记录中",
				Details: map[string]any{"field": "input", "credential_key": key},
			}
		}
	}
	if in.TimeoutSeconds < 0 {
		return &Error{Code: CodeInputInvalid, Message: "timeout_seconds 不能为负", Details: map[string]any{"field": "timeout_seconds"}}
	}
	if in.Retries != nil && *in.Retries < 0 {
		return &Error{Code: CodeInputInvalid, Message: "retries 不能为负", Details: map[string]any{"field": "retries"}}
	}
	if in.State != "" && in.State != StateEnabled && in.State != StatePaused {
		return &Error{Code: CodeInputInvalid, Message: "state 只能是 enabled 或 paused", Details: map[string]any{"field": "state"}}
	}
	return nil
}

// credentialKeys 是被禁止出现在任务参数中的键名（递归匹配）。
var credentialKeys = map[string]bool{
	"password": true, "passwd": true, "secret": true, "token": true,
	"credential": true, "credentials": true, "private_key": true,
	"privatekey": true, "passphrase": true, "access_key": true, "api_key": true,
}

func findCredentialKey(v any) (string, bool) {
	switch val := v.(type) {
	case map[string]any:
		for k, child := range val {
			if credentialKeys[strings.ToLower(k)] {
				return k, true
			}
			if found, ok := findCredentialKey(child); ok {
				return found, true
			}
		}
	case []any:
		for _, child := range val {
			if found, ok := findCredentialKey(child); ok {
				return found, true
			}
		}
	}
	return "", false
}

// NextRun 计算下一次触发时间。
func NextRun(t Task, from time.Time) (time.Time, error) {
	loc := time.UTC
	if t.Timezone != "" {
		l, err := time.LoadLocation(t.Timezone)
		if err != nil {
			return time.Time{}, fmt.Errorf("任务 %s 时区无效: %w", t.ID, err)
		}
		loc = l
	}
	if t.TriggerType == TriggerInterval {
		if t.IntervalSeconds <= 0 {
			return time.Time{}, fmt.Errorf("任务 %s 的间隔必须大于 0", t.ID)
		}
		return from.Add(time.Duration(t.IntervalSeconds) * time.Second), nil
	}
	sched, err := ParseCron(t.CronExpr)
	if err != nil {
		return time.Time{}, fmt.Errorf("任务 %s 的 cron 非法: %w", t.ID, err)
	}
	return sched.Next(from, loc)
}
