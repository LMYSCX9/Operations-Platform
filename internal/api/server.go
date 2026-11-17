// Package api 提供底座对内的 HTTP 接口与内嵌 UI 宿主。
//
// M0 只暴露健康检查、版本信息与静态页面；插件调用网关、鉴权与开放 API 均不在本阶段
// （OQ-019 决定 v1 仅服务内嵌 UI）。
package api

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"runtime"
	"time"

	"operations-platform/internal/config"
	"operations-platform/internal/host"
	"operations-platform/internal/identity"
	"operations-platform/internal/plugin"
	"operations-platform/internal/plugin/authz"
	"operations-platform/internal/scheduler"
	"operations-platform/internal/store"
	"operations-platform/web"
)

// 路由路径属于 M0~M2 实现基线，待 OQ-036/OQ-039/OQ-040 确认。
const (
	PathHealthz = "/healthz"
	PathVersion = "/api/version"
)

// BuildInfo 由构建脚本通过 -ldflags 注入。
type BuildInfo struct {
	Version   string
	Commit    string
	BuildTime string
}

type handler struct {
	logger  *slog.Logger
	store   *store.Store
	plugins *plugin.Service
	hosts   *host.Service
	tasks   *scheduler.Service
	auth    *identity.Service
	build   BuildInfo
}

// NewServer 组装路由与中间件。调用方负责启动与优雅关闭。
func NewServer(cfg config.Config, logger *slog.Logger, st *store.Store, svc *plugin.Service,
	hosts *host.Service, tasks *scheduler.Service, auth *identity.Service, build BuildInfo) *http.Server {
	h := &handler{logger: logger, store: st, plugins: svc, hosts: hosts, tasks: tasks, auth: auth, build: build}

	mux := http.NewServeMux()
	mux.HandleFunc("GET "+PathHealthz, h.health)
	mux.HandleFunc("GET "+PathVersion, h.version)

	// 身份与审计（M2）。
	mux.HandleFunc("POST /api/auth/login", h.login)
	mux.HandleFunc("POST /api/auth/logout", h.logout)
	mux.HandleFunc("GET /api/auth/session", h.session)
	mux.HandleFunc("GET /api/audit-events", h.listAuditEvents)

	// 总览（M5，OQ-043）。
	mux.HandleFunc("GET /api/overview", h.overview)

	// 插件与执行记录（M1）。鉴权在 Service 内按能力级判定，HTTP 层只透传主体。
	mux.HandleFunc("GET /api/plugins", h.listPlugins)
	mux.HandleFunc("GET /api/plugins/{id}", h.getPlugin)
	mux.HandleFunc("POST /api/plugins/scan", h.scanPlugins)
	mux.HandleFunc("POST /api/plugins/{id}/enable", h.enablePlugin)
	mux.HandleFunc("POST /api/plugins/{id}/disable", h.disablePlugin)
	mux.HandleFunc("POST /api/plugins/{id}/capabilities/{capability}/invoke", h.invokeCapability)

	// 类型、实例与能力（M6，DOC-17 §8）。/api/plugins 保留为本机实例别名。
	mux.HandleFunc("GET /api/plugin-types", h.listPluginTypes)
	mux.HandleFunc("GET /api/plugin-types/{id}", h.getPluginType)
	mux.HandleFunc("GET /api/plugin-instances", h.listPluginInstances)
	mux.HandleFunc("GET /api/plugin-instances/{id}", h.getPluginInstance)
	mux.HandleFunc("POST /api/plugin-instances/{id}/enable", h.enablePluginInstance)
	mux.HandleFunc("POST /api/plugin-instances/{id}/disable", h.disablePluginInstance)
	mux.HandleFunc("POST /api/plugin-instances/{id}/probe", h.probePluginInstance)
	mux.HandleFunc("GET /api/plugin-instances/{id}/probes", h.listInstanceProbes)
	mux.HandleFunc("POST /api/plugin-instances/{id}/capabilities/{capability}/invoke", h.invokeInstanceCapability)
	mux.HandleFunc("GET /api/plugin-instances/{id}/config", h.getInstanceConfig)
	mux.HandleFunc("PUT /api/plugin-instances/{id}/config", h.updateInstanceConfig)
	mux.HandleFunc("GET /api/plugin-events", h.listPluginEvents)
	mux.HandleFunc("GET /api/capabilities", h.listCapabilities)
	mux.HandleFunc("GET /api/capabilities/{capability}/instances", h.listCapabilityInstances)

	mux.HandleFunc("GET /api/executions", h.listExecutions)

	// 主机与下发（M3）。凭据只在服务内部解密，任何读接口都不回显。
	mux.HandleFunc("GET /api/hosts", h.listHosts)
	mux.HandleFunc("POST /api/hosts", h.createHost)
	mux.HandleFunc("GET /api/hosts/{id}", h.getHost)
	mux.HandleFunc("PUT /api/hosts/{id}", h.updateHost)
	mux.HandleFunc("DELETE /api/hosts/{id}", h.deleteHost)
	mux.HandleFunc("POST /api/hosts/{id}/test", h.testHost)
	mux.HandleFunc("POST /api/hosts/{id}/distributions", h.createDistribution)
	mux.HandleFunc("GET /api/hosts/{id}/distributions", h.listDistributions)
	mux.HandleFunc("GET /api/distributions/{id}", h.getDistribution)
	mux.HandleFunc("POST /api/distributions/{id}/retry", h.retryDistribution)
	mux.HandleFunc("DELETE /api/distributions/{id}", h.cancelDistribution)

	// 定时任务（M4）。
	mux.HandleFunc("GET /api/tasks", h.listTasks)
	mux.HandleFunc("POST /api/tasks", h.createTask)
	mux.HandleFunc("GET /api/tasks/{id}", h.getTask)
	mux.HandleFunc("PUT /api/tasks/{id}", h.updateTask)
	mux.HandleFunc("DELETE /api/tasks/{id}", h.deleteTask)
	mux.HandleFunc("POST /api/tasks/{id}/enable", h.enableTask)
	mux.HandleFunc("POST /api/tasks/{id}/pause", h.pauseTask)
	mux.HandleFunc("POST /api/tasks/{id}/run", h.runTask)
	mux.HandleFunc("GET /api/tasks/{id}/executions", h.listTaskExecutions)

	// 原型是单页静态文件，没有前端路由；未知路径返回 404 即可。
	mux.Handle("/", http.FileServer(http.FS(web.Dist())))

	// 顺序：访问日志 → 会话/CSRF → 业务路由。
	return &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           requestLogger(logger, h.requireSession(mux)),
		ReadHeaderTimeout: 5 * time.Second,
	}
}

