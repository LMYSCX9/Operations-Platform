package identity

import "context"

// ExternalProvider 是外部身份源（LDAP / 单点登录）的预留接口（OQ-037）。
//
// v1 **不实现、不装配、不暴露配置项**：仅用于固定边界，避免后续接入时改动
// 会话与授权模型。实现方必须返回本地已存在的账号映射，不得绕过角色矩阵。
type ExternalProvider interface {
	// Name 返回身份源名称，用于审计与日志。
	Name() string
	// Authenticate 校验外部凭证并返回本地账号；失败必须返回可审计的原因。
	Authenticate(ctx context.Context, credentials map[string]string) (User, error)
}
