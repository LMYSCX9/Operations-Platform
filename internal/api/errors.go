package api

import (
	"errors"
	"log/slog"
	"net/http"

	"operations-platform/internal/host"
	"operations-platform/internal/plugin"
	"operations-platform/internal/plugin/manifest"
	"operations-platform/internal/plugin/runtime"
	"operations-platform/internal/scheduler"
)

// 统一错误响应：{"error":{"code","message","details"}}。
type errorEnvelope struct {
	Error errorBody `json:"error"`
}

type errorBody struct {
	Code    string         `json:"code"`
	Message string         `json:"message"`
	Details map[string]any `json:"details,omitempty"`
}

func writeError(w http.ResponseWriter, status int, code, message string, details map[string]any) {
	writeJSON(w, status, errorEnvelope{Error: errorBody{Code: code, Message: message, Details: details}})
}

// statusForCode 是错误类别到 HTTP 状态码的唯一映射（spec §5.10）。
func statusForCode(code string) int {
	switch code {
	case runtime.CodeInputInvalid, runtime.CodeVersionIncompatible:
		return http.StatusBadRequest
	case runtime.CodePermissionDenied:
		return http.StatusForbidden
	case runtime.CodeCapabilityUnknown, "not_found":
		return http.StatusNotFound
	case runtime.CodeNotReady:
		return http.StatusConflict
	case runtime.CodeResultTooLarge:
		return http.StatusRequestEntityTooLarge
	case runtime.CodeTimeout:
		return http.StatusGatewayTimeout
	case runtime.CodeDependencyUnavailable:
		return http.StatusBadGateway
	default:
		return http.StatusInternalServerError
	}
}

// statusForHostCode 是主机与下发错误码到 HTTP 状态的映射。
func statusForHostCode(code string) int {
	switch code {
	case host.CodeInputInvalid, host.CodeUnsupportedOS:
		return http.StatusBadRequest
	case host.CodePermissionDenied:
		return http.StatusForbidden
	case host.CodeNotFound:
		return http.StatusNotFound
	case host.CodeConflict, host.CodeFingerprintMismatch:
		return http.StatusConflict
	case host.CodeTimeout:
		return http.StatusGatewayTimeout
	case host.CodeAuthFailed, host.CodeUnreachable, host.CodeVerifyFailed,
		host.CodeInstallFailed, host.CodeDependencyUnavailable:
		return http.StatusBadGateway
	default:
		return http.StatusInternalServerError
	}
}

// asHostError 是 errors.As 的类型断言封装，避免在 handler 里散落类型逻辑。
func asHostError(err error, target **host.Error) bool {
	return errors.As(err, target)
}

// statusForSchedulerCode 是定时任务错误码到 HTTP 状态的映射。
func statusForSchedulerCode(code string) int {
	switch code {
	case scheduler.CodeInputInvalid, scheduler.CodeDependency:
		return http.StatusBadRequest
	case scheduler.CodePermissionDenied:
		return http.StatusForbidden
	case scheduler.CodeNotFound:
		return http.StatusNotFound
	case scheduler.CodeConflict:
		return http.StatusConflict
	default:
		return http.StatusInternalServerError
	}
}

// writeServiceError 把服务层错误翻译为 HTTP 响应，避免把内部细节暴露给调用方。
func writeServiceError(w http.ResponseWriter, logger *slog.Logger, err error) {
	var se *scheduler.Error
	if errors.As(err, &se) {
		writeError(w, statusForSchedulerCode(se.Code), se.Code, se.Message, se.Details)
		return
	}
	var he *host.Error
	if errors.As(err, &he) {
		writeError(w, statusForHostCode(he.Code), he.Code, he.Message, he.Details)
		return
	}
	var pe *plugin.Error
	if errors.As(err, &pe) {
		writeError(w, statusForCode(pe.Code), pe.Code, pe.Message, pe.Details)
		return
	}
	var fe manifest.FieldErrors
	if errors.As(err, &fe) {
		writeError(w, http.StatusBadRequest, runtime.CodeInputInvalid, "插件清单校验失败", map[string]any{"validation": fe})
		return
	}
	logger.Error("请求处理失败", "error", err)
	writeError(w, http.StatusInternalServerError, runtime.CodeInternalError, "服务内部错误", nil)
}
