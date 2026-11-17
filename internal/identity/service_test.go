package identity

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"operations-platform/internal/plugin/authz"
	"operations-platform/internal/store"
)

func testLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func newService(t *testing.T, now func() time.Time) (*Service, *Repository) {
	t.Helper()
	ctx := context.Background()
	dataDir := t.TempDir()
	if err := store.InitDataDir(ctx, testLogger(), dataDir, store.NoopFilesystemChecker{}); err != nil {
		t.Fatalf("初始化数据目录失败: %v", err)
	}
	st, err := store.Open(ctx, testLogger(), dataDir)
	if err != nil {
		t.Fatalf("打开存储失败: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	repo := NewRepository(st)
	svc := NewService(Options{
		Repository:   repo,
		Logger:       testLogger(),
		SessionTTL:   12 * time.Hour,
		MaxAttempts:  3,
		Lockout:      5 * time.Minute,
		Now:          now,
		BootstrapPwd: "initial-pass",
	})
	if err := svc.Bootstrap(ctx); err != nil {
		t.Fatalf("创建内置管理员失败: %v", err)
	}
	return svc, repo
}

func TestLoginAndSessionLifecycle(t *testing.T) {
	ctx := context.Background()
	current := time.Date(2026, 11, 16, 10, 0, 0, 0, time.UTC)
	svc, _ := newService(t, func() time.Time { return current })

	user, session, token, err := svc.Login(ctx, "admin", "initial-pass", "127.0.0.1")
	if err != nil {
		t.Fatalf("登录失败: %v", err)
	}
	if user.Role != RoleAdmin || token == "" {
		t.Fatalf("登录结果异常: role=%s token=%q", user.Role, token)
	}
	if !session.ExpiresAt.Equal(current.Add(12 * time.Hour)) {
		t.Fatalf("会话有效期 = %s，期望 12h", session.ExpiresAt)
	}

	got, _, err := svc.Authenticate(ctx, token)
	if err != nil || got.ID != user.ID {
		t.Fatalf("会话校验失败: user=%v err=%v", got, err)
	}

	// 过期后必须返回 session_expired，而不是静默放行。
	current = current.Add(13 * time.Hour)
	_, _, err = svc.Authenticate(ctx, token)
	var ie *Error
	if !asIdentityError(err, &ie) || ie.Code != CodeSessionExpired {
		t.Fatalf("期望 session_expired，实际: %v", err)
	}

	svc.Logout(ctx, token)
	if _, _, err := svc.Authenticate(ctx, token); err == nil {
		t.Fatal("登出后会话应失效")
	}
}

func TestLoginRateLimit(t *testing.T) {
	ctx := context.Background()
	current := time.Date(2026, 11, 16, 10, 0, 0, 0, time.UTC)
	svc, _ := newService(t, func() time.Time { return current })

	for i := 0; i < 3; i++ {
		if _, _, _, err := svc.Login(ctx, "admin", "wrong", "127.0.0.1"); err == nil {
			t.Fatal("错误口令不应登录成功")
		}
	}
	_, _, _, err := svc.Login(ctx, "admin", "initial-pass", "127.0.0.1")
	var ie *Error
	if !asIdentityError(err, &ie) || ie.Code != CodeRateLimited {
		t.Fatalf("第 4 次应被限速，实际: %v", err)
	}
	if ie.RetryAfter <= 0 {
		t.Fatal("限速应给出重试等待时间")
	}

	// 锁定窗口结束后恢复。
	current = current.Add(6 * time.Minute)
	if _, _, _, err := svc.Login(ctx, "admin", "initial-pass", "127.0.0.1"); err != nil {
		t.Fatalf("锁定到期后应可登录: %v", err)
	}
}

func TestBootstrapSkipsWhenPasswordMissing(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()
	if err := store.InitDataDir(ctx, testLogger(), dataDir, store.NoopFilesystemChecker{}); err != nil {
		t.Fatalf("初始化数据目录失败: %v", err)
	}
	st, err := store.Open(ctx, testLogger(), dataDir)
	if err != nil {
		t.Fatalf("打开存储失败: %v", err)
	}
	defer st.Close()
	repo := NewRepository(st)
	svc := NewService(Options{Repository: repo, Logger: testLogger()})
	if err := svc.Bootstrap(ctx); err != nil {
		t.Fatalf("缺少初始口令不应报错: %v", err)
	}
	n, err := repo.CountUsers(ctx)
	if err != nil {
		t.Fatalf("统计账号失败: %v", err)
	}
	if n != 0 {
		t.Fatalf("缺少初始口令时不应创建账号，实际 %d 个", n)
	}
}

func TestRoleAuthorizer(t *testing.T) {
	auth := RoleAuthorizer{}
	cases := []struct {
		role   string
		action authz.Action
		want   bool
	}{
		{RoleAdmin, authz.ActionPluginEnable, true},
		{RoleAdmin, authz.ActionAuditRead, true},
		{RoleOperator, authz.ActionPluginEnable, false},
		{RoleOperator, authz.ActionHostDistribute, true},
		{RoleViewer, authz.ActionPluginRead, true},
		{RoleViewer, authz.ActionPluginInvoke, false},
		{RoleViewer, authz.ActionHostWrite, false},
		{"unknown", authz.ActionPluginRead, false},
	}
	for _, c := range cases {
		d := auth.Authorize(context.Background(), authz.Subject{ID: "user:x", Roles: []string{c.role}},
			c.action, authz.Target{Type: "plugin"})
		if d.Allowed != c.want {
			t.Errorf("role=%s action=%s allowed=%v，期望 %v", c.role, c.action, d.Allowed, c.want)
		}
	}
}

// asIdentityError 避免测试文件直接依赖 errors.As 的泛型写法。
func asIdentityError(err error, target **Error) bool {
	for err != nil {
		if e, ok := err.(*Error); ok {
			*target = e
			return true
		}
		type unwrapper interface{ Unwrap() error }
		u, ok := err.(unwrapper)
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}
