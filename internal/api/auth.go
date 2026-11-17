package api

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"operations-platform/internal/identity"
	"operations-platform/internal/plugin/authz"
)

// 会话 Cookie 属于实现基线（OQ-040）：HttpOnly + SameSite=Strict，HTTPS 下追加 Secure。
const (
	sessionCookieName = "ops_session"
	csrfHeader        = "X-Ops-Request"
)

type ctxKey int

const (
	subjectCtxKey ctxKey = iota
	userCtxKey
)

// SubjectFromContext 返回会话中间件注入的主体。
func SubjectFromContext(ctx context.Context) (authz.Subject, bool) {
	s, ok := ctx.Value(subjectCtxKey).(authz.Subject)
	return s, ok
}

// UserFromContext 返回会话中间件注入的账号。
func UserFromContext(ctx context.Context) (identity.User, bool) {
	u, ok := ctx.Value(userCtxKey).(identity.User)
	return u, ok
}

// requireSession 对所有 /api/* 路由做会话校验（登录与健康检查除外），
// 并对非安全方法做 CSRF 防护。未登录一律拒绝，不区分接口。
func (h *handler) requireSession(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		if !strings.HasPrefix(path, "/api/") ||
			path == PathVersion ||
			path == "/api/auth/login" {
			next.ServeHTTP(w, r)
			return
		}
		if !csrfSafe(r) {
			writeError(w, http.StatusForbidden, "csrf_rejected", "请求缺少同源校验标识，已拒绝", nil)
			return
		}
		cookie, err := r.Cookie(sessionCookieName)
		if err != nil || cookie.Value == "" {
			writeError(w, http.StatusUnauthorized, identity.CodeSessionInvalid, "未登录或会话已失效", nil)
			return
		}
		user, _, err := h.auth.Authenticate(r.Context(), cookie.Value)
		if err != nil {
			code := identity.CodeSessionInvalid
			var ie *identity.Error
			if errors.As(err, &ie) {
				code = ie.Code
			}
			writeError(w, http.StatusUnauthorized, code, "会话不可用，请重新登录", nil)
			return
		}
		ctx := context.WithValue(r.Context(), subjectCtxKey, identity.SubjectOf(user))
		ctx = context.WithValue(ctx, userCtxKey, user)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// csrfSafe 要求非安全方法满足以下任一条件：
//  1. Origin/Referer 与 Host 一致（浏览器同源请求）；
//  2. 显式携带 X-Ops-Request: 1（脚本调用）。
//
// 两者都不满足时拒绝，避免跨站表单/请求伪造。
func csrfSafe(r *http.Request) bool {
	switch r.Method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return true
	}
	if r.Header.Get(csrfHeader) == "1" {
		return true
	}
	origin := r.Header.Get("Origin")
	if origin == "" {
		origin = r.Header.Get("Referer")
	}
	if origin == "" {
		return false
	}
	host := r.Host
	return strings.Contains(origin, "://"+host)
}

func (h *handler) setSessionCookie(w http.ResponseWriter, r *http.Request, token string, expires time.Time) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    token,
		Path:     "/",
		Expires:  expires,
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		Secure:   r.TLS != nil,
	})
}

func (h *handler) clearSessionCookie(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		Secure:   r.TLS != nil,
	})
}

type loginPayload struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

func (h *handler) login(w http.ResponseWriter, r *http.Request) {
	var payload loginPayload
	if err := decodeJSONBody(r, &payload); err != nil {
		writeError(w, http.StatusBadRequest, "input_invalid", err.Error(), nil)
		return
	}
	user, session, token, err := h.auth.Login(r.Context(), payload.Username, payload.Password, clientIP(r))
	if err != nil {
		writeIdentityError(w, err)
		return
	}
	h.setSessionCookie(w, r, token, session.ExpiresAt)
	writeJSON(w, http.StatusOK, map[string]any{
		"user":       user,
		"expires_at": session.ExpiresAt,
	})
}

func (h *handler) logout(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie(sessionCookieName); err == nil {
		h.auth.Logout(r.Context(), cookie.Value)
	}
	h.clearSessionCookie(w, r)
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok"})
}

func (h *handler) session(w http.ResponseWriter, r *http.Request) {
	user, ok := UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, identity.CodeSessionInvalid, "未登录", nil)
		return
	}
	// 只回传有效期，不回传令牌本身。
	writeJSON(w, http.StatusOK, map[string]any{"user": user})
}

// writeIdentityError 把身份错误映射为 HTTP 状态与错误码。
func writeIdentityError(w http.ResponseWriter, err error) {
	var ie *identity.Error
	if errors.As(err, &ie) {
		switch ie.Code {
		case identity.CodeRateLimited:
			if ie.RetryAfter > 0 {
				w.Header().Set("Retry-After", strconv.Itoa(int(ie.RetryAfter.Seconds())+1))
			}
			writeError(w, http.StatusTooManyRequests, ie.Code, ie.Message, nil)
		case identity.CodeInvalidCredentials, identity.CodeSessionInvalid, identity.CodeSessionExpired:
			writeError(w, http.StatusUnauthorized, ie.Code, ie.Message, nil)
		case identity.CodeAccountDisabled, "permission_denied":
			writeError(w, http.StatusForbidden, ie.Code, ie.Message, nil)
		default:
			writeError(w, http.StatusBadRequest, ie.Code, ie.Message, nil)
		}
		return
	}
	writeError(w, http.StatusInternalServerError, "internal_error", "服务内部错误", nil)
}

func clientIP(r *http.Request) string {
	if v := r.Header.Get("X-Forwarded-For"); v != "" {
		return strings.TrimSpace(strings.Split(v, ",")[0])
	}
	return r.RemoteAddr
}
