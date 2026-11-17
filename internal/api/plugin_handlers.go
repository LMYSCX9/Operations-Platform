package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"operations-platform/internal/plugin/runtime"
)

// listPlugins 返回插件摘要、最近一次扫描的告警与冲突。
func (h *handler) listPlugins(w http.ResponseWriter, r *http.Request) {
	plugins, err := h.plugins.List(r.Context(), subjectOf(r))
	if err != nil {
		writeServiceError(w, h.logger, err)
		return
	}
	report := h.plugins.LastScanReport()
	writeJSON(w, http.StatusOK, map[string]any{
		"plugins":   plugins,
		"warnings":  report.Warnings,
		"conflicts": report.Conflicts,
	})
}

func (h *handler) getPlugin(w http.ResponseWriter, r *http.Request) {
	detail, err := h.plugins.Get(r.Context(), subjectOf(r), r.PathValue("id"))
	if err != nil {
		writeServiceError(w, h.logger, err)
		return
	}
	writeJSON(w, http.StatusOK, detail)
}

func (h *handler) scanPlugins(w http.ResponseWriter, r *http.Request) {
	rep, err := h.plugins.Scan(r.Context())
	if err != nil {
		writeServiceError(w, h.logger, err)
		return
	}
	writeJSON(w, http.StatusOK, rep)
}

func (h *handler) enablePlugin(w http.ResponseWriter, r *http.Request) {
	p, err := h.plugins.Enable(r.Context(), subjectOf(r), r.PathValue("id"))
	if err != nil {
		writeServiceError(w, h.logger, err)
		return
	}
	writeJSON(w, http.StatusOK, p)
}

func (h *handler) disablePlugin(w http.ResponseWriter, r *http.Request) {
	p, err := h.plugins.Disable(r.Context(), subjectOf(r), r.PathValue("id"))
	if err != nil {
		writeServiceError(w, h.logger, err)
		return
	}
	writeJSON(w, http.StatusOK, p)
}

// invokePayload 是调用请求体；input 缺失时按空对象处理。
type invokePayload struct {
	Input json.RawMessage `json:"input"`
}

func (h *handler) invokeCapability(w http.ResponseWriter, r *http.Request) {
	var payload invokePayload
	if err := decodeJSONBody(r, &payload); err != nil {
		writeError(w, http.StatusBadRequest, runtime.CodeInputInvalid, err.Error(), nil)
		return
	}
	res, err := h.plugins.Invoke(r.Context(), subjectOf(r),
		r.PathValue("id"), r.PathValue("capability"), payload.Input, "api")
	if err != nil {
		writeServiceError(w, h.logger, err)
		return
	}
	// 插件业务错误也返回结构化结果：HTTP 状态码按错误类别映射，body 保留 invocation_id 与 output。
	status := http.StatusOK
	if res.Status != "ok" && res.Error != nil {
		status = statusForCode(res.Error.Code)
	}
	writeJSON(w, status, res)
}

// decodeJSONBody 限制请求体体积并拒绝未知尾随内容。
func decodeJSONBody(r *http.Request, dst any) error {
	const maxBody = 1 << 20
	dec := json.NewDecoder(io.LimitReader(r.Body, maxBody))
	if err := dec.Decode(dst); err != nil {
		if errors.Is(err, io.EOF) {
			// 允许空 body：调用参数全部可选时等价于 {}。
			return nil
		}
		return errors.New("请求体必须是合法 JSON: " + err.Error())
	}
	return nil
}
