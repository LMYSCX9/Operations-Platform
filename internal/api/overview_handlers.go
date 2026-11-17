package api

import (
	"net/http"
	"time"

	"operations-platform/internal/plugin/registry"
	"operations-platform/internal/scheduler"
)

// overview 汇总总览 KPI（M5，OQ-043）。
//
// 口径固定为「真实统计」：
//   - 插件数来自注册表；主机数来自 hosts 表；已下发数只统计 state=installed；
//   - 执行指标为最近 24 小时窗口，失败 = status 不属于 ok/skipped/missed 的记录；
//   - 不含任何估算或示例值，统计不到的项宁可不返回。
func (h *handler) overview(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	subj := subjectOf(r)
	if err := h.plugins.AuthorizeOverview(ctx, subj); err != nil {
		writeServiceError(w, h.logger, err)
		return
	}

	plugins, err := h.plugins.List(ctx, subj)
	if err != nil {
		writeServiceError(w, h.logger, err)
		return
	}
	pluginTotal, pluginEnabled := len(plugins), 0
	for _, p := range plugins {
		if p.State == registry.StateEnabled {
			pluginEnabled++
		}
	}

	hostTotal, hostReachable, err := h.hosts.CountByStatus(ctx)
	if err != nil {
		writeServiceError(w, h.logger, err)
		return
	}
	distInstalled, err := h.hosts.InstalledDistributionCount(ctx)
	if err != nil {
		writeServiceError(w, h.logger, err)
		return
	}

	tasks, err := h.tasks.List(ctx, subj)
	if err != nil {
		writeServiceError(w, h.logger, err)
		return
	}
	taskEnabled, taskPaused, taskUnavailable := 0, 0, 0
	for _, t := range tasks {
		switch t.State {
		case scheduler.StateEnabled:
			taskEnabled++
		case scheduler.StatePaused:
			taskPaused++
		case scheduler.StateUnavailable:
			taskUnavailable++
		}
	}

	now := time.Now().UTC()
	execTotal, execFailed, err := h.plugins.CountExecutions(ctx, now.Add(-24*time.Hour), now)
	if err != nil {
		writeServiceError(w, h.logger, err)
		return
	}

	missed := h.tasks.MissedCounts(ctx, tasks)

	writeJSON(w, http.StatusOK, map[string]any{
		"generated_at": now,
		"plugins": map[string]any{
			"total":    pluginTotal,
			"enabled":  pluginEnabled,
			"disabled": pluginTotal - pluginEnabled,
		},
		"hosts": map[string]any{
			"total":     hostTotal,
			"reachable": hostReachable,
		},
		"distributions": map[string]any{
			"installed": distInstalled,
		},
		"tasks": map[string]any{
			"total":       len(tasks),
			"enabled":     taskEnabled,
			"paused":      taskPaused,
			"unavailable": taskUnavailable,
			"missed_7d":   sumCounts(missed),
		},
		"executions_24h": map[string]any{
			"total":  execTotal,
			"failed": execFailed,
			"ok":     execTotal - execFailed,
		},
	})
}

func sumCounts(m map[string]int) int {
	total := 0
	for _, v := range m {
		total += v
	}
	return total
}
