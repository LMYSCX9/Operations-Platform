// Package instances 维护插件类型、实例、实例配置、探测记录、证书绑定与注册事件（DOC-17、ADR-0023）。
//
// 事实来源是底座掌握的包、清单、下发记录与证书绑定；插件自报只作参考。
// 类型与实例分离：类型是包与清单的抽象，实例绑定到「本机」或「某主机」。
package instances

import "time"

// 托管方式。
const (
	CustodyLocal  = "local"
	CustodyRemote = "remote"
)

// 实例状态（三态 + 失败/漂移/未知）。
const (
	StateInstalled   = "installed"
	StateStarted     = "started"
	StateCallable    = "callable"
	StateStartFailed = "start_failed"
	StateStopFailed  = "stop_failed"
	StateDrifted     = "drifted"
	StateUnknown     = "unknown"
	StateDisabled    = "disabled"
	// StateRevoked 表示已撤销下发且证书已吊销（DOC-09 §6.5），保留静态事实与审计。
	StateRevoked = "revoked"
)

// 探测结果。
const (
	ProbeOK      = "ok"
	ProbeFailed  = "failed"
	ProbeUnknown = "unknown"
)

// 漂移原因。
const (
	DriftNone            = ""
	DriftPackageMissing  = "package_missing"
	DriftVersionMismatch = "version_mismatch"
	DriftCertMismatch    = "cert_mismatch"
	DriftUnitMissing     = "unit_missing"
)

// Type 是插件类型（以 plugin_id 为键）。
type Type struct {
	TypeID          string    `json:"type_id"`
	DisplayName     string    `json:"display_name"`
	Description     string    `json:"description,omitempty"`
	Form            string    `json:"form"`
	LatestVersion   string    `json:"latest_version"`
	Custody         string    `json:"custody"`
	ManifestYAML    string    `json:"-"`
	ManifestSHA256  string    `json:"manifest_sha256"`
	ContractVersion string    `json:"contract_version"`
	ContractRange   string    `json:"contract_range"`
	RuntimePort     int       `json:"runtime_port,omitempty"`
	ConfigSchema    string    `json:"-"` // JSON 数组，接口层解析后输出
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}

// Capability 是类型级能力声明（以类型为键独立存储）。
type Capability struct {
	TypeID             string `json:"type_id"`
	CapabilityID       string `json:"capability_id"`
	Description        string `json:"description,omitempty"`
	InputSchemaRef     string `json:"input_schema_ref,omitempty"`
	OutputSchemaRef    string `json:"output_schema_ref,omitempty"`
	Idempotent         bool   `json:"idempotent"`
	LongRunning        bool   `json:"long_running"`
	SupportsCancel     bool   `json:"supports_cancel"`
	Concurrency        string `json:"concurrency"`
	Sensitive          bool   `json:"sensitive"`
	RiskLevel          string `json:"risk_level"`
	SuggestedTimeoutMS int64  `json:"suggested_timeout_ms,omitempty"`
}

// Instance 是「类型 × 位置」的具体实例。
type Instance struct {
	InstanceID      string    `json:"instance_id"`
	TypeID          string    `json:"type_id"`
	HostID          string    `json:"host_id,omitempty"`
	Custody         string    `json:"custody"`
	PackageVersion  string    `json:"package_version"`
	ListenPort      int       `json:"listen_port,omitempty"`
	Enabled         bool      `json:"enabled"`
	Installed       bool      `json:"installed"`
	Started         bool      `json:"started"`
	Callable        bool      `json:"callable"`
	State           string    `json:"state"`
	StateReason     string    `json:"state_reason,omitempty"`
	Drift           string    `json:"drift,omitempty"`
	CertSerial      string    `json:"cert_serial,omitempty"`
	CertFingerprint string    `json:"cert_fingerprint,omitempty"`
	CertNotAfter    time.Time `json:"cert_not_after,omitempty"`
	LastProbeAt     time.Time `json:"last_probe_at,omitempty"`
	LastProbeResult string    `json:"last_probe_result,omitempty"`
	LastProbeReason string    `json:"last_probe_reason,omitempty"`
	LastError       string    `json:"last_error,omitempty"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}

// InstanceFilter 是实例查询条件；零值表示不过滤。
type InstanceFilter struct {
	TypeID  string
	HostID  string
	Custody string
	State   string
}

// Probe 是一条探测记录。
type Probe struct {
	ID         int64     `json:"id"`
	InstanceID string    `json:"instance_id"`
	ProbeAt    time.Time `json:"probe_at"`
	Stage      string    `json:"stage,omitempty"`
	OK         bool      `json:"ok"`
	LatencyMS  int64     `json:"latency_ms"`
	Detail     string    `json:"detail,omitempty"`
}

// ConfigEntry 是实例配置的密文行；接口永不返回 Ciphertext。
type ConfigEntry struct {
	InstanceID string
	Key        string
	Ciphertext []byte
	UpdatedAt  time.Time
	UpdatedBy  string
}

// RegistrationEvent 是注册/状态变化的审计轨迹（DOC-17 §8）。
type RegistrationEvent struct {
	ID         int64     `json:"id"`
	OccurredAt time.Time `json:"occurred_at"`
	Action     string    `json:"action"`
	TypeID     string    `json:"type_id,omitempty"`
	InstanceID string    `json:"instance_id,omitempty"`
	HostID     string    `json:"host_id,omitempty"`
	Actor      string    `json:"actor,omitempty"`
	Result     string    `json:"result"`
	Reason     string    `json:"reason,omitempty"`
	Details    string    `json:"details,omitempty"`
}

// LocalInstanceID 返回本机实例的稳定标识。
func LocalInstanceID(typeID string) string { return "local:" + typeID }

// RemoteInstanceID 返回远端实例的稳定标识。
func RemoteInstanceID(hostID, typeID string) string { return "remote:" + hostID + ":" + typeID }
