package identity

import (
	"context"

	"operations-platform/internal/plugin/authz"
)

// rolePermissions 是 v1 角色矩阵（实现基线，OQ-040）。
//
// 原则：
//   - 默认拒绝：未列出的动作一律拒绝；
//   - 只读角色不得产生副作用；
//   - 变更底座运行时行为（启停插件）与查看审计属于管理员保留动作。
var rolePermissions = map[string]map[authz.Action]bool{
	RoleAdmin: {
		authz.ActionPluginRead:     true,
		authz.ActionPluginEnable:   true,
		authz.ActionPluginDisable:  true,
		authz.ActionPluginInvoke:   true,
		authz.ActionExecutionRead:  true,
		authz.ActionAuditRead:      true,
		authz.ActionHostRead:       true,
		authz.ActionHostWrite:      true,
		authz.ActionHostTest:       true,
		authz.ActionHostDistribute: true,
		authz.ActionTaskRead:       true,
		authz.ActionTaskWrite:      true,
		authz.ActionTaskRun:        true,
		authz.ActionOverviewRead:   true,

		authz.ActionPluginInstanceProbe:  true,
		authz.ActionPluginInstanceConfig: true,
		authz.ActionPluginEventRead:      true,
	},
	RoleOperator: {
		authz.ActionPluginRead:     true,
		authz.ActionPluginInvoke:   true,
		authz.ActionExecutionRead:  true,
		authz.ActionHostRead:       true,
		authz.ActionHostWrite:      true,
		authz.ActionHostTest:       true,
		authz.ActionHostDistribute: true,
		authz.ActionTaskRead:       true,
		authz.ActionTaskWrite:      true,
		authz.ActionTaskRun:        true,
		authz.ActionOverviewRead:   true,

		authz.ActionPluginInstanceProbe:  true,
		authz.ActionPluginInstanceConfig: true,
		authz.ActionPluginEventRead:      true,
	},
	RoleViewer: {
		authz.ActionPluginRead:      true,
		authz.ActionExecutionRead:   true,
		authz.ActionHostRead:        true,
		authz.ActionTaskRead:        true,
		authz.ActionOverviewRead:    true,
		authz.ActionPluginEventRead: true,
	},
}

// RoleAuthorizer 按角色判定动作；任一角色允许即放行。
type RoleAuthorizer struct{}

func (RoleAuthorizer) Authorize(_ context.Context, subj authz.Subject, action authz.Action, _ authz.Target) authz.Decision {
	for _, role := range subj.Roles {
		if perms, ok := rolePermissions[role]; ok && perms[action] {
			return authz.Decision{Allowed: true}
		}
	}
	return authz.Decision{Allowed: false, Reason: "角色不包含动作 " + string(action)}
}

// RoleOf 返回账号的主角色（v1 一账号一角色）。
func RoleOf(u User) string { return u.Role }

// SubjectOf 把账号映射为授权主体。
func SubjectOf(u User) authz.Subject {
	return authz.Subject{ID: "user:" + u.ID, Roles: []string{u.Role}}
}
