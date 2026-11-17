// Package runtime 实现命令式插件的 stdio 调用链路（plugin-spec/02、05）。
//
// 该包只关心「如何安全地跑一个插件进程并解析一行结果」，不涉及注册表与授权。
package runtime

// 错误类别枚举，取自 plugin-spec/05 §2；插件不得自造类别。
const (
	CodeInputInvalid          = "input_invalid"
	CodeCapabilityUnknown     = "capability_unknown"
	CodePermissionDenied      = "permission_denied"
	CodeNotReady              = "not_ready"
	CodeTimeout               = "timeout"
	CodeCancelled             = "cancelled"
	CodeResultTooLarge        = "result_too_large"
	CodeDependencyUnavailable = "dependency_unavailable"
	CodeInternalError         = "internal_error"
	CodeProtocolInvalid       = "protocol_invalid"
	CodeInvalidResponse       = "invalid_response"
	CodeVersionIncompatible   = "version_incompatible"
)

// ValidCode 判断错误类别是否在契约枚举内。
func ValidCode(code string) bool {
	switch code {
	case CodeInputInvalid, CodeCapabilityUnknown, CodePermissionDenied, CodeNotReady,
		CodeTimeout, CodeCancelled, CodeResultTooLarge, CodeDependencyUnavailable,
		CodeInternalError, CodeProtocolInvalid, CodeInvalidResponse, CodeVersionIncompatible:
		return true
	}
	return false
}

// ExitCodeCategory 把命令式插件退出码映射为错误类别（plugin-spec/05 §3）。
// 0 表示成功，返回空字符串；未列出的退出码按 internal_error 处理。
func ExitCodeCategory(exitCode int) string {
	switch exitCode {
	case 0:
		return ""
	case 1:
		return CodeInternalError
	case 2:
		return CodeInputInvalid
	case 3:
		return CodeCapabilityUnknown
	case 4:
		return CodePermissionDenied
	case 5:
		return CodeTimeout
	case 6:
		return CodeResultTooLarge
	case 7:
		return CodeDependencyUnavailable
	case 8:
		return CodeProtocolInvalid
	default:
		return CodeInternalError
	}
}

// exitConsistent 判定响应错误类别与退出码语义是否一致。
// 退出码 8 同时覆盖 protocol_invalid 与 invalid_response（契约原文如此）。
func exitConsistent(code string, exitCode int) bool {
	if exitCode == 8 {
		return code == CodeProtocolInvalid || code == CodeInvalidResponse
	}
	return ExitCodeCategory(exitCode) == code
}
