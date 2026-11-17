package identity

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"operations-platform/internal/observability"
	"operations-platform/internal/store"
)

// Role 是 v1 的三级角色；角色矩阵属于实现基线（OQ-040）。
const (
	RoleAdmin    = "admin"
	RoleOperator = "operator"
	RoleViewer   = "viewer"
)

// User 是对外可见的账号信息，绝不包含派生值。
type User struct {
	ID          string    `json:"user_id"`
	Username    string    `json:"username"`
	DisplayName string    `json:"display_name"`
	Role        string    `json:"role"`
	Disabled    bool      `json:"disabled"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type userRecord struct {
	User
	PasswordHash string
}

// Session 是库内会话记录；对外不返回令牌摘要。
type Session struct {
	ID         string    `json:"-"`
	UserID     string    `json:"-"`
	CreatedAt  time.Time `json:"created_at"`
	ExpiresAt  time.Time `json:"expires_at"`
	LastSeenAt time.Time `json:"last_seen_at"`
}

// Repository 封装账号与会话的 SQL；不承载业务规则。
type Repository struct{ st *store.Store }

func NewRepository(st *store.Store) *Repository { return &Repository{st: st} }

func (r *Repository) CountUsers(ctx context.Context) (int, error) {
	var n int
	err := r.st.Read(ctx, func(tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, "SELECT count(*) FROM users").Scan(&n)
	})
	return n, err
}

func (r *Repository) CreateUser(ctx context.Context, u userRecord) error {
	now := u.CreatedAt.UTC().Format(time.RFC3339)
	return r.st.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			`INSERT INTO users (user_id, username, display_name, role, password_hash, disabled, created_at, updated_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			u.ID, u.Username, u.DisplayName, u.Role, u.PasswordHash, boolInt(u.Disabled), now, now)
		return err
	})
}

func (r *Repository) UserByUsername(ctx context.Context, username string) (*userRecord, error) {
	return r.queryUser(ctx, "SELECT user_id, username, display_name, role, password_hash, disabled, created_at, updated_at FROM users WHERE username = ?", username)
}

func (r *Repository) UserByID(ctx context.Context, id string) (*userRecord, error) {
	return r.queryUser(ctx, "SELECT user_id, username, display_name, role, password_hash, disabled, created_at, updated_at FROM users WHERE user_id = ?", id)
}

func (r *Repository) queryUser(ctx context.Context, query string, arg any) (*userRecord, error) {
	var (
		u        userRecord
		disabled int
		created  string
		updated  string
	)
	err := r.st.Read(ctx, func(tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, query, arg).Scan(
			&u.ID, &u.Username, &u.DisplayName, &u.Role, &u.PasswordHash, &disabled, &created, &updated)
	})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("读取账号失败: %w", err)
	}
	u.Disabled = disabled != 0
	u.CreatedAt, _ = time.Parse(time.RFC3339, created)
	u.UpdatedAt, _ = time.Parse(time.RFC3339, updated)
	return &u, nil
}

func (r *Repository) CreateSession(ctx context.Context, s Session) error {
	return r.st.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			`INSERT INTO sessions (session_id, user_id, created_at, expires_at, last_seen_at) VALUES (?, ?, ?, ?, ?)`,
			s.ID, s.UserID, s.CreatedAt.UTC().Format(time.RFC3339),
			s.ExpiresAt.UTC().Format(time.RFC3339), s.LastSeenAt.UTC().Format(time.RFC3339))
		return err
	})
}

func (r *Repository) SessionByHash(ctx context.Context, hash string) (*Session, error) {
	var (
		s                Session
		created, expires string
		lastSeen         string
	)
	err := r.st.Read(ctx, func(tx *sql.Tx) error {
		return tx.QueryRowContext(ctx,
			"SELECT session_id, user_id, created_at, expires_at, last_seen_at FROM sessions WHERE session_id = ?", hash).
			Scan(&s.ID, &s.UserID, &created, &expires, &lastSeen)
	})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("读取会话失败: %w", err)
	}
	s.CreatedAt, _ = time.Parse(time.RFC3339, created)
	s.ExpiresAt, _ = time.Parse(time.RFC3339, expires)
	s.LastSeenAt, _ = time.Parse(time.RFC3339, lastSeen)
	return &s, nil
}

func (r *Repository) TouchSession(ctx context.Context, hash string, at time.Time) error {
	return r.st.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "UPDATE sessions SET last_seen_at = ? WHERE session_id = ?",
			at.UTC().Format(time.RFC3339), hash)
		return err
	})
}

func (r *Repository) DeleteSession(ctx context.Context, hash string) error {
	return r.st.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "DELETE FROM sessions WHERE session_id = ?", hash)
		return err
	})
}

func (r *Repository) DeleteExpiredSessions(ctx context.Context, now time.Time) (int, error) {
	var n int64
	err := r.st.Write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, "DELETE FROM sessions WHERE expires_at <= ?", now.UTC().Format(time.RFC3339))
		if err != nil {
			return err
		}
		n, err = res.RowsAffected()
		return err
	})
	return int(n), err
}

// AuditFilter 是审计查询条件。
type AuditFilter struct {
	Action string
	Result string
	From   time.Time
	To     time.Time
	Limit  int
}

// ListAudits 按时间倒序返回审计事件。
func (r *Repository) ListAudits(ctx context.Context, f AuditFilter) ([]observability.AuditEvent, error) {
	limit := f.Limit
	if limit <= 0 {
		limit = 100
	}
	if limit > 500 {
		limit = 500
	}
	query := `SELECT occurred_at, subject, action, target_type, target_id, result, reason, details
	          FROM audit_events WHERE 1=1`
	args := make([]any, 0, 6)
	if f.Action != "" {
		query += " AND action = ?"
		args = append(args, f.Action)
	}
	if f.Result != "" {
		query += " AND result = ?"
		args = append(args, f.Result)
	}
	if !f.From.IsZero() {
		query += " AND occurred_at >= ?"
		args = append(args, f.From.UTC().Format(time.RFC3339))
	}
	if !f.To.IsZero() {
		query += " AND occurred_at <= ?"
		args = append(args, f.To.UTC().Format(time.RFC3339))
	}
	query += " ORDER BY occurred_at DESC LIMIT ?"
	args = append(args, limit)

	out := make([]observability.AuditEvent, 0, limit)
	err := r.st.Read(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, query, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var (
				e       observability.AuditEvent
				occurs  string
				details string
			)
			if err := rows.Scan(&occurs, &e.Subject, &e.Action, &e.TargetType, &e.TargetID, &e.Result, &e.Reason, &details); err != nil {
				return err
			}
			e.OccurredAt, _ = time.Parse(time.RFC3339, occurs)
			e.Details = observability.DecodeDetails(details)
			out = append(out, e)
		}
		return rows.Err()
	})
	return out, err
}

func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}
