package instances

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"operations-platform/internal/pki"
)

// DB 是实例存储需要的最小事务能力，便于测试替换。
type DB interface {
	Write(ctx context.Context, fn func(tx *sql.Tx) error) error
	Read(ctx context.Context, fn func(tx *sql.Tx) error) error
}

// ErrNotFound 表示类型或实例不存在。
var ErrNotFound = errors.New("插件类型或实例未登记")

// Store 封装类型/实例/配置/探测/证书/注册事件的持久化。
type Store struct {
	st  DB
	now func() time.Time
}

// NewStore 创建存储访问器。
func NewStore(st DB) *Store {
	return &Store{st: st, now: func() time.Time { return time.Now().UTC() }}
}

func ts(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

func parseTS(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	t, _ := time.Parse(time.RFC3339Nano, s)
	return t
}

// UpsertType 幂等登记类型与能力清单；清单摘要未变化时不写库。
func (s *Store) UpsertType(ctx context.Context, t Type, caps []Capability) (bool, error) {
	changed := false
	now := s.now()
	err := s.st.Write(ctx, func(tx *sql.Tx) error {
		var sha string
		err := tx.QueryRow(`SELECT manifest_sha256 FROM plugin_types WHERE type_id = ?`, t.TypeID).Scan(&sha)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			_, err = tx.Exec(`INSERT INTO plugin_types
				(type_id, display_name, description, form, latest_version, custody, manifest_yaml,
				 manifest_sha256, contract_version, contract_range, runtime_port, config_schema_json,
				 created_at, updated_at)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				t.TypeID, t.DisplayName, t.Description, t.Form, t.LatestVersion, t.Custody,
				t.ManifestYAML, t.ManifestSHA256, t.ContractVersion, t.ContractRange,
				t.RuntimePort, t.ConfigSchema, ts(now), ts(now))
			if err != nil {
				return fmt.Errorf("登记插件类型 %s 失败: %w", t.TypeID, err)
			}
			changed = true
		case err != nil:
			return fmt.Errorf("查询插件类型 %s 失败: %w", t.TypeID, err)
		case sha == t.ManifestSHA256:
			return nil
		default:
			_, err = tx.Exec(`UPDATE plugin_types SET
				display_name = ?, description = ?, form = ?, latest_version = ?, custody = ?,
				manifest_yaml = ?, manifest_sha256 = ?, contract_version = ?, contract_range = ?,
				runtime_port = ?, config_schema_json = ?, updated_at = ?
				WHERE type_id = ?`,
				t.DisplayName, t.Description, t.Form, t.LatestVersion, t.Custody,
				t.ManifestYAML, t.ManifestSHA256, t.ContractVersion, t.ContractRange,
				t.RuntimePort, t.ConfigSchema, ts(now), t.TypeID)
			if err != nil {
				return fmt.Errorf("更新插件类型 %s 失败: %w", t.TypeID, err)
			}
			changed = true
		}
		if !changed {
			return nil
		}
		if _, err := tx.Exec(`DELETE FROM plugin_type_capabilities WHERE type_id = ?`, t.TypeID); err != nil {
			return fmt.Errorf("清理能力清单失败: %w", err)
		}
		for _, c := range caps {
			if _, err := tx.Exec(`INSERT INTO plugin_type_capabilities
				(type_id, capability_id, description, input_schema_ref, output_schema_ref,
				 idempotent, long_running, supports_cancel, concurrency, sensitive, risk_level,
				 suggested_timeout_ms)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				c.TypeID, c.CapabilityID, c.Description, c.InputSchemaRef, c.OutputSchemaRef,
				boolInt(c.Idempotent), boolInt(c.LongRunning), boolInt(c.SupportsCancel),
				c.Concurrency, boolInt(c.Sensitive), defaultString(c.RiskLevel, "normal"),
				c.SuggestedTimeoutMS); err != nil {
				return fmt.Errorf("写入能力 %s 失败: %w", c.CapabilityID, err)
			}
		}
		return nil
	})
	return changed, err
}

