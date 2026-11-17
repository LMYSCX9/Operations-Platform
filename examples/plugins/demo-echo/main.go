// 示例插件 demo-echo：演示命令式插件的最小实现。
//
// 协议约束（plugin-spec/02、05）：
//   - stdin 读取一行 JSON 请求；
//   - stdout 只输出一行 JSON 响应，任何日志写 stderr；
//   - 退出码与 error.code 必须语义一致。
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
)

type request struct {
	ContractVersion string          `json:"contract_version"`
	Capability      string          `json:"capability"`
	Input           json.RawMessage `json:"input"`
	Context         struct {
		InvocationID string `json:"invocation_id"`
		DeadlineMS   int64  `json:"deadline_ms"`
		Subject      string `json:"subject"`
	} `json:"context"`
}

type response struct {
	Status        string         `json:"status"`
	Output        any            `json:"output,omitempty"`
	PluginVersion string         `json:"plugin_version"`
	Error         *responseError `json:"error,omitempty"`
}

type responseError struct {
	Code      string         `json:"code"`
	Message   string         `json:"message"`
	Details   map[string]any `json:"details,omitempty"`
	Retryable bool           `json:"retryable"`
}

const pluginVersion = "1.0.0"

// 退出码与错误类别一一对应（plugin-spec/05 §3）。
var exitCodes = map[string]int{
	"internal_error":         1,
	"input_invalid":          2,
	"capability_unknown":     3,
	"permission_denied":      4,
	"timeout":                5,
	"result_too_large":       6,
	"dependency_unavailable": 7,
	"protocol_invalid":       8,
}

func main() {
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	if !scanner.Scan() {
		emit(response{
			Status:        "error",
			PluginVersion: pluginVersion,
			Error:         &responseError{Code: "protocol_invalid", Message: "未收到请求"},
		}, 8)
		return
	}

	var req request
	if err := json.Unmarshal(scanner.Bytes(), &req); err != nil {
		emit(response{
			Status:        "error",
			PluginVersion: pluginVersion,
			Error:         &responseError{Code: "input_invalid", Message: "请求不是合法 JSON: " + err.Error()},
		}, 2)
		return
	}

	switch req.Capability {
	case "demo-echo.echo":
		echo(req)
	case "demo-echo.fail":
		fail(req)
	default:
		emit(response{
			Status:        "error",
			PluginVersion: pluginVersion,
			Error:         &responseError{Code: "capability_unknown", Message: "不支持的能力 " + req.Capability},
		}, 3)
	}
}

func echo(req request) {
	var in struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(req.Input, &in); err != nil {
		emit(response{
			Status:        "error",
			PluginVersion: pluginVersion,
			Error:         &responseError{Code: "input_invalid", Message: "input 解析失败: " + err.Error()},
		}, 2)
		return
	}
	if in.Text == "" {
		emit(response{
			Status:        "error",
			PluginVersion: pluginVersion,
			Error: &responseError{
				Code:    "input_invalid",
				Message: "字段 text 缺失或为空（必填）",
				Details: map[string]any{"field": "text", "reason": "required"},
			},
		}, 2)
		return
	}
	// 日志只走 stderr，避免污染结果行。
	fmt.Fprintf(os.Stderr, "demo-echo.echo invocation=%s\n", req.Context.InvocationID)
	emit(response{
		Status:        "ok",
		PluginVersion: pluginVersion,
		Output:        map[string]string{"echo": in.Text},
	}, 0)
}

func fail(req request) {
	var in struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	}
	_ = json.Unmarshal(req.Input, &in)
	if in.Code == "" {
		in.Code = "internal_error"
	}
	code, ok := exitCodes[in.Code]
	if !ok {
		emit(response{
			Status:        "error",
			PluginVersion: pluginVersion,
			Error:         &responseError{Code: "input_invalid", Message: "未知错误类别 " + in.Code},
		}, 2)
		return
	}
	if in.Message == "" {
		in.Message = "示例插件按请求返回 " + in.Code
	}
	emit(response{
		Status:        "error",
		PluginVersion: pluginVersion,
		Error:         &responseError{Code: in.Code, Message: in.Message, Retryable: in.Code == "timeout" || in.Code == "dependency_unavailable"},
	}, code)
}

func emit(resp response, exitCode int) {
	enc := json.NewEncoder(os.Stdout)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(resp); err != nil {
		fmt.Fprintln(os.Stderr, "输出响应失败:", err)
		os.Exit(1)
	}
	os.Exit(exitCode)
}
