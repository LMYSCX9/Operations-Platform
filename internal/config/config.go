// Package config 负责加载、合并与校验底座启动配置。
//
// 配置来源与优先级遵循 ADR-0009：命令行 > 环境变量 > 配置文件 > 内置默认值。
// 配置文件在 M1 起改为 YAML（与插件清单同格式，复用同一解析依赖），原因见 ADR-0009 与 ADR-0016。
// 键名、环境变量名与默认值属于实现基线（docs/12~15，OQ-039~OQ-042），集中在本包内，禁止散落到其他模块。
package config

import (
	"bytes"
	"encoding/base64"
	"errors"
	"flag"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"operations-platform/internal/observability"
)

const (
	EnvDataDir         = "OPS_PLATFORM_DATA_DIR"
	EnvListenAddr      = "OPS_PLATFORM_LISTEN_ADDR"
	EnvLogLevel        = "OPS_PLATFORM_LOG_LEVEL"
	EnvShutdownTimeout = "OPS_PLATFORM_SHUTDOWN_TIMEOUT"
	EnvPluginDir       = "OPS_PLATFORM_PLUGIN_DIR"
	EnvPluginHotReload = "OPS_PLATFORM_PLUGIN_HOT_RELOAD"
	EnvPluginScan      = "OPS_PLATFORM_PLUGIN_SCAN_INTERVAL"
	EnvPluginTimeout   = "OPS_PLATFORM_PLUGIN_INVOKE_TIMEOUT"
	EnvPluginMaxBytes  = "OPS_PLATFORM_PLUGIN_MAX_RESULT_BYTES"
	EnvPluginMaxConc   = "OPS_PLATFORM_PLUGIN_MAX_CONCURRENCY"
	EnvQueueBatch      = "OPS_PLATFORM_EXECUTION_QUEUE_BATCH"
	EnvQueueInterval   = "OPS_PLATFORM_EXECUTION_QUEUE_INTERVAL"
	EnvSessionTTL      = "OPS_PLATFORM_SESSION_TTL"
	EnvLoginAttempts   = "OPS_PLATFORM_LOGIN_MAX_ATTEMPTS"
	EnvLoginLockout    = "OPS_PLATFORM_LOGIN_LOCKOUT"
	EnvAdminPassword   = "OPS_PLATFORM_ADMIN_PASSWORD"
	EnvCredentialKey   = "OPS_PLATFORM_CREDENTIAL_KEY"
	EnvSSHConnect      = "OPS_PLATFORM_SSH_CONNECT_TIMEOUT"
	EnvSSHUpload       = "OPS_PLATFORM_SSH_UPLOAD_TIMEOUT"
	EnvDistMaxConc     = "OPS_PLATFORM_DISTRIBUTION_MAX_CONCURRENCY"
	EnvDistRemoteRoot  = "OPS_PLATFORM_DISTRIBUTION_REMOTE_ROOT"
	EnvTaskScan        = "OPS_PLATFORM_TASK_SCAN_INTERVAL"
	EnvTaskMinInterval = "OPS_PLATFORM_TASK_MIN_INTERVAL"
	EnvTaskTimeout     = "OPS_PLATFORM_TASK_DEFAULT_TIMEOUT"
	EnvTaskRetries     = "OPS_PLATFORM_TASK_DEFAULT_RETRIES"

	EnvPKIBaseID       = "OPS_PLATFORM_PKI_BASE_ID"
	EnvPKICAValidity   = "OPS_PLATFORM_PKI_CA_VALIDITY"
	EnvPKILeafValidity = "OPS_PLATFORM_PKI_LEAF_VALIDITY"
	EnvPluginProbe     = "OPS_PLATFORM_PLUGIN_PROBE_INTERVAL"
	EnvPluginStateTTL  = "OPS_PLATFORM_PLUGIN_STATE_TTL"
	EnvRemoteDial      = "OPS_PLATFORM_REMOTE_DIAL_TIMEOUT"
	EnvRemoteReady     = "OPS_PLATFORM_REMOTE_READY_TIMEOUT"
	EnvDistConfigRoot  = "OPS_PLATFORM_DISTRIBUTION_CONFIG_ROOT"
	EnvDistDataRoot    = "OPS_PLATFORM_DISTRIBUTION_DATA_ROOT"
	EnvDistUnitDir     = "OPS_PLATFORM_DISTRIBUTION_UNIT_DIR"
	EnvDistServiceUser = "OPS_PLATFORM_DISTRIBUTION_SERVICE_USER"
	EnvDistStart       = "OPS_PLATFORM_DISTRIBUTION_START_TIMEOUT"

	DefaultLogLevel        = "info"
	DefaultShutdownTimeout = 10 * time.Second

	DefaultPluginScanInterval   = 30 * time.Second
	DefaultPluginInvokeTimeout  = 30 * time.Second
	DefaultPluginMaxResultBytes = 8 << 20
	DefaultPluginMaxConcurrency = 20
	DefaultQueueBatch           = 50
	DefaultQueueInterval        = 200 * time.Millisecond
	DefaultSessionTTL           = 12 * time.Hour
	DefaultLoginMaxAttempts     = 5
	DefaultLoginLockout         = 5 * time.Minute
	DefaultSSHConnectTimeout    = 10 * time.Second
	DefaultSSHUploadTimeout     = 5 * time.Minute
	DefaultDistMaxConcurrency   = 4
	DefaultDistRemoteRoot       = "/opt/ops-platform/plugins"
	DefaultTaskScanInterval     = 30 * time.Second
	DefaultTaskMinInterval      = time.Minute
	DefaultTaskTimeout          = 5 * time.Minute
	DefaultTaskRetries          = 1

	// M6 基线（OQ-047~OQ-050），全部可配置；改动需要同步 ADR/OQ。
	DefaultPKIBaseID           = "base-01"
	DefaultPKICAValidity       = 10 * 365 * 24 * time.Hour
	DefaultPKILeafValidity     = 365 * 24 * time.Hour
	DefaultPluginProbeInterval = time.Minute
	DefaultPluginStateTTL      = 5 * time.Minute
	DefaultRemoteDialTimeout   = 5 * time.Second
	DefaultRemoteReadyTimeout  = 3 * time.Second
	DefaultDistConfigRoot      = "/etc/ops-platform/plugins"
	DefaultDistDataRoot        = "/var/lib/ops-platform/plugins"
	DefaultDistUnitDir         = "/etc/systemd/system"
	DefaultDistServiceUser     = "root"
	DefaultDistStartTimeout    = 60 * time.Second
)