// GetType 读取类型。
func (s *Store) GetType(ctx context.Context, typeID string) (Type, error) {
	var t Type
	var created, updated string
	err := s.st.Read(ctx, func(tx *sql.Tx) error {
		return tx.QueryRow(`SELECT type_id, display_name, description, form, latest_version, custody,
			manifest_yaml, manifest_sha256, contract_version, contract_range, runtime_port,
			config_schema_json, created_at, updated_at FROM plugin_types WHERE type_id = ?`, typeID).
			Scan(&t.TypeID, &t.DisplayName, &t.Description, &t.Form, &t.LatestVersion, &t.Custody,
				&t.ManifestYAML, &t.ManifestSHA256, &t.ContractVersion, &t.ContractRange,
				&t.RuntimePort, &t.ConfigSchema, &created, &updated)
	})
	if errors.Is(err, sql.ErrNoRows) {
		return Type{}, ErrNotFound
	}
	if err != nil {
		return Type{}, fmt.Errorf("读取插件类型 %s 失败: %w", typeID, err)
	}
	t.CreatedAt, t.UpdatedAt = parseTS(created), parseTS(updated)
	return t, nil
}

// ListTypes 返回全部类型，按标识排序。
func (s *Store) ListTypes(ctx context.Context) ([]Type, error) {
	var out []Type
	err := s.st.Read(ctx, func(tx *sql.Tx) error {
		rows, err := tx.Query(`SELECT type_id, display_name, description, form, latest_version, custody,
			manifest_yaml, manifest_sha256, contract_version, contract_range, runtime_port,
			config_schema_json, created_at, updated_at FROM plugin_types ORDER BY type_id`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var t Type
			var created, updated string
			if err := rows.Scan(&t.TypeID, &t.DisplayName, &t.Description, &t.Form, &t.LatestVersion,
				&t.Custody, &t.ManifestYAML, &t.ManifestSHA256, &t.ContractVersion, &t.ContractRange,
				&t.RuntimePort, &t.ConfigSchema, &created, &updated); err != nil {
				return err
			}
			t.CreatedAt, t.UpdatedAt = parseTS(created), parseTS(updated)
			out = append(out, t)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("列出插件类型失败: %w", err)
	}
	return out, nil
}

// ListCapabilities 返回某类型的能力清单。
func (s *Store) ListCapabilities(ctx context.Context, typeID string) ([]Capability, error) {
	var out []Capability
	err := s.st.Read(ctx, func(tx *sql.Tx) error {
		rows, err := tx.Query(`SELECT type_id, capability_id, description, input_schema_ref,
			output_schema_ref, idempotent, long_running, supports_cancel, concurrency, sensitive,
			risk_level, suggested_timeout_ms FROM plugin_type_capabilities
			WHERE type_id = ? ORDER BY capability_id`, typeID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var c Capability
			var idem, long, cancel, sens int
			if err := rows.Scan(&c.TypeID, &c.CapabilityID, &c.Description, &c.InputSchemaRef,
				&c.OutputSchemaRef, &idem, &long, &cancel, &c.Concurrency, &sens, &c.RiskLevel,
				&c.SuggestedTimeoutMS); err != nil {
				return err
			}
			c.Idempotent, c.LongRunning, c.SupportsCancel, c.Sensitive = idem != 0, long != 0, cancel != 0, sens != 0
			out = append(out, c)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("列出能力失败: %w", err)
	}
	return out, nil
}

// UpsertLocalInstance 幂等创建本机实例；安装后默认禁用（DOC-04 §3.3）。
// 版本变化时保留启用状态，但把实例拉回 installed 并清空 callable，等待重新探测。
func (s *Store) UpsertLocalInstance(ctx context.Context, typeID, version string) (Instance, bool, error) {
	id := LocalInstanceID(typeID)
	created := false
	now := s.now()
	err := s.st.Write(ctx, func(tx *sql.Tx) error {
		var curVersion string
		err := tx.QueryRow(`SELECT package_version FROM plugin_instances WHERE instance_id = ?`, id).Scan(&curVersion)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			_, err = tx.Exec(`INSERT INTO plugin_instances
				(instance_id, type_id, host_id, custody, package_version, enabled, installed,
				 started, callable, state, state_reason, created_at, updated_at)
				VALUES (?, ?, '', ?, ?, 0, 1, 0, 0, ?, '', ?, ?)`,
				id, typeID, CustodyLocal, version, StateInstalled, ts(now), ts(now))
			if err != nil {
				return fmt.Errorf("创建本机实例 %s 失败: %w", id, err)
			}
			created = true
			return nil
		case err != nil:
			return fmt.Errorf("查询实例 %s 失败: %w", id, err)
		case curVersion == version:
			return nil
		default:
			_, err = tx.Exec(`UPDATE plugin_instances SET package_version = ?, installed = 1,
				callable = 0, state = ?, state_reason = ?, drift = '', updated_at = ? WHERE instance_id = ?`,
				version, StateInstalled, "版本变化，等待重新启用", ts(now), id)
			if err != nil {
				return fmt.Errorf("更新实例 %s 版本失败: %w", id, err)
			}
			return nil
		}
	})
	if err != nil {
		return Instance{}, false, err
	}
	inst, err := s.GetInstance(ctx, id)
	return inst, created, err
}

// UpsertRemoteInstance 创建或更新远端实例（下发安装时调用）。
func (s *Store) UpsertRemoteInstance(ctx context.Context, inst Instance) error {
	now := s.now()
	if inst.InstanceID == "" {
		inst.InstanceID = RemoteInstanceID(inst.HostID, inst.TypeID)
	}
	return s.st.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO plugin_instances
			(instance_id, type_id, host_id, custody, package_version, listen_port, enabled, installed,
			 started, callable, state, state_reason, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(instance_id) DO UPDATE SET
				package_version = excluded.package_version,
				listen_port = excluded.listen_port,
				installed = excluded.installed,
				started = excluded.started,
				callable = excluded.callable,
				state = excluded.state,
				state_reason = excluded.state_reason,
				last_error = '',
				updated_at = excluded.updated_at`,
			inst.InstanceID, inst.TypeID, inst.HostID, CustodyRemote, inst.PackageVersion,
			inst.ListenPort, boolInt(inst.Enabled), boolInt(inst.Installed), boolInt(inst.Started),
			boolInt(inst.Callable), inst.State, inst.StateReason, ts(now), ts(now))
		if err != nil {
			return fmt.Errorf("写入远端实例 %s 失败: %w", inst.InstanceID, err)
		}
		return nil
	})
}

// GetInstance 读取实例。
func (s *Store) GetInstance(ctx context.Context, instanceID string) (Instance, error) {
	var i Instance
	var enabled, installed, started, callable int
	var created, updated, certNotAfter, probeAt string
	err := s.st.Read(ctx, func(tx *sql.Tx) error {
		return tx.QueryRow(`SELECT instance_id, type_id, host_id, custody, package_version, listen_port,
			enabled, installed, started, callable, state, state_reason, drift, cert_serial,
			cert_fingerprint, cert_not_after, last_probe_at, last_probe_result, last_probe_reason,
			last_error, created_at, updated_at FROM plugin_instances WHERE instance_id = ?`, instanceID).
			Scan(&i.InstanceID, &i.TypeID, &i.HostID, &i.Custody, &i.PackageVersion, &i.ListenPort,
				&enabled, &installed, &started, &callable, &i.State, &i.StateReason, &i.Drift,
				&i.CertSerial, &i.CertFingerprint, &certNotAfter, &probeAt, &i.LastProbeResult,
				&i.LastProbeReason, &i.LastError, &created, &updated)
	})
	if errors.Is(err, sql.ErrNoRows) {
		return Instance{}, ErrNotFound
	}
	if err != nil {
		return Instance{}, fmt.Errorf("读取实例 %s 失败: %w", instanceID, err)
	}
	i.Enabled, i.Installed, i.Started, i.Callable = enabled != 0, installed != 0, started != 0, callable != 0
	i.CertNotAfter, i.LastProbeAt = parseTS(certNotAfter), parseTS(probeAt)
	i.CreatedAt, i.UpdatedAt = parseTS(created), parseTS(updated)
	return i, nil
}

// ListInstances 按条件列出实例。
func (s *Store) ListInstances(ctx context.Context, f InstanceFilter) ([]Instance, error) {
	var where []string
	var args []any
	if f.TypeID != "" {
		where = append(where, "type_id = ?")
		args = append(args, f.TypeID)
	}
	if f.HostID != "" {
		where = append(where, "host_id = ?")
		args = append(args, f.HostID)
	}
	if f.Custody != "" {
		where = append(where, "custody = ?")
		args = append(args, f.Custody)
	}
	if f.State != "" {
		where = append(where, "state = ?")
		args = append(args, f.State)
	}
	query := `SELECT instance_id, type_id, host_id, custody, package_version, listen_port,
		enabled, installed, started, callable, state, state_reason, drift, cert_serial,
		cert_fingerprint, cert_not_after, last_probe_at, last_probe_result, last_probe_reason,
		last_error, created_at, updated_at FROM plugin_instances`
	if len(where) > 0 {
		query += " WHERE " + strings.Join(where, " AND ")
	}
	query += " ORDER BY instance_id"

	var out []Instance
	err := s.st.Read(ctx, func(tx *sql.Tx) error {
		rows, err := tx.Query(query, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var i Instance
			var enabled, installed, started, callable int
			var created, updated, certNotAfter, probeAt string
			if err := rows.Scan(&i.InstanceID, &i.TypeID, &i.HostID, &i.Custody, &i.PackageVersion,
				&i.ListenPort, &enabled, &installed, &started, &callable, &i.State, &i.StateReason,
				&i.Drift, &i.CertSerial, &i.CertFingerprint, &certNotAfter, &probeAt,
				&i.LastProbeResult, &i.LastProbeReason, &i.LastError, &created, &updated); err != nil {
				return err
			}
			i.Enabled, i.Installed, i.Started, i.Callable = enabled != 0, installed != 0, started != 0, callable != 0
			i.CertNotAfter, i.LastProbeAt = parseTS(certNotAfter), parseTS(probeAt)
			i.CreatedAt, i.UpdatedAt = parseTS(created), parseTS(updated)
			out = append(out, i)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("列出实例失败: %w", err)
	}
	return out, nil
}

// SetEnabled 更新启用状态，并按托管方式推导三态。
func (s *Store) SetEnabled(ctx context.Context, instanceID string, enabled bool) error {
	now := s.now()
	return s.st.Write(ctx, func(tx *sql.Tx) error {
		var custody, state string
		var installed int
		if err := tx.QueryRow(`SELECT custody, state, installed FROM plugin_instances WHERE instance_id = ?`,
			instanceID).Scan(&custody, &state, &installed); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrNotFound
			}
			return err
		}
		newState := StateDisabled
		callable, started := 0, 0
		if enabled {
			started = 1
			if custody == CustodyLocal {
				// 命令式插件：启用即可调用（仍以探测结果复核）。
				newState, callable = StateCallable, 1
			} else {
				// 远端实例启用不等于可调用；必须等探测通过。
				if state == StateCallable {
					newState, callable = StateCallable, 1
				} else {
					newState = StateStarted
				}
			}
		}
		if installed == 0 {
			newState, callable = StateInstalled, 0
			if enabled {
				newState = StateStartFailed
			}
		}
		_, err := tx.Exec(`UPDATE plugin_instances SET enabled = ?, started = ?, callable = ?,
			state = ?, state_reason = '', updated_at = ? WHERE instance_id = ?`,
			boolInt(enabled), started, callable, newState, ts(now), instanceID)
		if err != nil {
			return fmt.Errorf("更新实例 %s 启用状态失败: %w", instanceID, err)
		}
		return nil
	})
}

// UpdateStatus 是探测/下发后的状态汇总写入。
func (s *Store) UpdateStatus(ctx context.Context, instanceID, state, reason, drift, lastErr string,
	installed, started, callable bool) error {
	now := s.now()
	return s.st.Write(ctx, func(tx *sql.Tx) error {
		res, err := tx.Exec(`UPDATE plugin_instances SET state = ?, state_reason = ?, drift = ?,
			last_error = ?, installed = ?, started = ?, callable = ?, updated_at = ? WHERE instance_id = ?`,
			state, reason, drift, lastErr, boolInt(installed), boolInt(started), boolInt(callable),
			ts(now), instanceID)
		if err != nil {
			return fmt.Errorf("更新实例 %s 状态失败: %w", instanceID, err)
		}
		n, _ := res.RowsAffected()
		if n == 0 {
			return ErrNotFound
		}
		return nil
	})
}

// RecordProbe 写入探测结果并同步实例的最后探测信息。
func (s *Store) RecordProbe(ctx context.Context, p Probe) error {
	now := s.now()
	if p.ProbeAt.IsZero() {
		p.ProbeAt = now
	}
	return s.st.Write(ctx, func(tx *sql.Tx) error {
		if _, err := tx.Exec(`INSERT INTO instance_probes (instance_id, probe_at, stage, ok, latency_ms, detail)
			VALUES (?, ?, ?, ?, ?, ?)`,
			p.InstanceID, ts(p.ProbeAt), p.Stage, boolInt(p.OK), p.LatencyMS, p.Detail); err != nil {
			return fmt.Errorf("写入探测记录失败: %w", err)
		}
		result := ProbeFailed
		if p.OK {
			result = ProbeOK
		}
		_, err := tx.Exec(`UPDATE plugin_instances SET last_probe_at = ?, last_probe_result = ?,
			last_probe_reason = ?, updated_at = ? WHERE instance_id = ?`,
			ts(p.ProbeAt), result, p.Detail, ts(now), p.InstanceID)
		if err != nil {
			return fmt.Errorf("更新实例探测信息失败: %w", err)
		}
		return nil
	})
}

// ListProbes 返回实例最近探测记录。
func (s *Store) ListProbes(ctx context.Context, instanceID string, limit int) ([]Probe, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	var out []Probe
	err := s.st.Read(ctx, func(tx *sql.Tx) error {
		rows, err := tx.Query(`SELECT id, instance_id, probe_at, stage, ok, latency_ms, detail
			FROM instance_probes WHERE instance_id = ? ORDER BY probe_at DESC LIMIT ?`, instanceID, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var p Probe
			var at string
			var ok int
			if err := rows.Scan(&p.ID, &p.InstanceID, &at, &p.Stage, &ok, &p.LatencyMS, &p.Detail); err != nil {
				return err
			}
			p.ProbeAt, p.OK = parseTS(at), ok != 0
			out = append(out, p)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("列出探测记录失败: %w", err)
	}
	return out, nil
}

// SetConfig 写入实例配置密文；updated_by 为操作主体。
func (s *Store) SetConfig(ctx context.Context, instanceID, key string, ciphertext []byte, actor string) error {
	now := s.now()
	return s.st.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO plugin_instance_configs
			(instance_id, config_key, ciphertext, updated_at, updated_by) VALUES (?, ?, ?, ?, ?)
			ON CONFLICT(instance_id, config_key) DO UPDATE SET
				ciphertext = excluded.ciphertext, updated_at = excluded.updated_at, updated_by = excluded.updated_by`,
			instanceID, key, ciphertext, ts(now), actor)
		if err != nil {
			return fmt.Errorf("写入实例配置失败: %w", err)
		}
		return nil
	})
}

// DeleteConfig 删除实例配置项（用于恢复默认）。
func (s *Store) DeleteConfig(ctx context.Context, instanceID, key string) error {
	return s.st.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec(`DELETE FROM plugin_instance_configs WHERE instance_id = ? AND config_key = ?`,
			instanceID, key)
		return err
	})
}

