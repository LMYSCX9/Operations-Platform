// Package host 实现主机清单、加密凭据、连通性测试与插件下发安装（M3/M6）。
//
// 边界（DOC-09 §6）：远端 daemon 的下发终点是「产物落地 + systemd 启动 + 探测通过」；
// 三态（已安装/已启动/可调用）分别记录，命令式插件仍只下发不启动。
package host

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// 主机认证方式。
const (
	AuthMethodKey      = "key"
	AuthMethodPassword = "password"
)

// 主机运行状态（只反映最近一次探测事实）。
const (
	StatusUnknown             = "unknown"
	StatusReachable           = "reachable"
	StatusUnreachable         = "unreachable"
	StatusFingerprintMismatch = "fingerprint_mismatch"
)

// 下发状态机（DOC-09 §6）。
const (
	DistQueued      = "queued"
	DistConnecting  = "connecting"
	DistUploading   = "uploading"
	DistVerifying   = "verifying"
	DistInstalling  = "installing"
	DistInstalled   = "installed"
	DistStarting    = "starting"
	DistStarted     = "started"
	DistStartFailed = "start_failed"
	DistCallable    = "callable"
	DistFailed      = "failed"
	DistInterrupted = "interrupted"
	DistCancelled   = "cancelled"
)

// 下发错误类别（DOC-09 DIST-F-05）。
const (
	CatUnreachable         = "unreachable"
	CatAuthFailed          = "auth_failed"
	CatFingerprintMismatch = "fingerprint_mismatch"
	CatUnsupportedOS       = "unsupported_os"
	CatTimeout             = "timeout"
	CatArchiveFailed       = "archive_failed"
	CatUploadFailed        = "upload_failed"
	CatVerifyFailed        = "verify_failed"
	CatInstallFailed       = "install_failed"
	CatCancelled           = "cancelled"
	CatInterrupted         = "interrupted"
)

// 主机相关错误码；HTTP 层按类别映射状态码。
const (
	CodeInputInvalid          = "input_invalid"
	CodePermissionDenied      = "permission_denied"
	CodeNotFound              = "not_found"
	CodeConflict              = "conflict"
	CodeCredentialKeyMissing  = "credential_key_missing"
	CodeDependencyUnavailable = "dependency_unavailable"
	CodeTimeout               = "timeout"
	CodeFingerprintMismatch   = "fingerprint_mismatch"
	CodeUnsupportedOS         = "unsupported_os"
	CodeAuthFailed            = "auth_failed"
	CodeUnreachable           = "unreachable"
	CodeVerifyFailed          = "verify_failed"
	CodeInstallFailed         = "install_failed"
	CodeArchiveFailed         = "archive_failed"
	CodePKIFailed             = "pki_failed"
)

// Error 是主机服务的结构化错误。
type Error struct {
	Code    string         `json:"code"`
	Message string         `json:"message"`
	Details map[string]any `json:"details,omitempty"`
}

func (e *Error) Error() string { return e.Code + ": " + e.Message }

// ErrNotFound 表示主机或下发记录不存在。
var ErrNotFound = errors.New("记录不存在")

// Host 是对外可见的主机记录；不含任何凭据字段。
type Host struct {
	ID                   string     `json:"host_id"`
	Name                 string     `json:"name"`
	Address              string     `json:"address"`
	Port                 int        `json:"port"`
	Username             string     `json:"username"`
	AuthMethod           string     `json:"auth_method"`
	HostKeyFP            string     `json:"host_key_fingerprint,omitempty"`
	HostKeyTrusted       bool       `json:"host_key_trusted"`
	Status               string     `json:"status"`
	LastTestAt           *time.Time `json:"last_test_at,omitempty"`
	LastTestError        string     `json:"last_test_error,omitempty"`
	CredentialConfigured bool       `json:"credential_configured"`
	CreatedBy            string     `json:"created_by"`
	CreatedAt            time.Time  `json:"created_at"`
	UpdatedAt            time.Time  `json:"updated_at"`
}

// Credential 是解密后的凭据明文结构，只在建立连接前短暂存在。
// 该结构绝不序列化到任何接口响应、日志或审计。
type Credential struct {
	Method     string `json:"method"`
	PrivateKey string `json:"private_key,omitempty"`
	Passphrase string `json:"passphrase,omitempty"`
	Password   string `json:"password,omitempty"`
}

// HostInput 是新增/编辑主机的请求体。
// Credential 为 nil 表示不替换已有凭据（编辑场景）；新增时必须提供。
type HostInput struct {
	Name       string      `json:"name"`
	Address    string      `json:"address"`
	Port       int         `json:"port"`
	Username   string      `json:"username"`
	AuthMethod string      `json:"auth_method"`
	Credential *Credential `json:"credential,omitempty"`
}