// Config 是最终生效的配置。
type Config struct {
	ConfigFile string

	DataDir         string
	ListenAddr      string
	LogLevel        string
	ShutdownTimeout time.Duration

	PluginDir            string
	PluginHotReload      bool
	PluginScanInterval   time.Duration
	PluginInvokeTimeout  time.Duration
	PluginMaxResultSize  int64
	PluginMaxConcurrency int

	ExecutionQueueBatch    int
	ExecutionQueueInterval time.Duration

	SessionTTL       time.Duration
	LoginMaxAttempts int
	LoginLockout     time.Duration
	AdminPassword    string
	CredentialKey    []byte

	SSHConnectTimeout          time.Duration
	SSHUploadTimeout           time.Duration
	DistributionMaxConcurrency int
	DistributionRemoteRoot     string

	TaskScanInterval   time.Duration
	TaskMinInterval    time.Duration
	TaskDefaultTimeout time.Duration
	TaskDefaultRetries int

	// M6：内置 CA、远端探测与按主机下发产物。
	PKIBaseID                string
	PKICAValidity            time.Duration
	PKILeafValidity          time.Duration
	PluginProbeInterval      time.Duration
	PluginStateTTL           time.Duration
	RemoteDialTimeout        time.Duration
	RemoteReadyTimeout       time.Duration
	DistributionConfigRoot   string
	DistributionDataRoot     string
	DistributionUnitDir      string
	DistributionServiceUser  string
	DistributionStartTimeout time.Duration
}

// fileConfig 使用指针区分「字段缺失」与「字段显式为空」，否则无法正确实现覆盖优先级。
type fileConfig struct {
	DataDir         *string `yaml:"data_dir"`
	ListenAddr      *string `yaml:"listen_addr"`
	LogLevel        *string `yaml:"log_level"`
	ShutdownTimeout *string `yaml:"shutdown_timeout"`

	PluginDir            *string `yaml:"plugin_dir"`
	PluginHotReload      *bool   `yaml:"plugin_hot_reload"`
	PluginScanInterval   *string `yaml:"plugin_scan_interval"`
	PluginInvokeTimeout  *string `yaml:"plugin_invoke_timeout"`
	PluginMaxResultBytes *int64  `yaml:"plugin_max_result_bytes"`
	PluginMaxConcurrency *int    `yaml:"plugin_max_concurrency"`

	ExecutionQueueBatch    *int    `yaml:"execution_queue_batch"`
	ExecutionQueueInterval *string `yaml:"execution_queue_interval"`

	SessionTTL       *string `yaml:"session_ttl"`
	LoginMaxAttempts *int    `yaml:"login_max_attempts"`
	LoginLockout     *string `yaml:"login_lockout"`
	AdminPassword    *string `yaml:"admin_password"`

	CredentialKey *string `yaml:"credential_key"`

	SSHConnectTimeout          *string `yaml:"ssh_connect_timeout"`
	SSHUploadTimeout           *string `yaml:"ssh_upload_timeout"`
	DistributionMaxConcurrency *int    `yaml:"distribution_max_concurrency"`
	DistributionRemoteRoot     *string `yaml:"distribution_remote_root"`

	TaskScanInterval   *string `yaml:"task_scan_interval"`
	TaskMinInterval    *string `yaml:"task_min_interval"`
	TaskDefaultTimeout *string `yaml:"task_default_timeout"`
	TaskDefaultRetries *int    `yaml:"task_default_retries"`

	PKIBaseID                *string `yaml:"pki_base_id"`
	PKICAValidity            *string `yaml:"pki_ca_validity"`
	PKILeafValidity          *string `yaml:"pki_leaf_validity"`
	PluginProbeInterval      *string `yaml:"plugin_probe_interval"`
	PluginStateTTL           *string `yaml:"plugin_state_ttl"`
	RemoteDialTimeout        *string `yaml:"remote_dial_timeout"`
	RemoteReadyTimeout       *string `yaml:"remote_ready_timeout"`
	DistributionConfigRoot   *string `yaml:"distribution_config_root"`
	DistributionDataRoot     *string `yaml:"distribution_data_root"`
	DistributionUnitDir      *string `yaml:"distribution_unit_dir"`
	DistributionServiceUser  *string `yaml:"distribution_service_user"`
	DistributionStartTimeout *string `yaml:"distribution_start_timeout"`
}

