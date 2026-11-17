package identity

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"time"

	"operations-platform/internal/observability"
	"operations-platform/internal/plugin/authz"
)

// 业务错误码：HTTP 层按此映射状态码，前端据此区分「凭证错误」与「会话过期」。
const (
	CodeInvalidCredentials = "invalid_credentials"
	CodeAccountDisabled    = "account_disabled"
	CodeRateLimited        = "rate_limited"
	CodeSessionInvalid     = "session_invalid"
	CodeSessionExpired     = "session_expired"
)

// Error 是身份相关业务错误。
type Error struct {
	Code       string
	Message    string
	RetryAfter time.Duration
}

func (e *Error) Error() string { return e.Code + ": " + e.Message }

// Options 是身份服务的依赖与策略。
type Options struct {
	Repository   *Repository
	Audit        *observability.AuditRecorder
	Logger       *slog.Logger
	SessionTTL   time.Duration
	MaxAttempts  int
	Lockout      time.Duration
	Now          func() time.Time
	BootstrapPwd string
}

// Service 实现登录、会话与审计查询。
type Service struct {
	repo         *Repository
	audit        *observability.AuditRecorder
	logger       *slog.Logger
	ttl          time.Duration
	attempt      int
	lockout      time.Duration
	now          func() time.Time
	bootstrapPwd string

	mu       sync.Mutex
	failures map[string]*failureState
}

type failureState struct {
	count       int
	lockedUntil time.Time
}

func NewService(o Options) *Service {
	if o.Now == nil {
		o.Now = func() time.Time { return time.Now().UTC() }
	}
	if o.SessionTTL <= 0 {
		o.SessionTTL = 12 * time.Hour
	}
	if o.MaxAttempts <= 0 {
		o.MaxAttempts = 5
	}
	if o.Lockout <= 0 {
		o.Lockout = 5 * time.Minute
	}
	return &Service{
		repo:         o.Repository,
		audit:        o.Audit,
		logger:       o.Logger,
		ttl:          o.SessionTTL,
		attempt:      o.MaxAttempts,
		lockout:      o.Lockout,
		now:          o.Now,
		bootstrapPwd: o.BootstrapPwd,
		failures:     make(map[string]*failureState),
	}
}

// Bootstrap 在没有账号时创建内置管理员。
//
// 未配置初始口令时**不创建**账号并告警：宁可登录不可用，也不生成无法告知用户的随机口令。
func (s *Service) Bootstrap(ctx context.Context) error {
	n, err := s.repo.CountUsers(ctx)
	if err != nil {
		return err
	}
	if n > 0 {
		return nil
	}
	if s.bootstrapPwd == "" {
		s.logger.Warn("未创建内置管理员：缺少初始管理员口令（admin_password / OPS_PLATFORM_ADMIN_PASSWORD）")
		return nil
	}
	if err := ValidatePasswordPolicy(s.bootstrapPwd); err != nil {
		return err
	}
	hash, err := HashPassword(s.bootstrapPwd)
	if err != nil {
		return err
	}
	now := s.now()
	user := userRecord{
		User: User{
			ID:          "admin",
			Username:    "admin",
			DisplayName: "内置管理员",
			Role:        RoleAdmin,
			CreatedAt:   now,
			UpdatedAt:   now,
		},
		PasswordHash: hash,
	}
	if err := s.repo.CreateUser(ctx, user); err != nil {
		return err
	}
	s.logger.Info("已创建内置管理员账号", "username", "admin")
	return nil
}

// Login 校验口令并创建会话；失败按账号计数与锁定。
func (s *Service) Login(ctx context.Context, username, password, remote string) (User, *Session, string, error) {
	username = normalizeUsername(username)
	if locked, retry := s.checkLock(username); locked {
		s.auditEvent(ctx, "user:"+username, "auth.login", "session", "", "denied",
			"账号处于锁定窗口", map[string]any{"remote": remote, "retry_after_seconds": int(retry.Seconds())})
		return User{}, nil, "", &Error{Code: CodeRateLimited, Message: "登录失败次数过多，请稍后再试", RetryAfter: retry}
	}

	rec, err := s.repo.UserByUsername(ctx, username)
	if err != nil {
		return User{}, nil, "", err
	}
	if rec == nil || !VerifyPassword(rec.PasswordHash, password) {
		s.noteFailure(username)
		s.auditEvent(ctx, "user:"+username, "auth.login", "session", "", "failure",
			"用户名或口令错误", map[string]any{"remote": remote})
		// 对外不区分「账号不存在」与「口令错误」。
		return User{}, nil, "", &Error{Code: CodeInvalidCredentials, Message: "用户名或口令错误"}
	}
	if rec.Disabled {
		s.auditEvent(ctx, "user:"+rec.ID, "auth.login", "session", "", "failure", "账号已禁用",
			map[string]any{"remote": remote})
		return User{}, nil, "", &Error{Code: CodeAccountDisabled, Message: "账号已被禁用"}
	}

	s.resetFailures(username)
	token, err := NewSessionToken()
	if err != nil {
		return User{}, nil, "", err
	}
	now := s.now()
	session := &Session{
		ID:         HashToken(token),
		UserID:     rec.ID,
		CreatedAt:  now,
		ExpiresAt:  now.Add(s.ttl),
		LastSeenAt: now,
	}
	if err := s.repo.CreateSession(ctx, *session); err != nil {
		return User{}, nil, "", err
	}
	// 顺带清理过期会话，避免表无限增长；失败不影响登录。
	if n, err := s.repo.DeleteExpiredSessions(ctx, now); err != nil {
		s.logger.Warn("清理过期会话失败", "error", err)
	} else if n > 0 {
		s.logger.Info("已清理过期会话", "count", n)
	}

	s.auditEvent(ctx, "user:"+rec.ID, "auth.login", "session", "", "success", "",
		map[string]any{"remote": remote, "username": rec.Username})
	return rec.User, session, token, nil
}

