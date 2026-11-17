package host

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// DB 是主机包需要的最小存储能力，便于测试替换。
type DB interface {
	Write(ctx context.Context, fn func(tx *sql.Tx) error) error
	Read(ctx context.Context, fn func(tx *sql.Tx) error) error
}

// Store 封装 hosts / host_credentials / distributions 的 SQL（v4）。
// 表结构属于实现基线（OQ-041，docs/14）。
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

func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
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

// Create 在单事务内写入主机与其加密凭据。
func (s *Store) Create(ctx context.Context, h Host, ciphertext []byte) error {
	now := time.Now().UTC()
	h.CreatedAt, h.UpdatedAt = now, now
	if h.Status == "" {
		h.Status = StatusUnknown
	}
	return s.db.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO hosts
			(host_id, name, address, port, username, auth_method, host_key_fp, host_key_trusted,
			 status, last_test_at, last_test_error, created_by, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, '', 0, ?, '', '', ?, ?, ?)`,
			h.ID, h.Name, h.Address, h.Port, h.Username, h.AuthMethod, h.Status,
			h.CreatedBy, formatTime(h.CreatedAt), formatTime(h.UpdatedAt))
		if err != nil {
			return fmt.Errorf("写入主机 %s 失败: %w", h.ID, err)
		}
		if len(ciphertext) == 0 {
			return errors.New("新增主机必须同时写入加密凭据")
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO host_credentials (host_id, ciphertext, updated_at)
			VALUES (?, ?, ?)`, h.ID, ciphertext, formatTime(now))
		if err != nil {
			return fmt.Errorf("写入主机 %s 凭据失败: %w", h.ID, err)
		}
		return nil
	})
}

