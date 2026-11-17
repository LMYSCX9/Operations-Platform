package api

import (
	"net/http"

	"operations-platform/internal/plugin/registry"
	"operations-platform/internal/plugin/runtime"
	"operations-platform/internal/scheduler"
)

// listTasks 返回任务列表，并附带近 7 天错过次数（DOC-10 §8.3）。
func (h *handler) listTasks(w http.ResponseWriter, r *http.Request) {
	tasks, err := h.tasks.List(r.Context(), subjectOf(r))
	if err != nil {
		writeServiceError(w, h.logger, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"tasks":        tasks,
		"count":        len(tasks),
		"missed_count": h.tasks.MissedCounts(r.Context(), tasks),
	})
}

func (h *handler) getTask(w http.ResponseWriter, r *http.Request) {
	t, err := h.tasks.Get(r.Context(), subjectOf(r), r.PathValue("id"))
	if err != nil {
		writeServiceError(w, h.logger, err)
		return
	}
	writeJSON(w, http.StatusOK, t)
}

func (h *handler) createTask(w http.ResponseWriter, r *http.Request) {
	var in scheduler.TaskInput
	if err := decodeJSONBody(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, runtime.CodeInputInvalid, err.Error(), nil)
		return
	}
	t, err := h.tasks.Create(r.Context(), subjectOf(r), in)
	if err != nil {
		writeServiceError(w, h.logger, err)
		return
	}
	writeJSON(w, http.StatusCreated, t)
}

func (h *handler) updateTask(w http.ResponseWriter, r *http.Request) {
	var in scheduler.TaskInput
	if err := decodeJSONBody(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, runtime.CodeInputInvalid, err.Error(), nil)
		return
	}
	t, err := h.tasks.Update(r.Context(), subjectOf(r), r.PathValue("id"), in)
	if err != nil {
		writeServiceError(w, h.logger, err)
		return
	}
	writeJSON(w, http.StatusOK, t)
}

func (h *handler) deleteTask(w http.ResponseWriter, r *http.Request) {
	if err := h.tasks.Delete(r.Context(), subjectOf(r), r.PathValue("id")); err != nil {
		writeServiceError(w, h.logger, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "deleted"})
}

func (h *handler) enableTask(w http.ResponseWriter, r *http.Request) {
	t, err := h.tasks.Enable(r.Context(), subjectOf(r), r.PathValue("id"))
	if err != nil {
		writeServiceError(w, h.logger, err)
		return
	}
	writeJSON(w, http.StatusOK, t)
}

func (h *handler) pauseTask(w http.ResponseWriter, r *http.Request) {
	t, err := h.tasks.Pause(r.Context(), subjectOf(r), r.PathValue("id"))
	if err != nil {
		writeServiceError(w, h.logger, err)
		return
	}
	writeJSON(w, http.StatusOK, t)
}

func (h *handler) runTask(w http.ResponseWriter, r *http.Request) {
	res, err := h.tasks.RunNow(r.Context(), subjectOf(r), r.PathValue("id"))
	if err != nil {
		writeServiceError(w, h.logger, err)
		return
	}
	status := http.StatusOK
	if res.Status != registry.StatusOK && res.Error != nil {
		status = statusForCode(res.Error.Code)
	}
	writeJSON(w, status, res)
}

func (h *handler) listTaskExecutions(w http.ResponseWriter, r *http.Request) {
	filter := registry.ExecutionFilter{TaskID: r.PathValue("id"), Limit: 100}
	items, err := h.plugins.ListExecutions(r.Context(), subjectOf(r), filter)
	if err != nil {
		writeServiceError(w, h.logger, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"executions": items, "count": len(items)})
}
