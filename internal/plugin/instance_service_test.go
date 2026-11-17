package plugin

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	goruntime "runtime"
	"testing"
	"time"

	"operations-platform/internal/plugin/instances"
	"operations-platform/internal/plugin/manifest"
	"operations-platform/internal/plugin/remote"
	"operations-platform/internal/store"
)

func TestValidateConfigValue(t *testing.T) {
	cases := []struct {
		name string
		f    manifest.ConfigField
		v    any
		ok   bool
	}{
		{"字符串", manifest.ConfigField{Type: "string"}, "x", true},
		{"字符串类型不符", manifest.ConfigField{Type: "string"}, 1.0, false},
		{"数字", manifest.ConfigField{Type: "number"}, 1.5, true},
		{"整数接受整值浮点", manifest.ConfigField{Type: "integer"}, 2.0, true},
		{"整数拒绝小数", manifest.ConfigField{Type: "integer"}, 2.5, false},
		{"布尔", manifest.ConfigField{Type: "boolean"}, true, true},
		{"枚举命中", manifest.ConfigField{Type: "enum", Enum: []string{"a", "b"}}, "b", true},
		{"枚举越界", manifest.ConfigField{Type: "enum", Enum: []string{"a"}}, "c", false},
		{"未知类型", manifest.ConfigField{Type: "duration"}, "1s", false},
	}
	for _, c := range cases {
		err := validateConfigValue(c.f, c.v)
		if c.ok && err != nil {
			t.Fatalf("%s: 期望通过，实际 %v", c.name, err)
		}
		if !c.ok && err == nil {
			t.Fatalf("%s: 期望拒绝", c.name)
		}
	}
}

func TestNormalizeInstanceID(t *testing.T) {
	cases := map[string]string{
		"demo":           "local:demo",
		" local:demo ":   "local:demo",
		"remote:h1:demo": "remote:h1:demo",
		"":               "",
	}
	for in, want := range cases {
		if got := normalizeInstanceID(in); got != want {
			t.Fatalf("normalizeInstanceID(%q) = %q，期望 %q", in, got, want)
		}
	}
}

// Windows 基线（OQ-039）：清单入口无后缀、实机为同名 .exe 时不得误报包缺失。
func TestResolveLocalEntry(t *testing.T) {
	root := t.TempDir()
	binDir := filepath.Join(root, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	exe := "starter"
	if goruntime.GOOS == "windows" {
		exe += ".exe"
	}
	if err := os.WriteFile(filepath.Join(binDir, exe), []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	// 清单写无后缀入口（契约要求），实机存在同名 .exe。
	got := resolveLocalEntry(root, filepath.Join("bin", "starter"))
	want := filepath.Join(binDir, exe)
	if goruntime.GOOS == "windows" {
		if got != want {
			t.Fatalf("resolveLocalEntry 回退失败：got %q，期望 %q", got, want)
		}
	} else if got != filepath.Join(binDir, "starter") {
		t.Fatalf("非 Windows 应返回原路径：got %q", got)
	}
	// 入口完全缺失时返回原路径，由调用方判定缺失。
	missing := resolveLocalEntry(root, filepath.Join("bin", "nope"))
	if missing != filepath.Join(binDir, "nope") {
		t.Fatalf("缺失入口应返回原路径：got %q", missing)
	}
}

func TestDetectRemoteDrift(t *testing.T) {
	base := instances.Instance{CertFingerprint: "aa:bb", PackageVersion: "1.0.0"}
	ok := remote.ProbeResult{Fingerprint: "AA:BB", Version: "1.0.0", Stage: "version", Detail: "ok"}
	if drift, _, _ := detectRemoteDrift(base, ok); drift != instances.DriftNone {
		t.Fatalf("指纹大小写不应判漂移，实际 %s", drift)
	}
	if drift, stage, _ := detectRemoteDrift(base, remote.ProbeResult{Fingerprint: "cc:dd", Version: "1.0.0"}); drift != instances.DriftCertMismatch || stage != "tls" {
		t.Fatalf("证书不符应判 cert_mismatch，实际 %s/%s", drift, stage)
	}
	if drift, stage, _ := detectRemoteDrift(base, remote.ProbeResult{Fingerprint: "aa:bb", Version: "2.0.0"}); drift != instances.DriftVersionMismatch || stage != "version" {
		t.Fatalf("版本不符应判 version_mismatch，实际 %s/%s", drift, stage)
	}
}

func TestSweepDegradesStaleRemoteInstance(t *testing.T) {
	ctx := context.Background()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	dataDir := t.TempDir()
	if err := store.InitDataDir(ctx, logger, dataDir, store.NoopFilesystemChecker{}); err != nil {
		t.Fatalf("初始化数据目录失败: %v", err)
	}
	st, err := store.Open(ctx, logger, dataDir)
	if err != nil {
		t.Fatalf("打开存储失败: %v", err)
	}
	defer st.Close()

	is := instances.NewStore(st)
	if err := is.UpsertRemoteInstance(ctx, instances.Instance{
		TypeID: "demo", HostID: "host-1", PackageVersion: "1.0.0",
		Installed: true, Started: true, Callable: true, State: instances.StateCallable,
	}); err != nil {
		t.Fatalf("写入远端实例失败: %v", err)
	}
	id := instances.RemoteInstanceID("host-1", "demo")

	// 从未探测过（零值时间）即视为过期；只装配巡检所需字段。
	s := &Service{instances: is, probeTTL: time.Minute, logger: logger}
	s.Sweep(ctx)

	got, err := is.GetInstance(ctx, id)
	if err != nil {
		t.Fatalf("读取实例失败: %v", err)
	}
	if got.State != instances.StateUnknown || got.Callable {
		t.Fatalf("过期远端实例应降级为 unknown 且不可调用: %+v", got)
	}
}