// Update 更新主机字段；ciphertext 为 nil 表示保留原凭据（替换语义见 DOC-09 §3）。
func (s *Store) Update(ctx context.Context, h Host, ciphertext []byte) error {
	h.UpdatedAt = time.Now().UTC()
	return s.db.Write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE hosts SET
			name = ?, address = ?, port = ?, username = ?, auth_method = ?, updated_at = ?
			WHERE host_id = ?`,
			h.Name, h.Address, h.Port, h.Username, h.AuthMethod, formatTime(h.UpdatedAt), h.ID)
		if err != nil {
			return fmt.Errorf("更新主机 %s 失败: %w", h.ID, err)
		}
		if n, err := res.RowsAffected(); err != nil {
			return err
		} else if n == 0 {
			return ErrNotFound
		}
		if ciphertext != nil {
			_, err = tx.ExecContext(ctx, `INSERT INTO host_credentials (host_id, ciphertext, updated_at)
				VALUES (?, ?, ?)
				ON CONFLICT(host_id) DO UPDATE SET ciphertext = excluded.ciphertext, updated_at = excluded.updated_at`,
				h.ID, ciphertext, formatTime(h.UpdatedAt))
			if err != nil {
				return fmt.Errorf("替换主机 %s 凭据失败: %w", h.ID, err)
			}
		}
		return nil
	})
}

// Delete 删除主机；凭据由外键级联删除。下发记录保留以便追溯。
func (s *Store) Delete(ctx context.Context, id string) error {
	return s.db.Write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `DELETE FROM hosts WHERE host_id = ?`, id)
		if err != nil {
			return fmt.Errorf("删除主机 %s 失败: %w", id, err)
		}
		if n, err := res.RowsAffected(); err != nil {
			return err
		} else if n == 0 {
			return ErrNotFound
		}
		return nil
	})
}

const hostColumns = `host_id, name, address, port, username, auth_method, host_key_fp, host_key_trusted,
	status, last_test_at, last_test_error, created_by, created_at, updated_at`

// Get 读取单个主机（不读凭据）。
func (s *Store) Get(ctx context.Context, id string) (Host, error) {
	var out Host
	err := s.db.Read(ctx, func(tx *sql.Tx) error {
		var h Host
		var trusted int
		var lastTest, created, updated string
		var configured int
		err := tx.QueryRowContext(ctx, `SELECT `+hostColumns+`,
			EXISTS(SELECT 1 FROM host_credentials c WHERE c.host_id = hosts.host_id)
			FROM hosts WHERE host_id = ?`, id).
			Scan(&h.ID, &h.Name, &h.Address, &h.Port, &h.Username, &h.AuthMethod,
				&h.HostKeyFP, &trusted, &h.Status, &lastTest, &h.LastTestError,
				&h.CreatedBy, &created, &updated, &configured)
		if err != nil {
			return err
		}
		h.HostKeyTrusted = trusted != 0
		h.CredentialConfigured = configured != 0
		h.LastTestAt = parseTime(lastTest)
		if t := parseTime(created); t != nil {
			h.CreatedAt = *t
		}
		if t := parseTime(updated); t != nil {
			h.UpdatedAt = *t
		}
		out = h
		return nil
	})
	if errors.Is(err, sql.ErrNoRows) {
		return Host{}, ErrNotFound
	}
	if err != nil {
		return Host{}, fmt.Errorf("读取主机 %s 失败: %w", id, err)
	}
	return out, nil
}

// List 返回全部主机，按名称排序。
func (s *Store) List(ctx context.Context) ([]Host, error) {
	var out []Host
	err := s.db.Read(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `SELECT `+hostColumns+`,
			EXISTS(SELECT 1 FROM host_credentials c WHERE c.host_id = hosts.host_id)
			FROM hosts ORDER BY name`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var h Host
			var trusted int
			var lastTest, created, updated string
			var configured int
			if err := rows.Scan(&h.ID, &h.Name, &h.Address, &h.Port, &h.Username, &h.AuthMethod,
				&h.HostKeyFP, &trusted, &h.Status, &lastTest, &h.LastTestError,
				&h.CreatedBy, &created, &updated, &configured); err != nil {
				return err
			}
			h.HostKeyTrusted = trusted != 0
			h.CredentialConfigured = configured != 0
			h.LastTestAt = parseTime(lastTest)
			if t := parseTime(created); t != nil {
				h.CreatedAt = *t
			}
			if t := parseTime(updated); t != nil {
				h.UpdatedAt = *t
			}
			out = append(out, h)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("列出主机失败: %w", err)
	}
	return out, nil
}

// CredentialCiphertext 返回主机凭据密文；调用方负责解密，且不得外泄。
func (s *Store) CredentialCiphertext(ctx context.Context, id string) ([]byte, error) {
	var out []byte
	err := s.db.Read(ctx, func(tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, `SELECT ciphertext FROM host_credentials WHERE host_id = ?`, id).Scan(&out)
	})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("读取主机 %s 凭据失败: %w", id, err)
	}
	return out, nil
}

// SetTestResult 记录一次连通性测试结果。
func (s *Store) SetTestResult(ctx context.Context, id, status, errMsg string, at time.Time) error {
	return s.db.Write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE hosts SET status = ?, last_test_at = ?, last_test_error = ?, updated_at = ?
			WHERE host_id = ?`, status, formatTime(at), errMsg, formatTime(at), id)
		if err != nil {
			return fmt.Errorf("更新主机 %s 测试结果失败: %w", id, err)
		}
		if n, err := res.RowsAffected(); err != nil {
			return err
		} else if n == 0 {
			return ErrNotFound
		}
		return nil
	})
}

// TrustFingerprint 首次信任并锁存主机公钥指纹（TOFU，DOC-09 SSH-02）。
func (s *Store) TrustFingerprint(ctx context.Context, id, fp string) error {
	return s.db.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `UPDATE hosts SET host_key_fp = ?, host_key_trusted = 1, updated_at = ?
			WHERE host_id = ?`, fp, formatTime(time.Now().UTC()), id)
		if err != nil {
			return fmt.Errorf("锁存主机 %s 指纹失败: %w", id, err)
		}
		return nil
	})
}

