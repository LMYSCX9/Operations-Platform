package api

import (
	"net/http"

	"operations-platform/internal/host"
	"operations-platform/internal/plugin/runtime"
)

// listHosts 返回主机列表（不含任何凭据）。
func (h *handler) listHosts(w http.ResponseWriter, r *http.Request) {
	items, err := h.hosts.List(r.Context(), subjectOf(r))
	if err != nil {
		writeServiceError(w, h.logger, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"hosts": items, "count": len(items)})
}

func (h *handler) getHost(w http.ResponseWriter, r *http.Request) {
	item, err := h.hosts.Get(r.Context(), subjectOf(r), r.PathValue("id"))
	if err != nil {
		writeServiceError(w, h.logger, err)
		return
	}
	writeJSON(w, http.StatusOK, item)
}

func (h *handler) createHost(w http.ResponseWriter, r *http.Request) {
	var in host.HostInput
	if err := decodeJSONBody(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, runtime.CodeInputInvalid, err.Error(), nil)
		return
	}
	item, err := h.hosts.Create(r.Context(), subjectOf(r), in)
	if err != nil {
		writeServiceError(w, h.logger, err)
		return
	}
	writeJSON(w, http.StatusCreated, item)
}

func (h *handler) updateHost(w http.ResponseWriter, r *http.Request) {
	var in host.HostInput
	if err := decodeJSONBody(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, runtime.CodeInputInvalid, err.Error(), nil)
		return
	}
	item, err := h.hosts.Update(r.Context(), subjectOf(r), r.PathValue("id"), in)
	if err != nil {
		writeServiceError(w, h.logger, err)
		return
	}
	writeJSON(w, http.StatusOK, item)
}

func (h *handler) deleteHost(w http.ResponseWriter, r *http.Request) {
	force := r.URL.Query().Get("force") == "true"
	if err := h.hosts.Delete(r.Context(), subjectOf(r), r.PathValue("id"), force); err != nil {
		writeServiceError(w, h.logger, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "deleted"})
}

func (h *handler) testHost(w http.ResponseWriter, r *http.Request) {
	res, err := h.hosts.Test(r.Context(), subjectOf(r), r.PathValue("id"))
	if err != nil {
		// 连通性失败仍返回结构化结果，便于界面展示分类原因。
		var he *host.Error
		if ok := asHostError(err, &he); ok {
			writeJSON(w, statusForHostCode(he.Code), map[string]any{
				"result": res,
				"error": map[string]any{
					"code":    he.Code,
					"message": he.Message,
				},
			})
			return
		}
		writeServiceError(w, h.logger, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

type distributePayload struct {
	PluginID      string `json:"plugin_id"`
	PluginVersion string `json:"plugin_version"`
	// Port 可选；为 0 时使用清单 runtime.listen.port（远端 daemon 必填）。
	Port int `json:"port,omitempty"`
}

func (h *handler) createDistribution(w http.ResponseWriter, r *http.Request) {
	var payload distributePayload
	if err := decodeJSONBody(r, &payload); err != nil {
		writeError(w, http.StatusBadRequest, runtime.CodeInputInvalid, err.Error(), nil)
		return
	}
	if payload.PluginID == "" {
		writeError(w, http.StatusBadRequest, runtime.CodeInputInvalid, "plugin_id 不能为空", map[string]any{"field": "plugin_id"})
		return
	}
	d, err := h.hosts.DistributeWithPort(r.Context(), subjectOf(r), r.PathValue("id"), payload.PluginID, payload.PluginVersion, payload.Port)
	if err != nil {
		writeServiceError(w, h.logger, err)
		return
	}
	writeJSON(w, http.StatusAccepted, d)
}

func (h *handler) listDistributions(w http.ResponseWriter, r *http.Request) {
	hostID := r.PathValue("id")
	items, err := h.hosts.ListDistributions(r.Context(), subjectOf(r), hostID)
	if err != nil {
		writeServiceError(w, h.logger, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"distributions": items, "count": len(items)})
}

func (h *handler) getDistribution(w http.ResponseWriter, r *http.Request) {
	d, err := h.hosts.GetDistribution(r.Context(), subjectOf(r), r.PathValue("id"))
	if err != nil {
		writeServiceError(w, h.logger, err)
		return
	}
	writeJSON(w, http.StatusOK, d)
}

func (h *handler) retryDistribution(w http.ResponseWriter, r *http.Request) {
	d, err := h.hosts.Retry(r.Context(), subjectOf(r), r.PathValue("id"))
	if err != nil {
		writeServiceError(w, h.logger, err)
		return
	}
	writeJSON(w, http.StatusAccepted, d)
}

func (h *handler) cancelDistribution(w http.ResponseWriter, r *http.Request) {
	d, err := h.hosts.Cancel(r.Context(), subjectOf(r), r.PathValue("id"))
	if err != nil {
		writeServiceError(w, h.logger, err)
		return
	}
	writeJSON(w, http.StatusOK, d)
}