// Authenticate 校验会话令牌并返回账号；过期与非法分别给出不同错误码。
func (s *Service) Authenticate(ctx context.Context, token string) (User, *Session, error) {
	if token == "" {
		return User{}, nil, &Error{Code: CodeSessionInvalid, Message: "未携带会话"}
	}
	hash := HashToken(token)
	session, err := s.repo.SessionByHash(ctx, hash)
	if err != nil {
		return User{}, nil, err
	}
	if session == nil {
		return User{}, nil, &Error{Code: CodeSessionInvalid, Message: "会话无效"}
	}
	now := s.now()
	if !session.ExpiresAt.After(now) {
		_ = s.repo.DeleteSession(ctx, hash)
		return User{}, nil, &Error{Code: CodeSessionExpired, Message: "会话已过期，请重新登录"}
	}
	rec, err := s.repo.UserByID(ctx, session.UserID)
	if err != nil {
		return User{}, nil, err
	}
	if rec == nil || rec.Disabled {
		_ = s.repo.DeleteSession(ctx, hash)
		return User{}, nil, &Error{Code: CodeSessionInvalid, Message: "账号不可用"}
	}
	// 每分钟最多写一次 last_seen，避免每个请求都抢写锁。
	if now.Sub(session.LastSeenAt) >= time.Minute {
		if err := s.repo.TouchSession(ctx, hash, now); err != nil {
			s.logger.Warn("更新会话活跃时间失败", "error", err)
		}
	}
	return rec.User, session, nil
}

// Logout 删除会话并记审计。
func (s *Service) Logout(ctx context.Context, token string) {
	if token == "" {
		return
	}
	if err := s.repo.DeleteSession(ctx, HashToken(token)); err != nil {
		s.logger.Warn("删除会话失败", "error", err)
	}
}

// ListAudits 查询审计事件。
func (s *Service) ListAudits(ctx context.Context, subj authz.Subject, f AuditFilter) ([]observability.AuditEvent, error) {
	if d := (RoleAuthorizer{}).Authorize(ctx, subj, authz.ActionAuditRead, authz.Target{Type: "audit"}); !d.Allowed {
		s.auditEvent(ctx, subj.ID, "audit.read", "audit", "", "denied", d.Reason, nil)
		return nil, &Error{Code: "permission_denied", Message: "无审计查看权限"}
	}
	return s.repo.ListAudits(ctx, f)
}

func (s *Service) checkLock(username string) (bool, time.Duration) {
	if username == "" {
		return false, 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	st, ok := s.failures[username]
	if !ok {
		return false, 0
	}
	now := s.now()
	if st.lockedUntil.After(now) {
		return true, st.lockedUntil.Sub(now)
	}
	if !st.lockedUntil.IsZero() {
		// 锁定到期后重新计数。
		delete(s.failures, username)
	}
	return false, 0
}

func (s *Service) noteFailure(username string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st, ok := s.failures[username]
	if !ok {
		st = &failureState{}
		s.failures[username] = st
	}
	st.count++
	if st.count >= s.attempt {
		st.lockedUntil = s.now().Add(s.lockout)
		st.count = 0
	}
	if len(s.failures) > 1024 {
		// 防御性上限：只保留仍处于锁定的条目，避免内存无限增长。
		now := s.now()
		for k, v := range s.failures {
			if !v.lockedUntil.After(now) {
				delete(s.failures, k)
			}
		}
	}
}

func (s *Service) resetFailures(username string) {
	s.mu.Lock()
	delete(s.failures, username)
	s.mu.Unlock()
}

func (s *Service) auditEvent(ctx context.Context, subject, action, targetType, targetID, result, reason string, details map[string]any) {
	if s.audit == nil {
		return
	}
	s.audit.Record(ctx, observability.AuditEvent{
		OccurredAt: s.now(),
		Subject:    subject,
		Action:     action,
		TargetType: targetType,
		TargetID:   targetID,
		Result:     result,
		Reason:     reason,
		Details:    details,
	})
}

func normalizeUsername(v string) string {
	return strings.TrimSpace(strings.ToLower(v))
}
