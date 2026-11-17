// 命令式插件起步模板（plugin-spec/02 stdio 协议）。
//
// 协议约束：
//   - stdin 读取一行 JSON 请求；stdout 只输出一行 JSON 响应；日志一律走 stderr；
//   - 退出码与 error.code 必须同桶（contract.go 的 Exit* 常量，plugin-spec/05 §3）；
//   - 未知能力返回 capability_unknown；未识别的上下文字段忽略。
//
// 复制改造：改 plugin.yaml 的 identity/capabilities、本文件的 dispatch 与
// handler，删掉 greet 示例；错误与退出码只用 contract.go 已登记的类别。
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

const pluginVersion = "0.1.0"

func main() {
	scanner := bufio.NewScanner(os.Stdin)
	// 上限给足余量；底座另有结果体积上限（OQ-017），超限报 result_too_large。
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	if !scanner.Scan() {
		emitError(CodeProtocolInvalid, "未收到请求", nil, false, ExitProtocolInvalid)
		return
	}

	var req request
	if err := json.Unmarshal(scanner.Bytes(), &req); err != nil {
		emitError(CodeInputInvalid, "请求不是合法 JSON: "+err.Error(), nil, false, ExitInputInvalid)
		return
	}

	switch req.Capability {
	case "starter-command.greet":
		greet(req)
	default:
		emitError(CodeCapabilityUnknown, "不支持的能力 "+req.Capability, nil, false, ExitCapabilityUnknown)
	}
}

func greet(req request) {
	var in struct {
		Name     string `json:"name"`
		Greeting string `json:"greeting"`
	}
	if err := json.Unmarshal(req.Input, &in); err != nil {
		emitError(CodeInputInvalid, "input 解析失败: "+err.Error(), nil, false, ExitInputInvalid)
		return
	}
	if in.Name == "" {
		emitError(CodeInputInvalid, "字段 name 缺失或为空（必填）",
			map[string]any{"field": "name", "reason": "required"}, false, ExitInputInvalid)
		return
	}
	if in.Greeting == "" {
		in.Greeting = "你好"
	}
	// 日志只走 stderr，避免污染结果行；不得输出凭据与敏感输入。
	fmt.Fprintf(os.Stderr, "greet invocation=%s subject_present=%t\n", req.Context.InvocationID, req.Context.Subject != "")
	emit(response{
		Status:        StatusOK,
		PluginVersion: pluginVersion,
		Output:        map[string]string{"message": in.Greeting + "，" + in.Name},
	}, ExitOK)
}

func emit(resp response, exitCode int) {
	enc := json.NewEncoder(os.Stdout)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(resp); err != nil {
		fmt.Fprintln(os.Stderr, "输出响应失败:", err)
		os.Exit(ExitInternalError)
	}
	os.Exit(exitCode)
}

func emitError(code, message string, details map[string]any, retryable bool, exitCode int) {
	emit(response{
		Status:        StatusError,
		PluginVersion: pluginVersion,
		Error:         &responseError{Code: code, Message: message, Details: details, Retryable: retryable},
	}, exitCode)
}