// subjectOf 返回当前请求的操作主体。
// 主体由会话中间件注入；缺失时返回空主体（即默认拒绝），不再回退到内置管理员。
func subjectOf(r *http.Request) authz.Subject {
	subj, _ := SubjectFromContext(r.Context())
	return subj
}

func (h *handler) health(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()

	if err := h.store.Ping(ctx); err != nil {
		h.logger.Error("健康检查失败", "error", err)
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "unhealthy"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (h *handler) version(w http.ResponseWriter, r *http.Request) {
	schemaVersion, err := h.store.SchemaVersion(r.Context())
	if err != nil {
		h.logger.Error("读取 schema 版本失败", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "无法读取数据库 schema 版本"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"version":        h.build.Version,
		"commit":         h.build.Commit,
		"build_time":     h.build.BuildTime,
		"go_version":     runtime.Version(),
		"schema_version": schemaVersion,
	})
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	// 响应体已经部分写出后，编码错误只能记录，无法再改状态码。
	_ = json.NewEncoder(w).Encode(payload)
}

// statusRecorder 记录响应状态码与字节数，供访问日志使用。
type statusRecorder struct {
	http.ResponseWriter
	status int
	bytes  int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

func (r *statusRecorder) Write(b []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	n, err := r.ResponseWriter.Write(b)
	r.bytes += n
	return n, err
}

func requestLogger(logger *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w}
		next.ServeHTTP(rec, r)
		// 只记录路径，不记录查询串，避免把可能出现的敏感参数写入日志（NFR-LOG-05）。
		logger.Info("HTTP 请求",
			"method", r.Method,
			"path", r.URL.Path,
			"status", rec.status,
			"bytes", rec.bytes,
			"duration_ms", time.Since(start).Milliseconds(),
			"remote_addr", r.RemoteAddr,
		)
	})
}