// Validate 检查主机输入的基础约束；端口无默认值，必须显式给出（DOC-09 §2）。
func (in HostInput) Validate() error {
	details := map[string]any{}
	if strings.TrimSpace(in.Name) == "" {
		details["field"] = "name"
		return &Error{Code: CodeInputInvalid, Message: "主机名称不能为空", Details: details}
	}
	addr := strings.TrimSpace(in.Address)
	if addr == "" {
		return &Error{Code: CodeInputInvalid, Message: "主机地址不能为空", Details: map[string]any{"field": "address"}}
	}
	if strings.Contains(addr, "://") {
		return &Error{Code: CodeInputInvalid, Message: "地址不得包含协议前缀", Details: map[string]any{"field": "address"}}
	}
	if in.Port < 1 || in.Port > 65535 {
		return &Error{Code: CodeInputInvalid, Message: "端口必须在 1~65535 之间且无默认值", Details: map[string]any{"field": "port"}}
	}
	if strings.TrimSpace(in.Username) == "" {
		return &Error{Code: CodeInputInvalid, Message: "登录用户名不能为空", Details: map[string]any{"field": "username"}}
	}
	if in.AuthMethod != AuthMethodKey && in.AuthMethod != AuthMethodPassword {
		return &Error{Code: CodeInputInvalid, Message: "认证方式只能是 key 或 password", Details: map[string]any{"field": "auth_method"}}
	}
	if in.Credential != nil {
		if in.Credential.Method != "" && in.Credential.Method != in.AuthMethod {
			return &Error{Code: CodeInputInvalid, Message: "凭据类型与认证方式不一致", Details: map[string]any{"field": "credential.method"}}
		}
		if in.AuthMethod == AuthMethodKey && strings.TrimSpace(in.Credential.PrivateKey) == "" {
			return &Error{Code: CodeInputInvalid, Message: "SSH 密钥认证必须提供私钥", Details: map[string]any{"field": "credential.private_key"}}
		}
		if in.AuthMethod == AuthMethodPassword && in.Credential.Password == "" {
			return &Error{Code: CodeInputInvalid, Message: "账号密码认证必须提供密码", Details: map[string]any{"field": "credential.password"}}
		}
	}
	return nil
}

// TestResult 是一次连通性测试的结果。
type TestResult struct {
	HostID   string    `json:"host_id"`
	Status   string    `json:"status"`
	OS       string    `json:"os,omitempty"`
	Error    string    `json:"error,omitempty"`
	Category string    `json:"error_category,omitempty"`
	TestedAt time.Time `json:"tested_at"`
}

// Distribution 是一次插件下发任务。
type Distribution struct {
	ID             string     `json:"distribution_id"`
	HostID         string     `json:"host_id"`
	PluginID       string     `json:"plugin_id"`
	PluginVersion  string     `json:"plugin_version"`
	State          string     `json:"state"`
	Stage          string     `json:"stage"`
	ErrorCategory  string     `json:"error_category,omitempty"`
	ErrorMessage   string     `json:"error_message,omitempty"`
	RemotePath     string     `json:"remote_path,omitempty"`
	IdempotencyKey string     `json:"idempotency_key"`
	CreatedBy      string     `json:"created_by"`
	CreatedAt      time.Time  `json:"created_at"`
	StartedAt      *time.Time `json:"started_at,omitempty"`
	FinishedAt     *time.Time `json:"finished_at,omitempty"`
	// 三态独立（DIST-05）：Installed 以产物落地为准，StartedOK 以 unit active 为准，
	// Callable 以探测（TCP + mTLS + Ready + GetVersion）为准，不得互相推断。
	Installed  bool   `json:"installed"`
	StartedOK  bool   `json:"started_ok"`
	Callable   bool   `json:"callable"`
	InstanceID string `json:"instance_id,omitempty"`
	CertSerial string `json:"cert_serial,omitempty"`
	ListenPort int    `json:"listen_port,omitempty"`
	Note       string `json:"note,omitempty"`
}

// DistNote 是下发送接口固定附带的边界说明。M6 起远端 daemon 会启动并探测。
const DistNote = "远端 daemon：产物落地后执行 systemctl enable --now，三态（已安装/已启动/可调用）分别记录；命令式插件仅下发，不启动"

// Deployment 是一次远端 daemon 部署的产物集合，全部由底座生成（DOC-09 §6.1）。
type Deployment struct {
	InstanceID      string
	PluginID        string
	HostID          string
	Port            int
	ContractVersion string
	BaseID          string
	// Executable 是目标机上的入口绝对路径（包目录 + 清单 entry.executable）。
	Executable    string
	ServerCertPEM []byte
	ServerKeyPEM  []byte
	CACertPEM     []byte
	ConfigValues  map[string]any
	// 以下路径由配置注入，属实现基线（OQ-041 / OQ-048）。
	ConfigRoot  string
	DataRoot    string
	UnitDir     string
	ServiceUser string
}

// UnitName 返回 systemd unit 名；包含主机标识避免与其它主机/本机实例冲突（ADR-0018 §2.3）。
func (d Deployment) UnitName() string {
	return fmt.Sprintf("ops-plugin-%s-%s.service", sanitizeSegment(d.PluginID), sanitizeSegment(d.HostID))
}

// ConfigDir 返回按主机生成的状态目录（plugin-spec/03 §3.3，权限 0700）。
func (d Deployment) ConfigDir() string {
	return fmt.Sprintf("%s/%s", strings.TrimRight(d.ConfigRoot, "/"), sanitizeSegment(d.PluginID))
}

// ConfigPath 返回按主机生成的配置文件路径。
func (d Deployment) ConfigPath() string { return d.ConfigDir() + "/config.yaml" }

// TLSDir 返回证书目录。
func (d Deployment) TLSDir() string { return d.ConfigDir() + "/tls" }

// DataDir 返回实例数据目录。
func (d Deployment) DataDir() string {
	return fmt.Sprintf("%s/%s", strings.TrimRight(d.DataRoot, "/"), sanitizeSegment(d.PluginID))
}

// UnitPath 返回 unit 文件绝对路径。
func (d Deployment) UnitPath() string {
	return fmt.Sprintf("%s/%s", strings.TrimRight(d.UnitDir, "/"), d.UnitName())
}

// RemoteDir 返回目标机上的安装目录。
func RemoteDir(root, pluginID, version string) string {
	return fmt.Sprintf("%s/%s/%s", strings.TrimRight(root, "/"), pluginID, version)
}
