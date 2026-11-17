package api

import (
	"net/http"
	"strconv"
	"time"

	"operations-platform/internal/identity"
	"operations-platform/internal/plugin/runtime"
)

// listAuditEvents 返回审计事件；仅管理员可读（角色矩阵见 DOC-13）。
func (h *handler) listAuditEvents(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	filter := identity.AuditFilter{Action: q.Get("action"), Result: q.Get("result")}
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
	items, err := h.auth.ListAudits(r.Context(), subjectOf(r), filter)
	if err != nil {
		writeIdentityError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"audit_events": items, "count": len(items)})
}