// ListConfig 返回实例配置密文行；调用方负责解密，接口不得直接输出。
func (s *Store) ListConfig(ctx context.Context, instanceID string) ([]ConfigEntry, error) {
	var out []ConfigEntry
	err := s.st.Read(ctx, func(tx *sql.Tx) error {
		rows, err := tx.Query(`SELECT instance_id, config_key, ciphertext, updated_at, updated_by
			FROM plugin_instance_configs WHERE instance_id = ? ORDER BY config_key`, instanceID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var e ConfigEntry
			var at string
			if err := rows.Scan(&e.InstanceID, &e.Key, &e.Ciphertext, &at, &e.UpdatedBy); err != nil {
				return err
			}
			e.UpdatedAt = parseTS(at)
			out = append(out, e)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("读取实例配置失败: %w", err)
	}
	return out, nil
}

// SetCert 把证书序列号与到期时间登记到实例。
func (s *Store) SetCert(ctx context.Context, instanceID, serial, fingerprint string, notAfter time.Time) error {
	now := s.now()
	return s.st.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE plugin_instances SET cert_serial = ?, cert_fingerprint = ?,
			cert_not_after = ?, updated_at = ? WHERE instance_id = ?`,
			serial, fingerprint, ts(notAfter), ts(now), instanceID)
		return err
	})
}

// SaveCertificate 实现 pki.Store。
func (s *Store) SaveCertificate(ctx context.Context, b pki.Binding) error {
	return s.st.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO cert_bindings
			(serial, fingerprint, purpose, plugin_id, host_id, instance_id, subject, san, not_before,
			 not_after, cert_pem, key_ciphertext, revoked_at, revoked_reason, issued_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, '', '', ?)`,
			b.Serial, b.Fingerprint, b.Purpose, b.PluginID, b.HostID, b.InstanceID, b.Subject, b.SAN,
			ts(b.NotBefore), ts(b.NotAfter), b.CertPEM, b.KeyCiphertext, ts(b.IssuedAt))
		if err != nil {
			return fmt.Errorf("保存证书绑定失败: %w", err)
		}
		return nil
	})
}