// Load 解析命令行参数、环境变量与配置文件并返回校验后的配置。
// getenv 允许注入，便于测试优先级规则；传 nil 时使用 os.Getenv。
func Load(args []string, getenv func(string) string) (*Config, error) {
	if getenv == nil {
		getenv = os.Getenv
	}

	cfg := &Config{
		LogLevel:                   DefaultLogLevel,
		ShutdownTimeout:            DefaultShutdownTimeout,
		PluginScanInterval:         DefaultPluginScanInterval,
		PluginInvokeTimeout:        DefaultPluginInvokeTimeout,
		PluginMaxResultSize:        DefaultPluginMaxResultBytes,
		PluginMaxConcurrency:       DefaultPluginMaxConcurrency,
		ExecutionQueueBatch:        DefaultQueueBatch,
		ExecutionQueueInterval:     DefaultQueueInterval,
		SessionTTL:                 DefaultSessionTTL,
		LoginMaxAttempts:           DefaultLoginMaxAttempts,
		LoginLockout:               DefaultLoginLockout,
		SSHConnectTimeout:          DefaultSSHConnectTimeout,
		SSHUploadTimeout:           DefaultSSHUploadTimeout,
		DistributionMaxConcurrency: DefaultDistMaxConcurrency,
		DistributionRemoteRoot:     DefaultDistRemoteRoot,
		TaskScanInterval:           DefaultTaskScanInterval,
		TaskMinInterval:            DefaultTaskMinInterval,
		TaskDefaultTimeout:         DefaultTaskTimeout,
		TaskDefaultRetries:         DefaultTaskRetries,
		PKIBaseID:                  DefaultPKIBaseID,
		PKICAValidity:              DefaultPKICAValidity,
		PKILeafValidity:            DefaultPKILeafValidity,
		PluginProbeInterval:        DefaultPluginProbeInterval,
		PluginStateTTL:             DefaultPluginStateTTL,
		RemoteDialTimeout:          DefaultRemoteDialTimeout,
		RemoteReadyTimeout:         DefaultRemoteReadyTimeout,
		DistributionConfigRoot:     DefaultDistConfigRoot,
		DistributionDataRoot:       DefaultDistDataRoot,
		DistributionUnitDir:        DefaultDistUnitDir,
		DistributionServiceUser:    DefaultDistServiceUser,
		DistributionStartTimeout:   DefaultDistStartTimeout,
	}

	fs := flag.NewFlagSet("operations-platform", flag.ContinueOnError)
	var usage bytes.Buffer
	fs.SetOutput(&usage)
	configPath := fs.String("config", "", "配置文件路径（YAML）")
	dataDirFlag := fs.String("data-dir", "", "数据目录（必填，绝对路径）")
	listenFlag := fs.String("listen", "", "HTTP 监听地址（必填，host:port）")
	logLevelFlag := fs.String("log-level", "", "日志级别：debug|info|warn|error")
	shutdownFlag := fs.String("shutdown-timeout", "", "优雅关闭超时（例如 10s）")
	pluginDirFlag := fs.String("plugin-dir", "", "插件目录（默认 <data_dir>/plugins）")
	hotReloadFlag := fs.Bool("plugin-hot-reload", false, "开启插件目录热加载")
	scanIntervalFlag := fs.String("plugin-scan-interval", "", "插件/任务扫描周期（例如 30s）")
	pluginTimeoutFlag := fs.String("plugin-invoke-timeout", "", "插件调用超时（例如 30s）")
	pluginMaxBytesFlag := fs.Int64("plugin-max-result-bytes", 0, "插件结果体积上限（字节）")
	pluginMaxConcFlag := fs.Int("plugin-max-concurrency", 0, "插件并发调用上限")
	credentialKeyFlag := fs.String("credential-key", "", "凭据主密钥（base64，32 字节）；生产环境请用环境变量")
	if err := fs.Parse(args); err != nil {
		return nil, fmt.Errorf("解析命令行参数失败: %w\n%s", err, usage.String())
	}

	if *configPath != "" {
		fc, err := readConfigFile(*configPath)
		if err != nil {
			return nil, err
		}
		if err := fc.apply(cfg); err != nil {
			return nil, fmt.Errorf("配置文件 %q 非法: %w", *configPath, err)
		}
		cfg.ConfigFile = *configPath
	}

	// 环境变量只覆盖非空值：空字符串视为未设置，避免容器里误置空变量把文件配置抹掉。
	envSetters := []struct {
		name string
		fn   func(string) error
	}{
		{EnvDataDir, func(v string) error { cfg.DataDir = v; return nil }},
		{EnvListenAddr, func(v string) error { cfg.ListenAddr = v; return nil }},
		{EnvLogLevel, func(v string) error { cfg.LogLevel = v; return nil }},
		{EnvShutdownTimeout, func(v string) error { return setDuration(v, &cfg.ShutdownTimeout, "环境变量 "+EnvShutdownTimeout) }},
		{EnvPluginDir, func(v string) error { cfg.PluginDir = v; return nil }},
		{EnvPluginHotReload, func(v string) error { return setBool(v, &cfg.PluginHotReload, "环境变量 "+EnvPluginHotReload) }},
		{EnvPluginScan, func(v string) error { return setDuration(v, &cfg.PluginScanInterval, "环境变量 "+EnvPluginScan) }},
		{EnvPluginTimeout, func(v string) error {
			return setDuration(v, &cfg.PluginInvokeTimeout, "环境变量 "+EnvPluginTimeout)
		}},
		{EnvPluginMaxBytes, func(v string) error { return setInt64(v, &cfg.PluginMaxResultSize, "环境变量 "+EnvPluginMaxBytes) }},
		{EnvPluginMaxConc, func(v string) error { return setInt(v, &cfg.PluginMaxConcurrency, "环境变量 "+EnvPluginMaxConc) }},
		{EnvQueueBatch, func(v string) error { return setInt(v, &cfg.ExecutionQueueBatch, "环境变量 "+EnvQueueBatch) }},
		{EnvQueueInterval, func(v string) error {
			return setDuration(v, &cfg.ExecutionQueueInterval, "环境变量 "+EnvQueueInterval)
		}},
		{EnvSessionTTL, func(v string) error { return setDuration(v, &cfg.SessionTTL, "环境变量 "+EnvSessionTTL) }},
		{EnvLoginAttempts, func(v string) error { return setInt(v, &cfg.LoginMaxAttempts, "环境变量 "+EnvLoginAttempts) }},
		{EnvLoginLockout, func(v string) error {
			return setDuration(v, &cfg.LoginLockout, "环境变量 "+EnvLoginLockout)
		}},
		{EnvAdminPassword, func(v string) error { cfg.AdminPassword = v; return nil }},
		{EnvCredentialKey, func(v string) error {
			return setKey(v, &cfg.CredentialKey, "环境变量 "+EnvCredentialKey)
		}},
		{EnvSSHConnect, func(v string) error { return setDuration(v, &cfg.SSHConnectTimeout, "环境变量 "+EnvSSHConnect) }},
		{EnvSSHUpload, func(v string) error { return setDuration(v, &cfg.SSHUploadTimeout, "环境变量 "+EnvSSHUpload) }},
		{EnvDistMaxConc, func(v string) error {
			return setInt(v, &cfg.DistributionMaxConcurrency, "环境变量 "+EnvDistMaxConc)
		}},
		{EnvDistRemoteRoot, func(v string) error { cfg.DistributionRemoteRoot = v; return nil }},
		{EnvTaskScan, func(v string) error { return setDuration(v, &cfg.TaskScanInterval, "环境变量 "+EnvTaskScan) }},
		{EnvTaskMinInterval, func(v string) error {
			return setDuration(v, &cfg.TaskMinInterval, "环境变量 "+EnvTaskMinInterval)
		}},
		{EnvTaskTimeout, func(v string) error { return setDuration(v, &cfg.TaskDefaultTimeout, "环境变量 "+EnvTaskTimeout) }},
		{EnvTaskRetries, func(v string) error { return setInt(v, &cfg.TaskDefaultRetries, "环境变量 "+EnvTaskRetries) }},
		{EnvPKIBaseID, func(v string) error { cfg.PKIBaseID = v; return nil }},
		{EnvPKICAValidity, func(v string) error {
			return setDuration(v, &cfg.PKICAValidity, "环境变量 "+EnvPKICAValidity)
		}},
		{EnvPKILeafValidity, func(v string) error {
			return setDuration(v, &cfg.PKILeafValidity, "环境变量 "+EnvPKILeafValidity)
		}},
		{EnvPluginProbe, func(v string) error {
			return setDuration(v, &cfg.PluginProbeInterval, "环境变量 "+EnvPluginProbe)
		}},
		{EnvPluginStateTTL, func(v string) error {
			return setDuration(v, &cfg.PluginStateTTL, "环境变量 "+EnvPluginStateTTL)
		}},
		{EnvRemoteDial, func(v string) error {
			return setDuration(v, &cfg.RemoteDialTimeout, "环境变量 "+EnvRemoteDial)
		}},
		{EnvRemoteReady, func(v string) error {
			return setDuration(v, &cfg.RemoteReadyTimeout, "环境变量 "+EnvRemoteReady)
		}},
		{EnvDistConfigRoot, func(v string) error { cfg.DistributionConfigRoot = v; return nil }},
		{EnvDistDataRoot, func(v string) error { cfg.DistributionDataRoot = v; return nil }},
		{EnvDistUnitDir, func(v string) error { cfg.DistributionUnitDir = v; return nil }},
		{EnvDistServiceUser, func(v string) error { cfg.DistributionServiceUser = v; return nil }},
		{EnvDistStart, func(v string) error {
			return setDuration(v, &cfg.DistributionStartTimeout, "环境变量 "+EnvDistStart)
		}},
	}
	for _, s := range envSetters {
		if v := getenv(s.name); v != "" {
			if err := s.fn(v); err != nil {
				return nil, err
			}
		}
	}

	// flag.Visit 只遍历显式出现的参数，因此「命令行 > 环境变量」不会被默认值破坏。
	var flagErr error
	fs.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "data-dir":
			cfg.DataDir = *dataDirFlag
		case "listen":
			cfg.ListenAddr = *listenFlag
		case "log-level":
			cfg.LogLevel = *logLevelFlag
		case "shutdown-timeout":
			flagErr = setDuration(*shutdownFlag, &cfg.ShutdownTimeout, "命令行参数 -shutdown-timeout")
		case "plugin-dir":
			cfg.PluginDir = *pluginDirFlag
		case "plugin-hot-reload":
			cfg.PluginHotReload = *hotReloadFlag
		case "plugin-scan-interval":
			flagErr = setDuration(*scanIntervalFlag, &cfg.PluginScanInterval, "命令行参数 -plugin-scan-interval")
		case "plugin-invoke-timeout":
			flagErr = setDuration(*pluginTimeoutFlag, &cfg.PluginInvokeTimeout, "命令行参数 -plugin-invoke-timeout")
		case "plugin-max-result-bytes":
			cfg.PluginMaxResultSize = *pluginMaxBytesFlag
		case "plugin-max-concurrency":
			cfg.PluginMaxConcurrency = *pluginMaxConcFlag
		case "credential-key":
			flagErr = setKey(*credentialKeyFlag, &cfg.CredentialKey, "命令行参数 -credential-key")
		}
	})
	if flagErr != nil {
		return nil, flagErr
	}

	if err := cfg.normalize(); err != nil {
		return nil, err
	}
	return cfg, cfg.validate()
}

