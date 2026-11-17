package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// 默认值与上限取值属于实现基线（OQ-039）；可由 config 覆盖。
const (
	DefaultTimeout        = 30 * time.Second
	DefaultMaxResultBytes = 8 << 20
	// KillGrace 是发出中断信号后等待进程自行退出的宽限期（OQ-017 未定，取 2s）。
	KillGrace = 2 * time.Second
	// StderrTailBytes 限制回传/记录的 stderr 尾部体积，避免日志被插件刷爆。
	StderrTailBytes = 64 << 10
)

// InvokeSpec 是一次调用的静态参数与运行时预算。
type InvokeSpec struct {
	Root         string
	Executable   string
	Capability   string
	Input        json.RawMessage
	Subject      string
	InvocationID string
	// ContractVersion 是底座为该插件解析出的契约版本（M6 起由发现阶段写入）。
	ContractVersion string
	Deadline        time.Duration
	MaxResultBytes  int64
	// Env 为空时使用最小环境（仅 PATH）；测试可显式注入。
	Env []string
	// Args 为附加命令行参数；生产调用保持为空。
	Args []string
}

// HostError 是底座侧判定的结构化错误。
type HostError struct {
	Code      string         `json:"code"`
	Message   string         `json:"message"`
	Details   map[string]any `json:"details,omitempty"`
	Retryable bool           `json:"retryable,omitempty"`
}

func (e *HostError) Error() string { return e.Code + ": " + e.Message }

// Result 是调用结果；Status 为 ok 时 Output 必填，为 error 时 Error 必填。
type Result struct {
	Status        string          `json:"status"`
	Output        json.RawMessage `json:"output,omitempty"`
	PluginVersion string          `json:"plugin_version,omitempty"`
	Error         *HostError      `json:"error,omitempty"`
	ExitCode      int             `json:"-"`
	StderrTail    string          `json:"-"`
	Duration      time.Duration   `json:"-"`
}

type request struct {
	ContractVersion string          `json:"contract_version"`
	Capability      string          `json:"capability"`
	Input           json.RawMessage `json:"input"`
	Context         requestContext  `json:"context"`
}

type requestContext struct {
	InvocationID string `json:"invocation_id"`
	DeadlineMS   int64  `json:"deadline_ms"`
	Subject      string `json:"subject,omitempty"`
}

type response struct {
	Status        string          `json:"status"`
	Output        json.RawMessage `json:"output"`
	PluginVersion string          `json:"plugin_version"`
	Error         *responseError  `json:"error"`
}

type responseError struct {
	Code      string         `json:"code"`
	Message   string         `json:"message"`
	Details   map[string]any `json:"details"`
	Retryable bool           `json:"retryable"`
}

