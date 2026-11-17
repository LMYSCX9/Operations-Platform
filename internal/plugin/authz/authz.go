// Package authz 定义调用网关的授权判定契约（ADR-0006）。
//
// M1 只提供固定管理员主体，用于跑通调用链路；M2 由 identity 包提供基于
// 会话与角色的实现，接口保持不变。默认拒绝：未命中任何规则即拒绝。
package authz

import "context"

// Subject 是发起操作的主体。
type Subject struct {
	ID    string   `json:"subject"`
	Roles []string `json:"roles,omitempty"`
}

// Action 是可授权的动作名，属于实现基线（OQ-039/OQ-040）。
type Action string

const (
	ActionPluginRead     Action = "plugin.read"
	ActionPluginEnable   Action = "plugin.enable"
	ActionPluginDisable  Action = "plugin.disable"
	ActionPluginInvoke   Action = "plugin.invoke"
	ActionExecutionRead  Action = "execution.read"
	ActionAuditRead      Action = "audit.read"
	ActionHostRead       Action = "host.read"
	ActionHostWrite      Action = "host.write"
	ActionHostTest       Action = "host.test"
	ActionHostDistribute Action = "host.distribute"
	ActionTaskRead       Action = "task.read"
	ActionTaskWrite      Action = "task.write"
	ActionTaskRun        Action = "task.run"
	ActionOverviewRead   Action = "overview.read"

	// M6 实例级动作（DOC-17 §8）：目标类型为 plugin_instance / plugin_event。
	ActionPluginInstanceProbe  Action = "plugin.instance.probe"
	ActionPluginInstanceConfig Action = "plugin.instance.config"
	ActionPluginEventRead      Action = "plugin.event.read"
)

// Target 是被操作对象；Type 用于区分插件、主机、任务等。
type Target struct {
	Type string `json:"type"`
	ID   string `json:"id"`
}

// Decision 是授权结果。Reason 只用于日志与审计，不直接回给最终用户。
type Decision struct {
	Allowed bool
	Reason  string
}

// Authorizer 是授权判定的唯一入口。
type Authorizer interface {
	Authorize(ctx context.Context, subj Subject, action Action, target Target) Decision
}

// StaticAdmin 授予固定管理员主体全部权限，供 M1 使用。
type StaticAdmin struct{}

// BootstrapSubject 是 M1 的内置管理员主体。
var BootstrapSubject = Subject{ID: "user:admin", Roles: []string{"admin"}}

func (StaticAdmin) Authorize(_ context.Context, subj Subject, _ Action, _ Target) Decision {
	for _, r := range subj.Roles {
		if r == "admin" {
			return Decision{Allowed: true}
		}
	}
	return Decision{Allowed: false, Reason: "no matching rule"}
}
