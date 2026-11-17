package api

import (
	"net/http"
	"strconv"

	"operations-platform/internal/plugin/instances"
	"operations-platform/internal/plugin/runtime"
)

// listPluginTypes 返回类型目录（能力按类型聚合，实例数同时给出）。
func (h *handler) listPluginTypes(w http.ResponseWriter, r *http.Request) {
	types, err := h.plugins.ListTypes(r.Context(), subjectOf(r))
	if err != nil {
		writeServiceError(w, h.logger, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"types": types, "count": len(types)})
}

func (h *handler) getPluginType(w http.ResponseWriter, r *http.Request) {
	t, err := h.plugins.GetType(r.Context(), subjectOf(r), r.PathValue("id"))
	if err != nil {
		writeServiceError(w, h.logger, err)
		return
	}
	writeJSON(w, http.StatusOK, t)
}

// listPluginInstances 支持 type_id / host_id / custody / state 过滤。
func (h *handler) listPluginInstances(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	filter := instances.InstanceFilter{
		TypeID:  q.Get("type_id"),
		HostID:  q.Get("host_id"),
		Custody: q.Get("custody"),
		State:   q.Get("state"),
	}
	rows, err := h.plugins.ListInstances(r.Context(), subjectOf(r), filter)
	if err != nil {
		writeServiceError(w, h.logger, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"instances": rows, "count": len(rows)})
}

func (h *handler) getPluginInstance(w http.ResponseWriter, r *http.Request) {
	inst, err := h.plugins.GetInstance(r.Context(), subjectOf(r), r.PathValue("id"))
	if err != nil {
		writeServiceError(w, h.logger, err)
		return
	}
	writeJSON(w, http.StatusOK, inst)
}

func (h *handler) enablePluginInstance(w http.ResponseWriter, r *http.Request) {
	h.setPluginInstanceEnabled(w, r, true)
}

func (h *handler) disablePluginInstance(w http.ResponseWriter, r *http.Request) {
	h.setPluginInstanceEnabled(w, r, false)
}

func (h *handler) setPluginInstanceEnabled(w http.ResponseWriter, r *http.Request, enabled bool) {
	inst, err := h.plugins.SetInstanceEnabled(r.Context(), subjectOf(r), r.PathValue("id"), enabled)
	if err != nil {
		writeServiceError(w, h.logger, err)
		return
	}
	writeJSON(w, http.StatusOK, inst)
}

func (h *handler) probePluginInstance(w http.ResponseWriter, r *http.Request) {
	inst, err := h.plugins.ProbeInstance(r.Context(), subjectOf(r), r.PathValue("id"))
	if err != nil {
		writeServiceError(w, h.logger, err)
		return
	}
	writeJSON(w, http.StatusOK, inst)
}

func (h *handler) listInstanceProbes(w http.ResponseWriter, r *http.Request) {
	limit := 50
	if raw := r.URL.Query().Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n <= 0 {
			writeError(w, http.StatusBadRequest, runtime.CodeInputInvalid, "limit 必须是正整数", map[string]any{"field": "limit"})
			return
		}
		limit = n
	}
	probes, err := h.plugins.ListProbes(r.Context(), subjectOf(r), r.PathValue("id"), limit)
	if err != nil {
		writeServiceError(w, h.logger, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"probes": probes, "count": len(probes)})
}

func (h *handler) invokeInstanceCapability(w http.ResponseWriter, r *http.Request) {
	var payload invokePayload
	if err := decodeJSONBody(r, &payload); err != nil {
		writeError(w, http.StatusBadRequest, runtime.CodeInputInvalid, err.Error(), nil)
		return
	}
	res, err := h.plugins.InvokeInstance(r.Context(), subjectOf(r),
		r.PathValue("id"), r.PathValue("capability"), payload.Input, "api")
	if err != nil {
		writeServiceError(w, h.logger, err)
		return
	}
	status := http.StatusOK
	if res.Status != "ok" && res.Error != nil {
		status = statusForCode(res.Error.Code)
	}
	writeJSON(w, status, res)
}

func (h *handler) getInstanceConfig(w http.ResponseWriter, r *http.Request) {
	cfg, err := h.plugins.ListInstanceConfig(r.Context(), subjectOf(r), r.PathValue("id"))
	if err != nil {
		writeServiceError(w, h.logger, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"config": cfg})
}

// updateInstanceConfig 接收扁平键值；null 表示删除该键。
func (h *handler) updateInstanceConfig(w http.ResponseWriter, r *http.Request) {
	var payload map[string]any
	if err := decodeJSONBody(r, &payload); err != nil {
		writeError(w, http.StatusBadRequest, runtime.CodeInputInvalid, err.Error(), nil)
		return
	}
	cfg, err := h.plugins.SetInstanceConfig(r.Context(), subjectOf(r), r.PathValue("id"), payload)
	if err != nil {
		writeServiceError(w, h.logger, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"config": cfg})
}

func (h *handler) listPluginEvents(w http.ResponseWriter, r *http.Request) {
	limit := 100
	if raw := r.URL.Query().Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n <= 0 {
			writeError(w, http.StatusBadRequest, runtime.CodeInputInvalid, "limit 必须是正整数", map[string]any{"field": "limit"})
			return
		}
		limit = n
	}
	events, err := h.plugins.ListEvents(r.Context(), subjectOf(r), limit)
	if err != nil {
		writeServiceError(w, h.logger, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"events": events, "count": len(events)})
}

// listCapabilities 以类型为键汇总能力声明，并给出提供该能力的实例数。
func (h *handler) listCapabilities(w http.ResponseWriter, r *http.Request) {
	types, err := h.plugins.ListTypes(r.Context(), subjectOf(r))
	if err != nil {
		writeServiceError(w, h.logger, err)
		return
	}
	out := make([]map[string]any, 0)
	for _, t := range types {
		for _, c := range t.Capabilities {
			out = append(out, map[string]any{
				"capability":     c,
				"type_id":        t.TypeID,
				"instance_count": t.InstanceCount,
			})
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"capabilities": out, "count": len(out)})
}

func (h *handler) listCapabilityInstances(w http.ResponseWriter, r *http.Request) {
	items, err := h.plugins.CapabilityInstances(r.Context(), subjectOf(r), r.PathValue("capability"))
	if err != nil {
		writeServiceError(w, h.logger, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"instances": items, "count": len(items)})
}