// Invoke 启动插件进程、写入一行请求、读取一行响应，并按契约判定结果。
// 任何协议不一致都会转换为 error 结果，而不是向上抛裸错误。
func Invoke(ctx context.Context, spec InvokeSpec) Result {
	start := time.Now()
	res := func(status string) Result { return Result{Status: status, Duration: time.Since(start)} }

	if spec.Deadline <= 0 {
		spec.Deadline = DefaultTimeout
	}
	if spec.MaxResultBytes <= 0 {
		spec.MaxResultBytes = DefaultMaxResultBytes
	}
	exe, err := resolveExecutable(spec.Root, spec.Executable)
	if err != nil {
		r := res("error")
		r.Error = &HostError{Code: CodeInternalError, Message: err.Error()}
		return r
	}

	input := spec.Input
	if len(input) == 0 {
		input = json.RawMessage("{}")
	}
	contractVer := spec.ContractVersion
	if contractVer == "" {
		contractVer = "1.0"
	}
	reqBody, err := json.Marshal(request{
		ContractVersion: contractVer,
		Capability:      spec.Capability,
		Input:           input,
		Context: requestContext{
			InvocationID: spec.InvocationID,
			DeadlineMS:   spec.Deadline.Milliseconds(),
			Subject:      spec.Subject,
		},
	})
	if err != nil {
		r := res("error")
		r.Error = &HostError{Code: CodeInputInvalid, Message: "输入无法序列化为 JSON: " + err.Error()}
		return r
	}

	ctx, cancel := context.WithTimeout(ctx, spec.Deadline)
	defer cancel()

	cmd := exec.Command(exe, spec.Args...)
	cmd.Dir = spec.Root
	cmd.Stdin = bytes.NewReader(append(reqBody, '\n'))
	stdout := newLimitBuffer(spec.MaxResultBytes)
	stderr := newTailBuffer(StderrTailBytes)
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	setProcessGroup(cmd)
	// 插件不需要交互式终端，也不应继承宿主的敏感环境变量；显式清空后只保留 PATH。
	if spec.Env != nil {
		cmd.Env = spec.Env
	} else {
		cmd.Env = minimalEnv()
	}

	if err := cmd.Start(); err != nil {
		r := res("error")
		r.Error = &HostError{Code: CodeInternalError, Message: "启动插件失败: " + err.Error()}
		return r
	}

	waitCh := make(chan error, 1)
	go func() { waitCh <- cmd.Wait() }()

	var waitErr error
	timedOut, cancelled := false, false
	select {
	case waitErr = <-waitCh:
	case <-ctx.Done():
		timedOut = errors.Is(ctx.Err(), context.DeadlineExceeded)
		cancelled = !timedOut
		// 先发中断信号，宽限期内自行退出；超时后强制终止整个进程组。
		_ = cmd.Process.Signal(os.Interrupt)
		select {
		case waitErr = <-waitCh:
		case <-time.After(KillGrace):
			_ = killProcessGroup(cmd.Process)
			waitErr = <-waitCh
		}
	}

	out := res("error")
	out.ExitCode = exitCodeOf(waitErr)
	out.StderrTail = stderr.String()

	switch {
	case timedOut:
		out.Error = &HostError{
			Code:      CodeTimeout,
			Message:   fmt.Sprintf("插件调用超过 %s 未完成，已终止", spec.Deadline),
			Retryable: true,
		}
		return out
	case cancelled:
		out.Error = &HostError{Code: CodeCancelled, Message: "调用已取消"}
		return out
	case stdout.Truncated():
		out.Error = &HostError{
			Code:    CodeResultTooLarge,
			Message: fmt.Sprintf("stdout 超过 %d 字节上限，结果被拒绝", spec.MaxResultBytes),
			Details: map[string]any{"limit_bytes": spec.MaxResultBytes},
		}
		return out
	}

	parsed, perr := parseResponse(stdout.Bytes(), out.ExitCode)
	if perr != nil {
		out.Error = perr
		return out
	}
	out.Status = parsed.Status
	out.Output = parsed.Output
	out.PluginVersion = parsed.PluginVersion
	if parsed.Status == "error" {
		out.Error = &HostError{
			Code:      parsed.Error.Code,
			Message:   parsed.Error.Message,
			Details:   parsed.Error.Details,
			Retryable: parsed.Error.Retryable,
		}
	}
	return out
}

