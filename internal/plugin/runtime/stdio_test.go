package runtime

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestHelperProcess 让测试二进制自己扮演插件进程。
// 通过环境变量 HELPER_MODE 选择行为，避免为每个场景编译独立二进制。
func TestHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_HELPER_PROCESS") != "1" {
		return
	}
	mode := os.Getenv("HELPER_MODE")

	// 必须读完 stdin，模拟契约要求的「读至 EOF」。
	var req map[string]any
	_ = json.NewDecoder(os.Stdin).Decode(&req)

	switch mode {
	case "ok":
		_, _ = os.Stdout.WriteString(`{"status":"ok","output":{"echo":"hello"},"plugin_version":"0.1.0"}` + "\n")
		os.Exit(0)
	case "stderr":
		_, _ = os.Stderr.WriteString("log line that must not reach stdout\n")
		_, _ = os.Stdout.WriteString(`{"status":"ok","output":{}}` + "\n")
		os.Exit(0)
	case "err":
		_, _ = os.Stdout.WriteString(`{"status":"error","error":{"code":"input_invalid","message":"字段 text 缺失"}}` + "\n")
		os.Exit(2)
	case "silent":
		os.Exit(0)
	case "multiline":
		_, _ = os.Stdout.WriteString("{\"status\":\"ok\",\"output\":{}}\nlog\n")
		os.Exit(0)
	case "nonjson":
		_, _ = os.Stdout.WriteString("not json\n")
		os.Exit(0)
	case "exit-mismatch":
		_, _ = os.Stdout.WriteString(`{"status":"error","error":{"code":"input_invalid","message":"x"}}` + "\n")
		os.Exit(0)
	case "inconsistent":
		_, _ = os.Stdout.WriteString(`{"status":"error","error":{"code":"input_invalid","message":"x"}}` + "\n")
		os.Exit(5)
	case "big":
		payload := strings.Repeat("x", 500)
		out, _ := json.Marshal(map[string]any{"status": "ok", "output": map[string]string{"data": payload}})
		_, _ = os.Stdout.Write(append(out, '\n'))
		os.Exit(0)
	case "hang":
		time.Sleep(5 * time.Second)
		os.Exit(0)
	}
	os.Exit(99)
}

func helperSpec(t *testing.T, mode string, deadline time.Duration, maxBytes int64) InvokeSpec {
	t.Helper()
	exe := os.Args[0]
	return InvokeSpec{
		Root:           filepath.Dir(exe),
		Executable:     filepath.Base(exe),
		Capability:     "demo-echo.echo",
		Input:          json.RawMessage(`{"text":"hello"}`),
		Subject:        "user:test",
		InvocationID:   "inv-1",
		Deadline:       deadline,
		MaxResultBytes: maxBytes,
		Env:            append(os.Environ(), "GO_WANT_HELPER_PROCESS=1", "HELPER_MODE="+mode),
		Args:           []string{"-test.run=TestHelperProcess"},
	}
}

func TestInvokeOK(t *testing.T) {
	res := Invoke(context.Background(), helperSpec(t, "ok", 5*time.Second, 1<<20))
	if res.Status != "ok" {
		t.Fatalf("status = %q, error = %+v", res.Status, res.Error)
	}
	if !strings.Contains(string(res.Output), "hello") {
		t.Errorf("output = %s", res.Output)
	}
	if res.PluginVersion != "0.1.0" {
		t.Errorf("plugin_version = %q", res.PluginVersion)
	}
}

func TestInvokePluginError(t *testing.T) {
	res := Invoke(context.Background(), helperSpec(t, "err", 5*time.Second, 1<<20))
	if res.Status != "error" || res.Error == nil || res.Error.Code != CodeInputInvalid {
		t.Fatalf("期望 input_invalid，实际: %+v", res)
	}
	if res.ExitCode != 2 {
		t.Errorf("exit code = %d", res.ExitCode)
	}
}

func TestInvokeSilentExitIsInvalidResponse(t *testing.T) {
	res := Invoke(context.Background(), helperSpec(t, "silent", 5*time.Second, 1<<20))
	if res.Error == nil || res.Error.Code != CodeInvalidResponse {
		t.Fatalf("期望 invalid_response，实际: %+v", res.Error)
	}
}

func TestInvokeMultilineIsProtocolInvalid(t *testing.T) {
	res := Invoke(context.Background(), helperSpec(t, "multiline", 5*time.Second, 1<<20))
	if res.Error == nil || res.Error.Code != CodeProtocolInvalid {
		t.Fatalf("期望 protocol_invalid，实际: %+v", res.Error)
	}
}

func TestInvokeNonJSONIsInvalidResponse(t *testing.T) {
	res := Invoke(context.Background(), helperSpec(t, "nonjson", 5*time.Second, 1<<20))
	if res.Error == nil || res.Error.Code != CodeInvalidResponse {
		t.Fatalf("期望 invalid_response，实际: %+v", res.Error)
	}
}

func TestInvokeExitCodeMismatch(t *testing.T) {
	res := Invoke(context.Background(), helperSpec(t, "exit-mismatch", 5*time.Second, 1<<20))
	if res.Error == nil || res.Error.Code != CodeProtocolInvalid {
		t.Fatalf("期望 protocol_invalid，实际: %+v", res.Error)
	}
}

func TestInvokeInconsistentCategoryAndExit(t *testing.T) {
	res := Invoke(context.Background(), helperSpec(t, "inconsistent", 5*time.Second, 1<<20))
	if res.Error == nil || res.Error.Code != CodeProtocolInvalid {
		t.Fatalf("期望 protocol_invalid，实际: %+v", res.Error)
	}
}

func TestInvokeResultTooLarge(t *testing.T) {
	res := Invoke(context.Background(), helperSpec(t, "big", 5*time.Second, 100))
	if res.Error == nil || res.Error.Code != CodeResultTooLarge {
		t.Fatalf("期望 result_too_large，实际: %+v", res.Error)
	}
}

func TestInvokeTimeout(t *testing.T) {
	start := time.Now()
	res := Invoke(context.Background(), helperSpec(t, "hang", 150*time.Millisecond, 1<<20))
	if res.Error == nil || res.Error.Code != CodeTimeout {
		t.Fatalf("期望 timeout，实际: %+v", res.Error)
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Errorf("超时终止耗时 %s，疑似未终止进程", elapsed)
	}
}

func TestInvokeStderrNotInResult(t *testing.T) {
	res := Invoke(context.Background(), helperSpec(t, "stderr", 5*time.Second, 1<<20))
	if res.Status != "ok" {
		t.Fatalf("stderr 不应影响结果: %+v", res.Error)
	}
	if strings.Contains(string(res.Output), "log line") {
		t.Error("stderr 内容混入了结果")
	}
	if !strings.Contains(res.StderrTail, "log line") {
		t.Error("stderr 摘要应单独保留")
	}
}

func TestExitCodeCategory(t *testing.T) {
	cases := map[int]string{
		0: "", 1: CodeInternalError, 2: CodeInputInvalid, 3: CodeCapabilityUnknown,
		4: CodePermissionDenied, 5: CodeTimeout, 6: CodeResultTooLarge,
		7: CodeDependencyUnavailable, 8: CodeProtocolInvalid, 9: CodeInternalError,
	}
	for code, want := range cases {
		if got := ExitCodeCategory(code); got != want {
			t.Errorf("ExitCodeCategory(%d) = %q，期望 %q", code, got, want)
		}
	}
}
