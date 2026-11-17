package manifest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const validManifest = `
identity:
  id: demo-echo
  display_name: 示例回显插件
  description: 回显输入，用于验证插件运行时闭环
version:
  plugin: 0.1.0
  contract: ">=1.0 <2.0"
form: command
entry:
  executable: bin/demo-echo
capabilities:
  - id: demo-echo.echo
    description: 原样返回输入
    input_schema_ref: schemas/echo.input.json
    output_schema_ref: schemas/echo.output.json
    idempotent: true
    long_running: false
    suggested_timeout_ms: 3000
    supports_cancel: false
    concurrency: parallel
    sensitive: false
permissions:
  - category: data_dir
    actions: [read, write]
runtime:
  max_result_bytes: 1048576
metadata:
  license: MIT
`

func writePlugin(t *testing.T, manifestText string, files ...string) string {
	t.Helper()
	root := t.TempDir()
	if manifestText != "" {
		if err := os.WriteFile(filepath.Join(root, FileName), []byte(manifestText), 0o600); err != nil {
			t.Fatalf("写入清单失败: %v", err)
		}
	}
	for _, f := range files {
		full := filepath.Join(root, filepath.FromSlash(f))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatalf("创建目录失败: %v", err)
		}
		if err := os.WriteFile(full, []byte("{}"), 0o755); err != nil {
			t.Fatalf("写入文件 %s 失败: %v", f, err)
		}
	}
	return root
}

func standardFiles() []string {
	return []string{
		"bin/demo-echo",
		"schemas/echo.input.json",
		"schemas/echo.output.json",
	}
}

func TestLoadValidManifest(t *testing.T) {
	root := writePlugin(t, validManifest, standardFiles()...)
	m, errs, err := Load(root)
	if err != nil {
		t.Fatalf("Load 返回环境错误: %v", err)
	}
	if len(errs) > 0 {
		t.Fatalf("合法清单不应报错，实际: %v", errs)
	}
	if m.Identity.ID != "demo-echo" {
		t.Errorf("identity.id = %q", m.Identity.ID)
	}
	if len(m.Capabilities) != 1 || m.Capabilities[0].ID != "demo-echo.echo" {
		t.Errorf("能力解析错误: %+v", m.Capabilities)
	}
	if len(m.SHA256) != 64 {
		t.Errorf("摘要长度 = %d，期望 64", len(m.SHA256))
	}
}

func TestLoadRejectsInvalidIdentityID(t *testing.T) {
	text := strings.Replace(validManifest, "id: demo-echo\n  display_name", "id: Demo_Echo\n  display_name", 1)
	root := writePlugin(t, text, standardFiles()...)
	_, errs, err := Load(root)
	if err != nil {
		t.Fatalf("Load 环境错误: %v", err)
	}
	if !hasField(errs, "identity.id") {
		t.Fatalf("期望 identity.id 报错，实际: %v", errs)
	}
}

func TestLoadRejectsCapabilityWithoutPrefix(t *testing.T) {
	text := strings.Replace(validManifest, "id: demo-echo.echo", "id: echo", 1)
	root := writePlugin(t, text, standardFiles()...)
	_, errs, err := Load(root)
	if err != nil {
		t.Fatalf("Load 环境错误: %v", err)
	}
	if !hasField(errs, "capabilities[0].id") {
		t.Fatalf("期望能力前缀错误，实际: %v", errs)
	}
}

func TestLoadRejectsDependencies(t *testing.T) {
	text := validManifest + "\ndependencies:\n  - other-plugin\n"
	root := writePlugin(t, text, standardFiles()...)
	_, errs, err := Load(root)
	if err != nil {
		t.Fatalf("Load 环境错误: %v", err)
	}
	if !hasField(errs, "dependencies") {
		t.Fatalf("期望拒绝 dependencies，实际: %v", errs)
	}
}