// CreateDistribution 幂等创建下发任务：同一 (host, plugin, version, key) 重复提交返回既有记录。
func (s *Store) CreateDistribution(ctx context.Context, d Distribution) (Distribution, error) {
	now := time.Now().UTC()
	if d.CreatedAt.IsZero() {
		d.CreatedAt = now
	}
	if d.State == "" {
		d.State = DistQueued
	}
	var created bool
	err := s.db.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO distributions
			(distribution_id, host_id, plugin_id, plugin_version, state, stage, error_category, error_message,
			 remote_path, idempotency_key, created_by, created_at, started_at, finished_at,
			 instance_id, cert_serial, started_ok)
			VALUES (?, ?, ?, ?, ?, '', '', '', '', ?, ?, ?, '', '', ?, ?, 0)`,
			d.ID, d.HostID, d.PluginID, d.PluginVersion, d.State, d.IdempotencyKey, d.CreatedBy, formatTime(d.CreatedAt),
			d.InstanceID, d.CertSerial)
		if err != nil {
			// 唯一约束冲突走幂等路径：读取既有记录返回。
			existing, getErr := s.getDistributionTx(ctx, tx, d.HostID, d.PluginID, d.PluginVersion, d.IdempotencyKey)
			if getErr != nil {
				return fmt.Errorf("创建下发任务失败: %w", err)
			}
			d = existing
			return nil
		}
		created = true
		return nil
	})
	if err != nil {
		return Distribution{}, err
	}
	if !created {
		return d, nil
	}
	d.Callable = false
	d.Note = DistNote
	return d, nil
}

func (s *Store) getDistributionTx(ctx context.Context, tx *sql.Tx, hostID, pluginID, version, key string) (Distribution, error) {
	var d Distribution
	var created, started, finished string
	err := tx.QueryRowContext(ctx, `SELECT `+distColumns+`
		FROM distributions WHERE host_id = ? AND plugin_id = ? AND plugin_version = ? AND idempotency_key = ?`,
		hostID, pluginID, version, key).
		Scan(&d.ID, &d.HostID, &d.PluginID, &d.PluginVersion, &d.State, &d.Stage,
			&d.ErrorCategory, &d.ErrorMessage, &d.RemotePath, &d.IdempotencyKey, &d.CreatedBy,
			&created, &started, &finished, &d.InstanceID, &d.CertSerial, &d.StartedOK)
	if err != nil {
		return Distribution{}, err
	}
	d.CreatedAt = derefTime(parseTime(created))
	d.StartedAt = parseTime(started)
	d.FinishedAt = parseTime(finished)
	d.Installed = d.State == DistInstalled || d.State == DistStarted || d.State == DistCallable
	d.Callable = d.State == DistCallable
	d.Note = DistNote
	return d, nil
}

func derefTime(t *time.Time) time.Time {
	if t == nil {
		return time.Time{}
	}
	return *t
}

const distColumns = `distribution_id, host_id, plugin_id, plugin_version, state, stage,
	error_category, error_message, remote_path, idempotency_key, created_by, created_at, started_at, finished_at,
	instance_id, cert_serial, started_ok`

func scanDistribution(scan func(dest ...any) error) (Distribution, error) {
	var d Distribution
	var created, started, finished string
	var startedOK int
	err := scan(&d.ID, &d.HostID, &d.PluginID, &d.PluginVersion, &d.State, &d.Stage,
		&d.ErrorCategory, &d.ErrorMessage, &d.RemotePath, &d.IdempotencyKey, &d.CreatedBy,
		&created, &started, &finished, &d.InstanceID, &d.CertSerial, &startedOK)
	if err != nil {
		return Distribution{}, err
	}
	d.CreatedAt = derefTime(parseTime(created))
	d.StartedAt = parseTime(started)
	d.FinishedAt = parseTime(finished)
	d.StartedOK = startedOK != 0
	d.Installed = d.State == DistInstalled || d.State == DistStarted || d.State == DistCallable
	d.Callable = d.State == DistCallable
	d.Note = DistNote
	return d, nil
}

// GetDistribution 读取单个下发任务。
func (s *Store) GetDistribution(ctx context.Context, id string) (Distribution, error) {
	var out Distribution
	err := s.db.Read(ctx, func(tx *sql.Tx) error {
		row := tx.QueryRowContext(ctx, `SELECT `+distColumns+` FROM distributions WHERE distribution_id = ?`, id)
		d, err := scanDistribution(row.Scan)
		if err != nil {
			return err
		}
		out = d
		return nil
	})
	if errors.Is(err, sql.ErrNoRows) {
		return Distribution{}, ErrNotFound
	}
	if err != nil {
		return Distribution{}, fmt.Errorf("读取下发任务 %s 失败: %w", id, err)
	}
	return out, nil
}

// ListDistributions 返回某主机的下发记录；hostID 为空时返回全部。
func (s *Store) ListDistributions(ctx context.Context, hostID string) ([]Distribution, error) {
	var out []Distribution
	err := s.db.Read(ctx, func(tx *sql.Tx) error {
		query := `SELECT ` + distColumns + ` FROM distributions`
		var args []any
		if hostID != "" {
			query += ` WHERE host_id = ?`
			args = append(args, hostID)
		}
		query += ` ORDER BY created_at DESC`
		rows, err := tx.QueryContext(ctx, query, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			d, err := scanDistribution(rows.Scan)
			if err != nil {
				return err
			}
			out = append(out, d)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("列出发下记录失败: %w", err)
	}
	return out, nil
}

// UpdateDistribution 更新下发任务的状态与阶段信息。
func (s *Store) UpdateDistribution(ctx context.Context, d Distribution) error {
	return s.db.Write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE distributions SET
			state = ?, stage = ?, error_category = ?, error_message = ?, remote_path = ?,
			started_at = ?, finished_at = ?, instance_id = ?, cert_serial = ?, started_ok = ?
			WHERE distribution_id = ?`,
			d.State, d.Stage, d.ErrorCategory, d.ErrorMessage, d.RemotePath,
			formatTime(derefTime(d.StartedAt)), formatTime(derefTime(d.FinishedAt)),
			d.InstanceID, d.CertSerial, boolInt(d.StartedOK), d.ID)
		if err != nil {
			return fmt.Errorf("更新下发任务 %s 失败: %w", d.ID, err)
		}
		if n, err := res.RowsAffected(); err != nil {
			return err
		} else if n == 0 {
			return ErrNotFound
		}
		return nil
	})
}

