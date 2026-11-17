package discovery

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"operations-platform/internal/plugin/registry"
	"operations-platform/internal/store"
)

func testLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// writePlugin 在 dir/name 下写一份最小可用清单与入口文件。
func writePlugin(t *testing.T, dir, name, version string) {
	t.Helper()
	root := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Join(root, "bin"), 0o755); err != nil {
		t.Fatalf("创建插件目录失败: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "bin", "entry"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("写入入口失败: %v", err)
	}
	// 清单契约要求 input_schema_ref / output_schema_ref 非空且文件存在。
	if err := os.MkdirAll(filepath.Join(root, "schemas"), 0o755); err != nil {
		t.Fatalf("创建 schema 目录失败: %v", err)
	}
	for _, f := range []string{"in.json", "out.json"} {
		if err := os.WriteFile(filepath.Join(root, "schemas", f), []byte(`{"type":"object"}`), 0o600); err != nil {
			t.Fatalf("写入 schema 失败: %v", err)
		}
	}
	manifest := "identity:\n" +
		"  id: " + name + "\n" +
		"  display_name: 测试插件\n" +
		"  description: 用于发现测试\n" +
		"version:\n" +
		"  plugin: " + version + "\n" +
		"  contract: \">=1.0 <2.0\"\n" +
		"form: command\n" +
		"entry:\n" +
		"  executable: bin/entry\n" +
		"capabilities:\n" +
		"  - id: " + name + ".noop\n" +
		"    description: 空能力\n" +
		"    input_schema_ref: schemas/in.json\n" +
		"    output_schema_ref: schemas/out.json\n" +
		"    idempotent: true\n" +
		"    long_running: false\n" +
		"    supports_cancel: false\n" +
		"    concurrency: parallel\n" +
		"    sensitive: false\n"
	if err := os.WriteFile(filepath.Join(root, "plugin.yaml"), []byte(manifest), 0o600); err != nil {
		t.Fatalf("写入清单失败: %v", err)
	}
}