// parseResponse 按契约固定顺序判定：无输出、多行、JSON、status、退出码一致性。
func parseResponse(stdout []byte, exitCode int) (response, *HostError) {
	var resp response

	text := strings.TrimRight(string(stdout), "\r\n")
	if text == "" {
		return resp, &HostError{Code: CodeInvalidResponse, Message: "插件未输出结果"}
	}
	lines := strings.Split(text, "\n")
	if len(lines) != 1 {
		return resp, &HostError{
			Code:    CodeProtocolInvalid,
			Message: fmt.Sprintf("stdout 必须恰好一行结果，实际 %d 行", len(lines)),
		}
	}
	line := strings.TrimSpace(lines[0])
	if line == "" {
		return resp, &HostError{Code: CodeInvalidResponse, Message: "插件输出为空行"}
	}
	if err := json.Unmarshal([]byte(line), &resp); err != nil {
		return resp, &HostError{Code: CodeInvalidResponse, Message: "stdout 不是合法 JSON: " + err.Error()}
	}
	if resp.Status != "ok" && resp.Status != "error" {
		return resp, &HostError{
			Code:    CodeProtocolInvalid,
			Message: fmt.Sprintf("响应 status 必须为 ok 或 error，实际 %q", resp.Status),
		}
	}

	if resp.Status == "ok" {
		if exitCode != 0 {
			return resp, &HostError{
				Code:    CodeProtocolInvalid,
				Message: fmt.Sprintf("响应为 ok 但退出码为 %d", exitCode),
			}
		}
		return resp, nil
	}

	if resp.Error == nil {
		return resp, &HostError{Code: CodeInvalidResponse, Message: "status=error 但缺少 error 对象"}
	}
	if resp.Error.Message == "" {
		return resp, &HostError{Code: CodeInvalidResponse, Message: "error.message 不得为空"}
	}
	if !ValidCode(resp.Error.Code) {
		return resp, &HostError{
			Code:    CodeInvalidResponse,
			Message: fmt.Sprintf("error.code %q 不在契约枚举内", resp.Error.Code),
		}
	}
	if exitCode == 0 {
		return resp, &HostError{
			Code:    CodeProtocolInvalid,
			Message: "响应为 error 但退出码为 0",
		}
	}
	if !exitConsistent(resp.Error.Code, exitCode) {
		return resp, &HostError{
			Code: CodeProtocolInvalid,
			Message: fmt.Sprintf("响应错误类别 %s 与退出码 %d 语义不一致",
				resp.Error.Code, exitCode),
		}
	}
	return resp, nil
}

func resolveExecutable(root, rel string) (string, error) {
	if rel == "" {
		return "", errors.New("插件未声明入口可执行文件")
	}
	full := filepath.Join(root, filepath.FromSlash(rel))
	info, err := os.Stat(full)
	if err == nil {
		if info.IsDir() {
			return "", fmt.Errorf("入口 %q 是目录", rel)
		}
		return full, nil
	}
	// Windows 下清单可能写成无扩展名路径；这是实现基线（OQ-039），仅做同名 .exe 回退。
	if filepath.Ext(full) == "" {
		exe := full + ".exe"
		if info, statErr := os.Stat(exe); statErr == nil && !info.IsDir() {
			return exe, nil
		}
	}
	return "", fmt.Errorf("入口可执行文件 %q 不存在", rel)
}

func exitCodeOf(err error) int {
	if err == nil {
		return 0
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode()
	}
	return -1
}

func minimalEnv() []string {
	if path := os.Getenv("PATH"); path != "" {
		return []string{"PATH=" + path}
	}
	return nil
}

// limitBuffer 收集 stdout，超过上限后继续消费但不再保存，保证子进程不会因管道写满而阻塞。
type limitBuffer struct {
	buf       bytes.Buffer
	limit     int64
	truncated bool
}

func newLimitBuffer(limit int64) *limitBuffer { return &limitBuffer{limit: limit} }

func (b *limitBuffer) Write(p []byte) (int, error) {
	remaining := b.limit - int64(b.buf.Len())
	if remaining <= 0 {
		b.truncated = true
		return len(p), nil
	}
	if int64(len(p)) > remaining {
		b.buf.Write(p[:remaining])
		b.truncated = true
		return len(p), nil
	}
	b.buf.Write(p)
	return len(p), nil
}

func (b *limitBuffer) Bytes() []byte   { return b.buf.Bytes() }
func (b *limitBuffer) Truncated() bool { return b.truncated }

// tailBuffer 只保留最后 limit 字节，用于 stderr 摘要。
type tailBuffer struct {
	limit int
	data  []byte
}

func newTailBuffer(limit int) *tailBuffer { return &tailBuffer{limit: limit} }

func (b *tailBuffer) Write(p []byte) (int, error) {
	b.data = append(b.data, p...)
	if len(b.data) > b.limit {
		b.data = b.data[len(b.data)-b.limit:]
	}
	return len(p), nil
}

func (b *tailBuffer) String() string { return string(b.data) }
