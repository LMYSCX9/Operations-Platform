package scheduler

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// DB 是调度包需要的最小存储能力。
type DB interface {
	Write(ctx context.Context, fn func(tx *sql.Tx) error) error
	Read(ctx context.Context, fn func(tx *sql.Tx) error) error
}

// Store 封装 tasks 表（v5，OQ-042）。
type Store struct {
	db DB
}

func NewStore(db DB) *Store { return &Store{db: db} }

func formatTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339Nano)
}

func formatTimePtr(t *time.Time) string {
	if t == nil {
		return ""
	}
	return formatTime(*t)
}

func parseTime(raw string) *time.Time {
	if raw == "" {
		return nil
	}
	t, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		return nil
	}
	return &t
}

const taskColumns = `task_id, name, description, instance_id, plugin_id, capability_id, input_json,
	trigger_type, cron_expr, interval_seconds, timezone, timeout_seconds, retries,
	overlap_policy, state, next_run_at, last_run_at, claimed_at, claim_token,
	created_by, created_at, updated_at`

func scanTask(scan func(dest ...any) error) (Task, error) {
	var t Task
	var input, nextRun, lastRun, claimed, claimToken, created, updated string
	err := scan(&t.ID, &t.Name, &t.Description, &t.InstanceID, &t.PluginID, &t.CapabilityID, &input,
		&t.TriggerType, &t.CronExpr, &t.IntervalSeconds, &t.Timezone, &t.TimeoutSeconds, &t.Retries,
		&t.OverlapPolicy, &t.State, &nextRun, &lastRun, &claimed, &claimToken,
		&t.CreatedBy, &created, &updated)
	if err != nil {
		return Task{}, err
	}
	t.Input = []byte(input)
	t.NextRunAt = parseTime(nextRun)
	t.LastRunAt = parseTime(lastRun)
	if v := parseTime(created); v != nil {
		t.CreatedAt = *v
	}
	if v := parseTime(updated); v != nil {
		t.UpdatedAt = *v
	}
	return t, nil
}

// Create 新增任务。
func (s *Store) Create(ctx context.Context, t Task) error {
	now := time.Now().UTC()
	t.CreatedAt, t.UpdatedAt = now, now
	return s.db.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO tasks
			(task_id, name, description, instance_id, plugin_id, capability_id, input_json,
			 trigger_type, cron_expr, interval_seconds, timezone, timeout_seconds, retries,
			 overlap_policy, state, next_run_at, last_run_at, claimed_at, claim_token,
			 created_by, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, '', '', '', ?, ?, ?)`,
			t.ID, t.Name, t.Description, t.InstanceID, t.PluginID, t.CapabilityID, string(t.Input),
			t.TriggerType, t.CronExpr, t.IntervalSeconds, t.Timezone, t.TimeoutSeconds, t.Retries,
			t.OverlapPolicy, t.State, formatTimePtr(t.NextRunAt), t.CreatedBy,
			formatTime(t.CreatedAt), formatTime(t.UpdatedAt))
		if err != nil {
			return fmt.Errorf("创建任务失败: %w", err)
		}
		return nil
	})
}

// Update 更新任务定义；不改变运行态字段（next_run_at/last_run_at/claim）。
func (s *Store) Update(ctx context.Context, t Task) error {
	now := time.Now().UTC()
	return s.db.Write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE tasks SET
			name = ?, description = ?, instance_id = ?, plugin_id = ?, capability_id = ?, input_json = ?,
			trigger_type = ?, cron_expr = ?, interval_seconds = ?, timezone = ?,
			timeout_seconds = ?, retries = ?, updated_at = ?
			WHERE task_id = ?`,
			t.Name, t.Description, t.InstanceID, t.PluginID, t.CapabilityID, string(t.Input),
			t.TriggerType, t.CronExpr, t.IntervalSeconds, t.Timezone,
			t.TimeoutSeconds, t.Retries, formatTime(now), t.ID)
		if err != nil {
			return fmt.Errorf("更新任务失败: %w", err)
		}
		if n, err := res.RowsAffected(); err != nil {
			return err
		} else if n == 0 {
			return ErrNotFound
		}
		return nil
	})
}

// Delete 删除任务；执行记录保留。
func (s *Store) Delete(ctx context.Context, id string) error {
	return s.db.Write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `DELETE FROM tasks WHERE task_id = ?`, id)
		if err != nil {
			return fmt.Errorf("删除任务失败: %w", err)
		}
		if n, err := res.RowsAffected(); err != nil {
			return err
		} else if n == 0 {
			return ErrNotFound
		}
		return nil
	})
}