// GetCertificate 实现 pki.Store。
func (s *Store) GetCertificate(ctx context.Context, serial string) (pki.Binding, error) {
	var b pki.Binding
	var notBefore, notAfter, revokedAt, issuedAt string
	err := s.st.Read(ctx, func(tx *sql.Tx) error {
		return tx.QueryRow(`SELECT serial, fingerprint, purpose, plugin_id, host_id, instance_id,
			subject, san, not_before, not_after, cert_pem, key_ciphertext, revoked_at,
			revoked_reason, issued_at FROM cert_bindings WHERE serial = ?`, serial).
			Scan(&b.Serial, &b.Fingerprint, &b.Purpose, &b.PluginID, &b.HostID, &b.InstanceID,
				&b.Subject, &b.SAN, &notBefore, &notAfter, &b.CertPEM, &b.KeyCiphertext,
				&revokedAt, &b.RevokedReason, &issuedAt)
	})
	if errors.Is(err, sql.ErrNoRows) {
		return pki.Binding{}, pki.ErrCertificateNotFound
	}
	if err != nil {
		return pki.Binding{}, fmt.Errorf("读取证书失败: %w", err)
	}
	b.NotBefore, b.NotAfter, b.RevokedAt, b.IssuedAt = parseTS(notBefore), parseTS(notAfter), parseTS(revokedAt), parseTS(issuedAt)
	return b, nil
}