func readConfigFile(path string) (fileConfig, error) {
	var fc fileConfig
	f, err := os.Open(path)
	if err != nil {
		return fc, fmt.Errorf("读取配置文件 %q 失败: %w", path, err)
	}
	defer f.Close()

	dec := yaml.NewDecoder(f)
	// 严格拒绝未知字段：配置键写错时必须在启动阶段暴露，不能静默忽略。
	dec.KnownFields(true)
	if err := dec.Decode(&fc); err != nil {
		return fc, fmt.Errorf("解析配置文件 %q 失败（格式为 YAML）: %w", path, err)
	}
	return fc, nil
}

func (fc fileConfig) apply(cfg *Config) error {
	if fc.DataDir != nil {
		cfg.DataDir = *fc.DataDir
	}
	if fc.ListenAddr != nil {
		cfg.ListenAddr = *fc.ListenAddr
	}
	if fc.LogLevel != nil {
		cfg.LogLevel = *fc.LogLevel
	}
	if fc.ShutdownTimeout != nil {
		if err := setDuration(*fc.ShutdownTimeout, &cfg.ShutdownTimeout, "配置项 shutdown_timeout"); err != nil {
			return err
		}
	}
	if fc.PluginDir != nil {
		cfg.PluginDir = *fc.PluginDir
	}
	if fc.PluginHotReload != nil {
		cfg.PluginHotReload = *fc.PluginHotReload
	}
	if fc.PluginScanInterval != nil {
		if err := setDuration(*fc.PluginScanInterval, &cfg.PluginScanInterval, "配置项 plugin_scan_interval"); err != nil {
			return err
		}
	}
	if fc.PluginInvokeTimeout != nil {
		if err := setDuration(*fc.PluginInvokeTimeout, &cfg.PluginInvokeTimeout, "配置项 plugin_invoke_timeout"); err != nil {
			return err
		}
	}
	if fc.PluginMaxResultBytes != nil {
		cfg.PluginMaxResultSize = *fc.PluginMaxResultBytes
	}
	if fc.PluginMaxConcurrency != nil {
		cfg.PluginMaxConcurrency = *fc.PluginMaxConcurrency
	}
	if fc.ExecutionQueueBatch != nil {
		cfg.ExecutionQueueBatch = *fc.ExecutionQueueBatch
	}
	if fc.ExecutionQueueInterval != nil {
		if err := setDuration(*fc.ExecutionQueueInterval, &cfg.ExecutionQueueInterval, "配置项 execution_queue_interval"); err != nil {
			return err
		}
	}
	if fc.SessionTTL != nil {
		if err := setDuration(*fc.SessionTTL, &cfg.SessionTTL, "配置项 session_ttl"); err != nil {
			return err
		}
	}
	if fc.LoginMaxAttempts != nil {
		cfg.LoginMaxAttempts = *fc.LoginMaxAttempts
	}
	if fc.LoginLockout != nil {
		if err := setDuration(*fc.LoginLockout, &cfg.LoginLockout, "配置项 login_lockout"); err != nil {
			return err
		}
	}
	if fc.AdminPassword != nil {
		cfg.AdminPassword = *fc.AdminPassword
	}
	if fc.CredentialKey != nil {
		if err := setKey(*fc.CredentialKey, &cfg.CredentialKey, "配置项 credential_key"); err != nil {
			return err
		}
	}
	if fc.SSHConnectTimeout != nil {
		if err := setDuration(*fc.SSHConnectTimeout, &cfg.SSHConnectTimeout, "配置项 ssh_connect_timeout"); err != nil {
			return err
		}
	}
	if fc.SSHUploadTimeout != nil {
		if err := setDuration(*fc.SSHUploadTimeout, &cfg.SSHUploadTimeout, "配置项 ssh_upload_timeout"); err != nil {
			return err
		}
	}
	if fc.DistributionMaxConcurrency != nil {
		cfg.DistributionMaxConcurrency = *fc.DistributionMaxConcurrency
	}
	if fc.DistributionRemoteRoot != nil {
		cfg.DistributionRemoteRoot = *fc.DistributionRemoteRoot
	}
	if fc.TaskScanInterval != nil {
		if err := setDuration(*fc.TaskScanInterval, &cfg.TaskScanInterval, "配置项 task_scan_interval"); err != nil {
			return err
		}
	}
	if fc.TaskMinInterval != nil {
		if err := setDuration(*fc.TaskMinInterval, &cfg.TaskMinInterval, "配置项 task_min_interval"); err != nil {
			return err
		}
	}
	if fc.TaskDefaultTimeout != nil {
		if err := setDuration(*fc.TaskDefaultTimeout, &cfg.TaskDefaultTimeout, "配置项 task_default_timeout"); err != nil {
			return err
		}
	}
	if fc.TaskDefaultRetries != nil {
		cfg.TaskDefaultRetries = *fc.TaskDefaultRetries
	}
	if fc.PKIBaseID != nil {
		cfg.PKIBaseID = *fc.PKIBaseID
	}
	if fc.PKICAValidity != nil {
		if err := setDuration(*fc.PKICAValidity, &cfg.PKICAValidity, "配置项 pki_ca_validity"); err != nil {
			return err
		}
	}
	if fc.PKILeafValidity != nil {
		if err := setDuration(*fc.PKILeafValidity, &cfg.PKILeafValidity, "配置项 pki_leaf_validity"); err != nil {
			return err
		}
	}
	if fc.PluginProbeInterval != nil {
		if err := setDuration(*fc.PluginProbeInterval, &cfg.PluginProbeInterval, "配置项 plugin_probe_interval"); err != nil {
			return err
		}
	}
	if fc.PluginStateTTL != nil {
		if err := setDuration(*fc.PluginStateTTL, &cfg.PluginStateTTL, "配置项 plugin_state_ttl"); err != nil {
			return err
		}
	}
	if fc.RemoteDialTimeout != nil {
		if err := setDuration(*fc.RemoteDialTimeout, &cfg.RemoteDialTimeout, "配置项 remote_dial_timeout"); err != nil {
			return err
		}
	}
	if fc.RemoteReadyTimeout != nil {
		if err := setDuration(*fc.RemoteReadyTimeout, &cfg.RemoteReadyTimeout, "配置项 remote_ready_timeout"); err != nil {
			return err
		}
	}
	if fc.DistributionConfigRoot != nil {
		cfg.DistributionConfigRoot = *fc.DistributionConfigRoot
	}
	if fc.DistributionDataRoot != nil {
		cfg.DistributionDataRoot = *fc.DistributionDataRoot
	}
	if fc.DistributionUnitDir != nil {
		cfg.DistributionUnitDir = *fc.DistributionUnitDir
	}
	if fc.DistributionServiceUser != nil {
		cfg.DistributionServiceUser = *fc.DistributionServiceUser
	}
	if fc.DistributionStartTimeout != nil {
		if err := setDuration(*fc.DistributionStartTimeout, &cfg.DistributionStartTimeout, "配置项 distribution_start_timeout"); err != nil {
			return err
		}
	}
	return nil
}