// Get 读取单个任务。
func (s *Store) Get(ctx context.Context, id string) (Task, error) {
	var out Task
	err := s.db.Read(ctx, func(tx *sql.Tx) error {
		t, err := scanTask(tx.QueryRowContext(ctx, `SELECT `+taskColumns+` FROM tasks WHERE task_id = ?`, id).Scan)
		if err != nil {
			return err
		}
		out = t
		return nil
	})
	if errors.Is(err, sql.ErrNoRows) {
		return Task{}, ErrNotFound
	}
	if err != nil {
		return Task{}, fmt.Errorf("读取任务失败: %w", err)
	}
	return out, nil
}

// List 返回全部任务。
func (s *Store) List(ctx context.Context) ([]Task, error) {
	var out []Task
	err := s.db.Read(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `SELECT `+taskColumns+` FROM tasks ORDER BY name`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			t, err := scanTask(rows.Scan)
			if err != nil {
				return err
			}
			out = append(out, t)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("列出任务失败: %w", err)
	}
	return out, nil
}

// ListDue 返回已到期的启用任务（不含已被认领的任务）。
func (s *Store) ListDue(ctx context.Context, now time.Time) ([]Task, error) {
	var out []Task
	err := s.db.Read(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `SELECT `+taskColumns+` FROM tasks
			WHERE state = ? AND next_run_at != '' AND next_run_at <= ? AND claimed_at = ''
			ORDER BY next_run_at`, StateEnabled, formatTime(now))
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			t, err := scanTask(rows.Scan)
			if err != nil {
				return err
			}
			out = append(out, t)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("扫描到期任务失败: %w", err)
	}
	return out, nil
}

// Claim 以单条条件更新认领任务（先认领后执行，DOC-10 §6）。
func (s *Store) Claim(ctx context.Context, id, token string, now, expectedNext time.Time) (bool, error) {
	var claimed bool
	err := s.db.Write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE tasks SET claimed_at = ?, claim_token = ?
			WHERE task_id = ? AND state = ? AND claimed_at = '' AND next_run_at = ?`,
			formatTime(now), token, id, StateEnabled, formatTime(expectedNext))
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return err
		}
		claimed = n == 1
		return nil
	})
	return claimed, err
}

// ListClaimed 返回仍带认领标记的任务（启动时用于崩溃恢复）。
func (s *Store) ListClaimed(ctx context.Context) ([]Task, error) {
	var out []Task
	err := s.db.Read(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `SELECT `+taskColumns+` FROM tasks WHERE claimed_at != ''`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			t, err := scanTask(rows.Scan)
			if err != nil {
				return err
			}
			out = append(out, t)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("读取认领中的任务失败: %w", err)
	}
	return out, nil
}

// SetNextRun 更新下一次触发时间与最近触发时间，保留认领（执行期间继续占位）。
func (s *Store) SetNextRun(ctx context.Context, id string, next, last time.Time) error {
	return s.db.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `UPDATE tasks SET next_run_at = ?, last_run_at = ?, updated_at = ?
			WHERE task_id = ?`, formatTime(next), formatTime(last), formatTime(time.Now().UTC()), id)
		return err
	})
}

// ReleaseClaim 释放任务认领。
func (s *Store) ReleaseClaim(ctx context.Context, id string) error {
	return s.db.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `UPDATE tasks SET claimed_at = '', claim_token = '' WHERE task_id = ?`, id)
		return err
	})
}

// AdvanceMissed 仅推进下一次触发时间（用于错过执行留痕，不写 last_run_at）。
func (s *Store) AdvanceMissed(ctx context.Context, id string, next time.Time) error {
	return s.db.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `UPDATE tasks SET next_run_at = ?, updated_at = ? WHERE task_id = ?`,
			formatTime(next), formatTime(time.Now().UTC()), id)
		return err
	})
}

// SetState 启用/暂停任务；启用时必须给出下次触发时间。
func (s *Store) SetState(ctx context.Context, id, state string, next *time.Time) error {
	return s.db.Write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE tasks SET state = ?, next_run_at = ?, claimed_at = '', claim_token = '', updated_at = ?
			WHERE task_id = ?`, state, formatTimePtr(next), formatTime(time.Now().UTC()), id)
		if err != nil {
			return fmt.Errorf("更新任务状态失败: %w", err)
		}
		if n, err := res.RowsAffected(); err != nil {
			return err
		} else if n == 0 {
			return ErrNotFound
		}
		return nil
	})
}

// ClearClaims 在启动时清理进程崩溃遗留的认领（未完成的执行会另记 interrupted）。
func (s *Store) ClearClaims(ctx context.Context) error {
	return s.db.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `UPDATE tasks SET claimed_at = '', claim_token = '' WHERE claimed_at != ''`)
		return err
	})
}