// FindActiveCertificate 实现 pki.Store：返回实例下未吊销且最新的证书。
func (s *Store) FindActiveCertificate(ctx context.Context, instanceID, purpose string) (pki.Binding, error) {
	var b pki.Binding
	var notBefore, notAfter, revokedAt, issuedAt string
	err := s.st.Read(ctx, func(tx *sql.Tx) error {
		return tx.QueryRow(`SELECT serial, fingerprint, purpose, plugin_id, host_id, instance_id,
			subject, san, not_before, not_after, cert_pem, key_ciphertext, revoked_at,
			revoked_reason, issued_at FROM cert_bindings
			WHERE instance_id = ? AND purpose = ? AND revoked_at = ''
			ORDER BY issued_at DESC LIMIT 1`, instanceID, purpose).
			Scan(&b.Serial, &b.Fingerprint, &b.Purpose, &b.PluginID, &b.HostID, &b.InstanceID,
				&b.Subject, &b.SAN, &notBefore, &notAfter, &b.CertPEM, &b.KeyCiphertext,
				&revokedAt, &b.RevokedReason, &issuedAt)
	})
	if errors.Is(err, sql.ErrNoRows) {
		return pki.Binding{}, pki.ErrCertificateNotFound
	}
	if err != nil {
		return pki.Binding{}, fmt.Errorf("读取实例证书失败: %w", err)
	}
	b.NotBefore, b.NotAfter, b.RevokedAt, b.IssuedAt = parseTS(notBefore), parseTS(notAfter), parseTS(revokedAt), parseTS(issuedAt)
	return b, nil
}