// MarkInterrupted 把重启后仍处于中间态的任务标记为中断（DIST-C-06）。
func (s *Store) MarkInterrupted(ctx context.Context) (int, error) {
	var affected int64
	err := s.db.Write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE distributions SET
			state = ?, stage = 'recovered', error_category = ?, error_message = '服务重启时下发未完成，已标记为中断，可显式重试',
			finished_at = ?
			WHERE state IN (?, ?, ?, ?)`,
			DistInterrupted, CatInterrupted, formatTime(time.Now().UTC()),
			DistConnecting, DistUploading, DistVerifying, DistInstalling)
		if err != nil {
			return err
		}
		affected, err = res.RowsAffected()
		return err
	})
	return int(affected), err
}

// CountDistributions 返回某主机的下发记录数，用于删除前的保护判断。
func (s *Store) CountDistributions(ctx context.Context, hostID string) (int, error) {
	var n int
	err := s.db.Read(ctx, func(tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, `SELECT count(*) FROM distributions WHERE host_id = ?`, hostID).Scan(&n)
	})
	return n, err
}

// CountDistributionsByState 按状态统计全部主机的下发记录，供总览 KPI 使用。
func (s *Store) CountDistributionsByState(ctx context.Context, state string) (int, error) {
	var n int
	err := s.db.Read(ctx, func(tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, `SELECT count(*) FROM distributions WHERE state = ?`, state).Scan(&n)
	})
	return n, err
}
