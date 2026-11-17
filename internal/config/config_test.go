package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func writeConfig(t *testing.T, payload map[string]any) string {
	t.Helper()
	data, err := yaml.Marshal(payload)
	if err != nil {
		t.Fatalf("序列化测试配置失败: %v", err)
	}
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("写入测试配置失败: %v", err)
	}
	return path
}

func TestLoadUsesDefaultsForOptionalItems(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), "data")
	cfg, err := Load([]string{"-data-dir", dataDir, "-listen", "127.0.0.1:8080"}, nil)
	if err != nil {
		t.Fatalf("Load 失败: %v", err)
	}
	if cfg.LogLevel != DefaultLogLevel {
		t.Errorf("log_level 默认值 = %q，期望 %q", cfg.LogLevel, DefaultLogLevel)
	}
	if cfg.ShutdownTimeout != DefaultShutdownTimeout {
		t.Errorf("shutdown_timeout 默认值 = %s，期望 %s", cfg.ShutdownTimeout, DefaultShutdownTimeout)
	}
	if cfg.PluginDir != filepath.Join(dataDir, "plugins") {
		t.Errorf("plugin_dir 默认值 = %q，期望 %q", cfg.PluginDir, filepath.Join(dataDir, "plugins"))
	}
	if cfg.PluginHotReload {
		t.Error("plugin_hot_reload 默认应为关闭")
	}
	if cfg.PluginInvokeTimeout != DefaultPluginInvokeTimeout {
		t.Errorf("plugin_invoke_timeout 默认值 = %s", cfg.PluginInvokeTimeout)
	}
	if cfg.PluginMaxResultSize != DefaultPluginMaxResultBytes {
		t.Errorf("plugin_max_result_bytes 默认值 = %d", cfg.PluginMaxResultSize)
	}
	if cfg.PluginMaxConcurrency != DefaultPluginMaxConcurrency {
		t.Errorf("plugin_max_concurrency 默认值 = %d", cfg.PluginMaxConcurrency)
	}
	if cfg.TaskDefaultRetries != DefaultTaskRetries {
		t.Errorf("task_default_retries 默认值 = %d", cfg.TaskDefaultRetries)
	}
}

func TestLoadPrecedence(t *testing.T) {
	fileDataDir := filepath.Join(t.TempDir(), "data-from-file")
	envDataDir := filepath.Join(t.TempDir(), "data-from-env")
	cliDataDir := filepath.Join(t.TempDir(), "data-from-cli")

	configPath := writeConfig(t, map[string]any{
		"data_dir":               fileDataDir,
		"listen_addr":            "127.0.0.1:9001",
		"log_level":              "warn",
		"shutdown_timeout":       "20s",
		"plugin_hot_reload":      true,
		"plugin_max_concurrency": 8,
	})

	env := map[string]string{
		EnvDataDir:         envDataDir,
		EnvListenAddr:      "127.0.0.1:9002",
		EnvLogLevel:        "error",
		EnvShutdownTimeout: "30s",
		EnvPluginMaxConc:   "12",
	}

	cfg, err := Load(
		[]string{"-config", configPath, "-data-dir", cliDataDir, "-listen", "127.0.0.1:9003"},
		func(key string) string { return env[key] },
	)
	if err != nil {
		t.Fatalf("Load 失败: %v", err)
	}

	if cfg.DataDir != cliDataDir {
		t.Errorf("data_dir = %q，期望命令行值 %q", cfg.DataDir, cliDataDir)
	}
	if cfg.ListenAddr != "127.0.0.1:9003" {
		t.Errorf("listen_addr = %q，期望命令行值 127.0.0.1:9003", cfg.ListenAddr)
	}
	if cfg.LogLevel != "error" {
		t.Errorf("log_level = %q，期望环境变量值 error", cfg.LogLevel)
	}
	if cfg.ShutdownTimeout.String() != "30s" {
		t.Errorf("shutdown_timeout = %s，期望环境变量值 30s", cfg.ShutdownTimeout)
	}
	if !cfg.PluginHotReload {
		t.Error("plugin_hot_reload 应来自配置文件 true")
	}
	if cfg.PluginMaxConcurrency != 12 {
		t.Errorf("plugin_max_concurrency = %d，期望环境变量值 12", cfg.PluginMaxConcurrency)
	}
}

func TestLoadRejectsMissingDataDir(t *testing.T) {
	_, err := Load([]string{"-listen", "127.0.0.1:8080"}, func(string) string { return "" })
	if err == nil || !strings.Contains(err.Error(), "未配置数据目录") {
		t.Fatalf("期望拒绝缺失数据目录，实际: %v", err)
	}
}

func TestLoadRejectsRelativeDataDir(t *testing.T) {
	_, err := Load([]string{"-data-dir", "relative/path", "-listen", "127.0.0.1:8080"}, func(string) string { return "" })
	if err == nil || !strings.Contains(err.Error(), "绝对路径") {
		t.Fatalf("期望拒绝相对路径，实际: %v", err)
	}
}

func TestLoadRejectsUnknownConfigField(t *testing.T) {
	configPath := writeConfig(t, map[string]any{
		"data_dir":    filepath.Join(t.TempDir(), "data"),
		"listen_addr": "127.0.0.1:8080",
		"unknown_key": "x",
	})
	_, err := Load([]string{"-config", configPath}, func(string) string { return "" })
	if err == nil || !strings.Contains(err.Error(), "解析配置文件") {
		t.Fatalf("期望拒绝未知配置字段，实际: %v", err)
	}
}

func TestLoadRejectsPluginDirOutsideDataDir(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), "data")
	_, err := Load([]string{"-data-dir", dataDir, "-listen", "127.0.0.1:8080", "-plugin-dir", "../outside"}, func(string) string { return "" })
	if err == nil || !strings.Contains(err.Error(), "越出数据目录") {
		t.Fatalf("期望拒绝越出数据目录的 plugin_dir，实际: %v", err)
	}
}

func TestLoadRejectsInvalidCredentialKey(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), "data")
	_, err := Load([]string{"-data-dir", dataDir, "-listen", "127.0.0.1:8080"}, func(key string) string {
		if key == EnvCredentialKey {
			return "not-base64!!"
		}
		return ""
	})
	if err == nil || !strings.Contains(err.Error(), "base64") {
		t.Fatalf("期望拒绝非法主密钥，实际: %v", err)
	}
}