func setDuration(raw string, dst *time.Duration, source string) error {
	d, err := time.ParseDuration(raw)
	if err != nil {
		return fmt.Errorf("%s 不是合法的时长（例如 10s）: %w", source, err)
	}
	*dst = d
	return nil
}

func setBool(raw string, dst *bool, source string) error {
	b, err := strconv.ParseBool(raw)
	if err != nil {
		return fmt.Errorf("%s 不是合法的布尔值（true/false）: %w", source, err)
	}
	*dst = b
	return nil
}

func setInt(raw string, dst *int, source string) error {
	n, err := strconv.Atoi(raw)
	if err != nil {
		return fmt.Errorf("%s 不是合法的整数: %w", source, err)
	}
	*dst = n
	return nil
}

func setInt64(raw string, dst *int64, source string) error {
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return fmt.Errorf("%s 不是合法的整数: %w", source, err)
	}
	*dst = n
	return nil
}

// setKey 只接受 base64 编码的 32 字节主密钥；密钥内容绝不回显到错误信息之外。
func setKey(raw string, dst *[]byte, source string) error {
	key, err := base64.StdEncoding.DecodeString(strings.TrimSpace(raw))
	if err != nil {
		return fmt.Errorf("%s 不是合法的 base64 字符串: %w", source, err)
	}
	if len(key) != 32 {
		return fmt.Errorf("%s 解码后必须为 32 字节（AES-256），当前 %d 字节", source, len(key))
	}
	*dst = key
	return nil
}