// RevokeCertificate 实现 pki.Store；重复吊销保持幂等。
func (s *Store) RevokeCertificate(ctx context.Context, serial, reason string, at time.Time) error {
	return s.st.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE cert_bindings SET revoked_at = ?, revoked_reason = ?
			WHERE serial = ? AND revoked_at = ''`, ts(at), reason, serial)
		if err != nil {
			return fmt.Errorf("吊销证书失败: %w", err)
		}
		return nil
	})
}

// AppendEvent 写入注册/状态事件。
func (s *Store) AppendEvent(ctx context.Context, e RegistrationEvent) error {
	if e.OccurredAt.IsZero() {
		e.OccurredAt = s.now()
	}
	if e.Details == "" {
		e.Details = "{}"
	}
	return s.st.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO registration_events
			(occurred_at, action, type_id, instance_id, host_id, actor, result, reason, details)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			ts(e.OccurredAt), e.Action, e.TypeID, e.InstanceID, e.HostID, e.Actor, e.Result, e.Reason, e.Details)
		if err != nil {
			return fmt.Errorf("写入注册事件失败: %w", err)
		}
		return nil
	})
}

// ListEvents 返回最近的注册事件。
func (s *Store) ListEvents(ctx context.Context, limit int) ([]RegistrationEvent, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	var out []RegistrationEvent
	err := s.st.Read(ctx, func(tx *sql.Tx) error {
		rows, err := tx.Query(`SELECT id, occurred_at, action, type_id, instance_id, host_id, actor,
			result, reason, details FROM registration_events ORDER BY occurred_at DESC, id DESC LIMIT ?`, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var e RegistrationEvent
			var at string
			if err := rows.Scan(&e.ID, &at, &e.Action, &e.TypeID, &e.InstanceID, &e.HostID,
				&e.Actor, &e.Result, &e.Reason, &e.Details); err != nil {
				return err
			}
			e.OccurredAt = parseTS(at)
			out = append(out, e)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("列出注册事件失败: %w", err)
	}
	return out, nil
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func defaultString(v, fallback string) string {
	if v == "" {
		return fallback
	}
	return v
}