func TestLoadRejectsCredentialField(t *testing.T) {
	text := strings.Replace(validManifest, "metadata:\n  license: MIT", "metadata:\n  license: MIT\n  password: hunter2", 1)
	root := writePlugin(t, text, standardFiles()...)
	_, errs, err := Load(root)
	if err != nil {
		t.Fatalf("Load 环境错误: %v", err)
	}
	if !hasField(errs, "metadata.password") {
		t.Fatalf("期望拒绝凭据字段，实际: %v", errs)
	}
}

func TestLoadRejectsMissingSchemaRef(t *testing.T) {
	root := writePlugin(t, validManifest, "bin/demo-echo", "schemas/echo.input.json")
	_, errs, err := Load(root)
	if err != nil {
		t.Fatalf("Load 环境错误: %v", err)
	}
	if !hasField(errs, "capabilities[0].output_schema_ref") {
		t.Fatalf("期望 output_schema_ref 报错，实际: %v", errs)
	}
}

func TestLoadRejectsPathTraversal(t *testing.T) {
	text := strings.Replace(validManifest, "input_schema_ref: schemas/echo.input.json", "input_schema_ref: ../outside.json", 1)
	root := writePlugin(t, text, standardFiles()...)
	_, errs, err := Load(root)
	if err != nil {
		t.Fatalf("Load 环境错误: %v", err)
	}
	if !hasField(errs, "capabilities[0].input_schema_ref") {
		t.Fatalf("期望路径越界报错，实际: %v", errs)
	}
}

func TestLoadRejectsFormEntryMismatch(t *testing.T) {
	text := strings.Replace(validManifest, "form: command\nentry:\n  executable: bin/demo-echo",
		"form: daemon\nentry:\n  executable: bin/demo-echo", 1)
	root := writePlugin(t, text, standardFiles()...)
	_, errs, err := Load(root)
	if err != nil {
		t.Fatalf("Load 环境错误: %v", err)
	}
	if !hasField(errs, "entry.executable") && !hasField(errs, "entry.launch") {
		t.Fatalf("期望 form 与 entry 不匹配报错，实际: %v", errs)
	}
}

func TestLoadRejectsMissingManifest(t *testing.T) {
	root := t.TempDir()
	_, errs, err := Load(root)
	if err != nil {
		t.Fatalf("Load 环境错误: %v", err)
	}
	if !hasField(errs, FileName) {
		t.Fatalf("期望清单不存在错误，实际: %v", errs)
	}
}

func hasField(errs FieldErrors, path string) bool {
	for _, e := range errs {
		if e.Path == path {
			return true
		}
	}
	return false
}

func TestParseRangeAndContract(t *testing.T) {
	cases := []struct {
		expr    string
		wantOK  bool
		wantErr bool
	}{
		{">=1.0 <2.0", true, false},
		{">=2.0 <3.0", false, false},
		{"<1.0", false, false},
		{"=1.1", true, false},
		{"=1.0", true, false},
		{">=1.0", true, false},
		{"", false, true},
		{"1.0", false, true},
		{"~1.0", false, true},
		{"=1.0 =1.1", false, true},
	}
	for _, tc := range cases {
		rng, err := ParseRange(tc.expr)
		if tc.wantErr {
			if err == nil {
				t.Errorf("ParseRange(%q) 期望语法错误", tc.expr)
			}
			continue
		}
		if err != nil {
			t.Errorf("ParseRange(%q) 意外错误: %v", tc.expr, err)
			continue
		}
		ok := CheckContract(rng) == nil
		if ok != tc.wantOK {
			t.Errorf("CheckContract(%q) = %v，期望 %v", tc.expr, ok, tc.wantOK)
		}
	}
}

func TestVersionCompare(t *testing.T) {
	v10, _ := ParseVersion("1.0")
	v11, _ := ParseVersion("1.1")
	v20, _ := ParseVersion("2.0")
	if v10.compare(v11) >= 0 || v11.compare(v20) >= 0 {
		t.Error("版本比较顺序错误")
	}
	if _, err := ParseVersion("1"); err == nil {
		t.Error("缺少次版本号应报错")
	}
}