// normalize 处理依赖其他字段的默认值。
func (cfg *Config) normalize() error {
	if cfg.PluginDir == "" {
		if cfg.DataDir == "" {
			return nil // 交给 validate 报「未配置数据目录」
		}
		cfg.PluginDir = filepath.Join(cfg.DataDir, "plugins")
		return nil
	}
	if !filepath.IsAbs(cfg.PluginDir) {
		if cfg.DataDir == "" {
			return errors.New("plugin_dir 为相对路径时要求同时配置 data_dir")
		}
		cfg.PluginDir = filepath.Join(cfg.DataDir, cfg.PluginDir)
	}
	rel, err := filepath.Rel(cfg.DataDir, cfg.PluginDir)
	if err == nil && (rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator))) {
		return fmt.Errorf("plugin_dir %q 解析后越出数据目录 %q", cfg.PluginDir, cfg.DataDir)
	}
	return nil
}

func (cfg *Config) validate() error {
	if cfg.DataDir == "" {
		return errors.New("未配置数据目录：请通过 -data-dir、环境变量 " + EnvDataDir + " 或配置文件 data_dir 指定（默认回退位置未定，OQ-022）")
	}
	if !filepath.IsAbs(cfg.DataDir) {
		return fmt.Errorf("数据目录必须为绝对路径，当前为 %q（DOC-08 ST-D-01）", cfg.DataDir)
	}

	if cfg.ListenAddr == "" {
		return errors.New("未配置监听地址：请通过 -listen、环境变量 " + EnvListenAddr + " 或配置文件 listen_addr 指定（默认监听地址未定，OQ-036）")
	}
	if err := validateListenAddr(cfg.ListenAddr); err != nil {
		return err
	}

	if _, err := observability.ParseLevel(cfg.LogLevel); err != nil {
		return fmt.Errorf("配置项 log_level 非法: %w", err)
	}

	if cfg.ShutdownTimeout <= 0 {
		return fmt.Errorf("优雅关闭超时必须大于 0，当前为 %s", cfg.ShutdownTimeout)
	}
	if cfg.PluginScanInterval < time.Second {
		return fmt.Errorf("plugin_scan_interval 不得小于 1s，当前为 %s", cfg.PluginScanInterval)
	}
	if cfg.PluginInvokeTimeout <= 0 {
		return fmt.Errorf("plugin_invoke_timeout 必须大于 0，当前为 %s", cfg.PluginInvokeTimeout)
	}
	if cfg.PluginMaxResultSize < 1<<10 || cfg.PluginMaxResultSize > 1<<30 {
		return fmt.Errorf("plugin_max_result_bytes 必须在 1KiB~1GiB 之间，当前为 %d", cfg.PluginMaxResultSize)
	}
	if cfg.PluginMaxConcurrency < 1 || cfg.PluginMaxConcurrency > 1000 {
		return fmt.Errorf("plugin_max_concurrency 必须在 1~1000 之间，当前为 %d", cfg.PluginMaxConcurrency)
	}
	if cfg.ExecutionQueueBatch < 1 || cfg.ExecutionQueueBatch > 10000 {
		return fmt.Errorf("execution_queue_batch 必须在 1~10000 之间，当前为 %d", cfg.ExecutionQueueBatch)
	}
	if cfg.ExecutionQueueInterval < 10*time.Millisecond || cfg.ExecutionQueueInterval > 10*time.Second {
		return fmt.Errorf("execution_queue_interval 必须在 10ms~10s 之间，当前为 %s", cfg.ExecutionQueueInterval)
	}
	if cfg.SessionTTL <= 0 {
		return fmt.Errorf("session_ttl 必须大于 0，当前为 %s", cfg.SessionTTL)
	}
	if cfg.LoginMaxAttempts < 1 || cfg.LoginMaxAttempts > 100 {
		return fmt.Errorf("login_max_attempts 必须在 1~100 之间，当前为 %d", cfg.LoginMaxAttempts)
	}
	if cfg.LoginLockout < time.Second {
		return fmt.Errorf("login_lockout 不得小于 1s，当前为 %s", cfg.LoginLockout)
	}
	if cfg.SSHConnectTimeout <= 0 {
		return fmt.Errorf("ssh_connect_timeout 必须大于 0，当前为 %s", cfg.SSHConnectTimeout)
	}
	if cfg.SSHUploadTimeout <= 0 {
		return fmt.Errorf("ssh_upload_timeout 必须大于 0，当前为 %s", cfg.SSHUploadTimeout)
	}
	if cfg.DistributionMaxConcurrency < 1 || cfg.DistributionMaxConcurrency > 64 {
		return fmt.Errorf("distribution_max_concurrency 必须在 1~64 之间，当前为 %d", cfg.DistributionMaxConcurrency)
	}
	// 远端安装根目录始终是目标机上的 POSIX 路径；Windows 上 filepath.IsAbs 不认 "/opt/..."，需单独放行。
	if !filepath.IsAbs(cfg.DistributionRemoteRoot) && !strings.HasPrefix(cfg.DistributionRemoteRoot, "/") {
		return fmt.Errorf("distribution_remote_root 必须为绝对路径，当前为 %q", cfg.DistributionRemoteRoot)
	}
	if cfg.TaskScanInterval < time.Second {
		return fmt.Errorf("task_scan_interval 不得小于 1s，当前为 %s", cfg.TaskScanInterval)
	}
	if cfg.TaskMinInterval < time.Minute {
		return fmt.Errorf("task_min_interval 不得小于 1m，当前为 %s", cfg.TaskMinInterval)
	}
	if cfg.TaskDefaultTimeout <= 0 {
		return fmt.Errorf("task_default_timeout 必须大于 0，当前为 %s", cfg.TaskDefaultTimeout)
	}
	if cfg.TaskDefaultRetries < 0 || cfg.TaskDefaultRetries > 10 {
		return fmt.Errorf("task_default_retries 必须在 0~10 之间，当前为 %d", cfg.TaskDefaultRetries)
	}
	if strings.TrimSpace(cfg.PKIBaseID) == "" {
		return fmt.Errorf("pki_base_id 不能为空")
	}
	if cfg.PKICAValidity <= 0 || cfg.PKILeafValidity <= 0 {
		return fmt.Errorf("pki_ca_validity 与 pki_leaf_validity 必须大于 0")
	}
	if cfg.PKILeafValidity >= cfg.PKICAValidity {
		return fmt.Errorf("pki_leaf_validity 必须小于 pki_ca_validity，当前为 %s >= %s",
			cfg.PKILeafValidity, cfg.PKICAValidity)
	}
	if cfg.PluginProbeInterval < 10*time.Second {
		return fmt.Errorf("plugin_probe_interval 不得小于 10s，当前为 %s", cfg.PluginProbeInterval)
	}
	if cfg.PluginStateTTL < cfg.PluginProbeInterval {
		return fmt.Errorf("plugin_state_ttl 不得小于 plugin_probe_interval（%s），当前为 %s",
			cfg.PluginProbeInterval, cfg.PluginStateTTL)
	}
	if cfg.RemoteDialTimeout <= 0 || cfg.RemoteReadyTimeout <= 0 {
		return fmt.Errorf("remote_dial_timeout 与 remote_ready_timeout 必须大于 0")
	}
	for name, value := range map[string]string{
		"distribution_config_root": cfg.DistributionConfigRoot,
		"distribution_data_root":   cfg.DistributionDataRoot,
		"distribution_unit_dir":    cfg.DistributionUnitDir,
	} {
		if !strings.HasPrefix(value, "/") {
			return fmt.Errorf("%s 必须为绝对路径，当前为 %q", name, value)
		}
	}
	if strings.TrimSpace(cfg.DistributionServiceUser) == "" {
		return fmt.Errorf("distribution_service_user 不能为空")
	}
	if cfg.DistributionStartTimeout <= 0 {
		return fmt.Errorf("distribution_start_timeout 必须大于 0")
	}
	return nil
}

