package api

import (
	"net/http"
	"strconv"
	"time"

	"operations-platform/internal/plugin/registry"
	"operations-platform/internal/plugin/runtime"
)

// listExecutions 支持按插件、能力、状态、来源与时间窗口过滤；默认最近 100 条。
func (h *handler) listExecutions(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	filter := registry.ExecutionFilter{
		PluginID:     q.Get("plugin_id"),
		CapabilityID: q.Get("capability_id"),
		Status:       q.Get("status"),
		Source:       q.Get("source"),
		TaskID:       q.Get("task_id"),
		InstanceID:   q.Get("instance_id"),
	}
	if raw := q.Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n <= 0 {
			writeError(w, http.StatusBadRequest, runtime.CodeInputInvalid, "limit 必须是正整数", map[string]any{"field": "limit"})
			return
		}
		filter.Limit = n
	}
	if raw := q.Get("from"); raw != "" {
		t, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			writeError(w, http.StatusBadRequest, runtime.CodeInputInvalid, "from 必须是 RFC3339 时间", map[string]any{"field": "from"})
			return
		}
		filter.From = t
	}
	if raw := q.Get("to"); raw != "" {
		t, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			writeError(w, http.StatusBadRequest, runtime.CodeInputInvalid, "to 必须是 RFC3339 时间", map[string]any{"field": "to"})
			return
		}
		filter.To = t
	}

	items, err := h.plugins.ListExecutions(r.Context(), subjectOf(r), filter)
	if err != nil {
		writeServiceError(w, h.logger, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"executions": items, "count": len(items)})
}