func newFixture(t *testing.T) (*Scanner, *registry.Registry, context.Context, string) {
	t.Helper()
	ctx := context.Background()
	dataDir := t.TempDir()
	if err := store.InitDataDir(ctx, testLogger(), dataDir, store.NoopFilesystemChecker{}); err != nil {
		t.Fatalf("初始化数据目录失败: %v", err)
	}
	st, err := store.Open(ctx, testLogger(), dataDir)
	if err != nil {
		t.Fatalf("打开存储失败: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	reg := registry.New(st)
	pluginDir := t.TempDir()
	writePlugin(t, pluginDir, "demo-echo", "1.0.0")
	writePlugin(t, pluginDir, "demo-other", "1.0.0")
	// 故意违规的目录：缺少 plugin.yaml。
	if err := os.MkdirAll(filepath.Join(pluginDir, "broken"), 0o755); err != nil {
		t.Fatalf("创建反例目录失败: %v", err)
	}
	return New(pluginDir, reg, testLogger()), reg, ctx, pluginDir
}

func TestScanIsIdempotent(t *testing.T) {
	scanner, reg, ctx, _ := newFixture(t)

	first, err := scanner.Scan(ctx)
	if err != nil {
		t.Fatalf("首次扫描失败: %v", err)
	}
	if first.Inserted != 2 || first.Rejected != 1 {
		t.Fatalf("首次扫描 inserted=%d rejected=%d，期望 2/1", first.Inserted, first.Rejected)
	}
	if len(first.Warnings) == 0 {
		t.Fatal("反例目录应产生 warnings")
	}

	second, err := scanner.Scan(ctx)
	if err != nil {
		t.Fatalf("二次扫描失败: %v", err)
	}
	if second.Inserted != 0 || second.Updated != 0 || second.Unchanged != 2 {
		t.Fatalf("二次扫描 inserted=%d updated=%d unchanged=%d，期望 0/0/2",
			second.Inserted, second.Updated, second.Unchanged)
	}

	list, err := reg.List(ctx)
	if err != nil {
		t.Fatalf("列出插件失败: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("注册表应有 2 条记录，实际 %d", len(list))
	}
	for _, p := range list {
		if p.State != registry.StateDisabled {
			t.Fatalf("安装后默认应为 disabled，%s 为 %s", p.ID, p.State)
		}
	}
}

func TestScanKeepsEnabledStateAfterManifestChange(t *testing.T) {
	scanner, reg, ctx, pluginDir := newFixture(t)
	if _, err := scanner.Scan(ctx); err != nil {
		t.Fatalf("首次扫描失败: %v", err)
	}
	if err := reg.SetState(ctx, "demo-echo", registry.StateEnabled); err != nil {
		t.Fatalf("启用插件失败: %v", err)
	}

	// 升级版本：摘要变化应被识别为 updated，但不得自动改变启用状态。
	writePlugin(t, pluginDir, "demo-echo", "1.0.1")
	rep, err := scanner.Scan(ctx)
	if err != nil {
		t.Fatalf("升级后扫描失败: %v", err)
	}
	if rep.Updated != 1 || rep.Unchanged != 1 {
		t.Fatalf("升级扫描 updated=%d unchanged=%d，期望 1/1", rep.Updated, rep.Unchanged)
	}
	p, err := reg.Get(ctx, "demo-echo")
	if err != nil {
		t.Fatalf("读取插件失败: %v", err)
	}
	if p.Version != "1.0.1" {
		t.Fatalf("版本未更新，实际 %s", p.Version)
	}
	if p.State != registry.StateEnabled {
		t.Fatalf("清单变更不应改变启用状态，实际 %s", p.State)
	}
}

func TestScanRejectsIncompatibleContract(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()
	if err := store.InitDataDir(ctx, testLogger(), dataDir, store.NoopFilesystemChecker{}); err != nil {
		t.Fatalf("初始化数据目录失败: %v", err)
	}
	st, err := store.Open(ctx, testLogger(), dataDir)
	if err != nil {
		t.Fatalf("打开存储失败: %v", err)
	}
	defer st.Close()
	dir := t.TempDir()
	root := filepath.Join(dir, "future-plugin")
	if err := os.MkdirAll(filepath.Join(root, "bin"), 0o755); err != nil {
		t.Fatalf("创建插件目录失败: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "bin", "entry"), nil, 0o755); err != nil {
		t.Fatalf("写入入口失败: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(root, "schemas"), 0o755); err != nil {
		t.Fatalf("创建 schema 目录失败: %v", err)
	}
	for _, f := range []string{"in.json", "out.json"} {
		if err := os.WriteFile(filepath.Join(root, "schemas", f), []byte(`{"type":"object"}`), 0o600); err != nil {
			t.Fatalf("写入 schema 失败: %v", err)
		}
	}
	bad := "identity:\n  id: future-plugin\n  display_name: 未来插件\n  description: 契约不兼容\n" +
		"version:\n  plugin: 1.0.0\n  contract: \">=2.0\"\nform: command\nentry:\n  executable: bin/entry\n" +
		"capabilities:\n  - id: future-plugin.noop\n    description: 空\n" +
		"    input_schema_ref: schemas/in.json\n    output_schema_ref: schemas/out.json\n" +
		"    idempotent: true\n    long_running: false\n" +
		"    supports_cancel: false\n    concurrency: parallel\n    sensitive: false\n"
	if err := os.WriteFile(filepath.Join(root, "plugin.yaml"), []byte(bad), 0o600); err != nil {
		t.Fatalf("写入清单失败: %v", err)
	}

	reg := registry.New(st)
	rep, err := New(dir, reg, testLogger()).Scan(ctx)
	if err != nil {
		t.Fatalf("扫描失败: %v", err)
	}
	if rep.Rejected != 1 || rep.Inserted != 0 {
		t.Fatalf("不兼容契约应被拒绝，rejected=%d inserted=%d", rep.Rejected, rep.Inserted)
	}
	list, err := reg.List(ctx)
	if err != nil {
		t.Fatalf("列出插件失败: %v", err)
	}
	if len(list) != 0 {
		t.Fatalf("被拒绝的插件不应登记，实际 %d 条", len(list))
	}
}