func validateListenAddr(addr string) error {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("监听地址 %q 非法（应形如 127.0.0.1:8080）: %w", addr, err)
	}
	if host != "" {
		if ip := net.ParseIP(host); ip == nil {
			// 允许 localhost 这类主机名，但拒绝带协议前缀的写法。
			if host == "localhost" {
				return nil
			}
			return fmt.Errorf("监听地址 %q 的主机部分非法", addr)
		}
	}
	portNum, err := strconv.Atoi(port)
	if err != nil || portNum < 1 || portNum > 65535 {
		return fmt.Errorf("监听地址 %q 的端口非法（应为 1-65535）", addr)
	}
	return nil
}

// Redacted 返回可安全写入日志的最终生效配置；主密钥只暴露「是否已配置」。
func (cfg *Config) Redacted() map[string]any {
	return map[string]any{
		"config_file":                  cfg.ConfigFile,
		"data_dir":                     cfg.DataDir,
		"listen_addr":                  cfg.ListenAddr,
		"log_level":                    cfg.LogLevel,
		"shutdown_timeout":             cfg.ShutdownTimeout.String(),
		"plugin_dir":                   cfg.PluginDir,
		"plugin_hot_reload":            cfg.PluginHotReload,
		"plugin_scan_interval":         cfg.PluginScanInterval.String(),
		"plugin_invoke_timeout":        cfg.PluginInvokeTimeout.String(),
		"plugin_max_result_bytes":      cfg.PluginMaxResultSize,
		"plugin_max_concurrency":       cfg.PluginMaxConcurrency,
		"execution_queue_batch":        cfg.ExecutionQueueBatch,
		"execution_queue_interval":     cfg.ExecutionQueueInterval.String(),
		"session_ttl":                  cfg.SessionTTL.String(),
		"login_max_attempts":           cfg.LoginMaxAttempts,
		"login_lockout":                cfg.LoginLockout.String(),
		"credential_key_configured":    len(cfg.CredentialKey) > 0,
		"ssh_connect_timeout":          cfg.SSHConnectTimeout.String(),
		"ssh_upload_timeout":           cfg.SSHUploadTimeout.String(),
		"distribution_max_concurrency": cfg.DistributionMaxConcurrency,
		"distribution_remote_root":     cfg.DistributionRemoteRoot,
		"task_scan_interval":           cfg.TaskScanInterval.String(),
		"task_min_interval":            cfg.TaskMinInterval.String(),
		"task_default_timeout":         cfg.TaskDefaultTimeout.String(),
		"task_default_retries":         cfg.TaskDefaultRetries,
	}
}
