// Package manifest 解析并校验插件清单 plugin.yaml。
//
// 字段集与必需性与 plugin-spec/01-manifest.md 一一对应，schema 的唯一权威副本是
// plugin-spec/manifest.schema.json；本包内 schema.json 与之逐字节一致，由 embed 加载。
// 禁止在本包新增未在契约中出现的字段：未定项必须回到 docs/07 登记。
package manifest

// FileName 是契约固定的清单文件名（plugin-spec/01 §1）。
const FileName = "plugin.yaml"

// MaxFileSize 限制清单读取体积，防止误读大文件；该值属实现基线（OQ-039）。
const MaxFileSize = 1 << 20

// Manifest 对应清单字段（契约 1.2；基础 25 字段 + 运行时监听/配置项/风险等级增量）。
type Manifest struct {
	Identity     Identity      `yaml:"identity" json:"identity"`
	Version      VersionBlock  `yaml:"version" json:"version"`
	Form         string        `yaml:"form" json:"form"`
	Entry        Entry         `yaml:"entry" json:"entry"`
	Capabilities []Capability  `yaml:"capabilities" json:"capabilities"`
	Permissions  []Permission  `yaml:"permissions,omitempty" json:"permissions,omitempty"`
	Runtime      *Runtime      `yaml:"runtime,omitempty" json:"runtime,omitempty"`
	Config       []ConfigField `yaml:"config,omitempty" json:"config,omitempty"`
	Metadata     *Metadata     `yaml:"metadata,omitempty" json:"metadata,omitempty"`

	// 以下字段由加载器填充，不属于清单文本。
	Root     string   `yaml:"-" json:"-"`
	Raw      []byte   `yaml:"-" json:"-"`
	SHA256   string   `yaml:"-" json:"-"`
	Warnings []string `yaml:"-" json:"-"`
}

type Identity struct {
	ID          string `yaml:"id" json:"id"`
	DisplayName string `yaml:"display_name" json:"display_name"`
	Description string `yaml:"description" json:"description"`
}

type VersionBlock struct {
	Plugin   string `yaml:"plugin" json:"plugin"`
	Contract string `yaml:"contract" json:"contract"`
}

type Entry struct {
	Executable string `yaml:"executable,omitempty" json:"executable,omitempty"`
	Launch     string `yaml:"launch,omitempty" json:"launch,omitempty"`
	Socket     string `yaml:"socket,omitempty" json:"socket,omitempty"`
}

type Capability struct {
	ID                 string `yaml:"id" json:"id"`
	Description        string `yaml:"description" json:"description"`
	InputSchemaRef     string `yaml:"input_schema_ref" json:"input_schema_ref"`
	OutputSchemaRef    string `yaml:"output_schema_ref" json:"output_schema_ref"`
	Idempotent         bool   `yaml:"idempotent" json:"idempotent"`
	LongRunning        bool   `yaml:"long_running" json:"long_running"`
	SuggestedTimeoutMS int    `yaml:"suggested_timeout_ms,omitempty" json:"suggested_timeout_ms,omitempty"`
	SupportsCancel     bool   `yaml:"supports_cancel" json:"supports_cancel"`
	Concurrency        string `yaml:"concurrency" json:"concurrency"`
	Sensitive          bool   `yaml:"sensitive" json:"sensitive"`
	// RiskLevel 是契约 1.2 增量：normal | destructive；destructive 必须显式声明。
	RiskLevel string `yaml:"risk_level,omitempty" json:"risk_level,omitempty"`
}

type Permission struct {
	Category string   `yaml:"category" json:"category"`
	Actions  []string `yaml:"actions" json:"actions"`
	Scope    string   `yaml:"scope,omitempty" json:"scope,omitempty"`
}

type Runtime struct {
	MaxResultBytes      int64  `yaml:"max_result_bytes,omitempty" json:"max_result_bytes,omitempty"`
	ResourceExpectation string `yaml:"resource_expectation,omitempty" json:"resource_expectation,omitempty"`
	// Listen 仅 daemon 形态使用（契约 1.2）：声明远端监听端口默认值；不绑定证书。
	Listen *Listen `yaml:"listen,omitempty" json:"listen,omitempty"`
}

// Listen 是远端 daemon 的监听声明；证书不绑定端口（ADR-0022）。
type Listen struct {
	Port int `yaml:"port,omitempty" json:"port,omitempty"`
}

// ConfigField 是契约 1.2 的实例配置项声明（plugin-spec/11）。
type ConfigField struct {
	Key         string   `yaml:"key" json:"key"`
	Type        string   `yaml:"type" json:"type"`
	Required    bool     `yaml:"required,omitempty" json:"required,omitempty"`
	Secret      bool     `yaml:"secret,omitempty" json:"secret,omitempty"`
	Default     any      `yaml:"default,omitempty" json:"default,omitempty"`
	Enum        []string `yaml:"enum,omitempty" json:"enum,omitempty"`
	Description string   `yaml:"description,omitempty" json:"description,omitempty"`
}

type Metadata struct {
	Author   string `yaml:"author,omitempty" json:"author,omitempty"`
	License  string `yaml:"license,omitempty" json:"license,omitempty"`
	Homepage string `yaml:"homepage,omitempty" json:"homepage,omitempty"`
	Docs     string `yaml:"docs,omitempty" json:"docs,omitempty"`
}

// Capability 按能力标识查找。
func (m *Manifest) Capability(id string) (Capability, bool) {
	for _, c := range m.Capabilities {
		if c.ID == id {
			return c, true
		}
	}
	return Capability{}, false
}
