// Package registry 维护插件注册表与执行记录（SQLite v2）。
//
// 所有写操作经 store.Store.Write；查询经 store.Store.Read，禁止调用方直接持有 *sql.DB。
// 表结构属于实现基线（OQ-039，docs/12）。
package registry

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// 插件状态只允许两种持久状态：安装后默认禁用（ADR-0003）。
const (
	StateDisabled = "disabled"
	StateEnabled  = "enabled"
)

// 执行结果状态。
const (
	StatusOK          = "ok"
	StatusError       = "error"
	StatusTimeout     = "timeout"
	StatusSkipped     = "skipped"
	StatusMissed      = "missed"
	StatusInterrupted = "interrupted"
)

// 调用来源。
const (
	SourceManual    = "manual"
	SourceScheduled = "scheduled"
	SourceAPI       = "api"
)

// ErrNotFound 表示插件不存在。
var ErrNotFound = errors.New("插件未登记")

// Plugin 是注册表中的一行。
type Plugin struct {
	ID             string    `json:"plugin_id"`
	Version        string    `json:"plugin_version"`
	Form           string    `json:"form"`
	State          string    `json:"state"`
	InstallPath    string    `json:"install_path"`
	ManifestYAML   string    `json:"-"`
	ManifestSHA256 string    `json:"manifest_sha256"`
	ContractOK     bool      `json:"contract_ok"`
	ContractReason string    `json:"contract_reason,omitempty"`
	DiscoveredAt   time.Time `json:"discovered_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

// Execution 对应 plugin_executions 一行。
type Execution struct {
	InvocationID  string    `json:"invocation_id"`
	PluginID      string    `json:"plugin_id"`
	CapabilityID  string    `json:"capability_id"`
	Source        string    `json:"source"`
	Subject       string    `json:"subject"`
	StartedAt     time.Time `json:"started_at"`
	FinishedAt    time.Time `json:"finished_at"`
	DurationMS    int64     `json:"duration_ms"`
	Status        string    `json:"status"`
	ErrorCategory string    `json:"error_category,omitempty"`
	TaskID        string    `json:"task_id,omitempty"`
	// InstanceID 是 M6 的实例维度；本地实例为 local:<plugin_id>，远端为 remote:<host>:<plugin>。
	InstanceID string `json:"instance_id,omitempty"`
	// InputSummary / OutputSummary 是可展示的截断摘要（M5，OQ-046）；
	// 完整输入输出不落库，避免执行记录变成敏感数据仓库。
	InputSummary  string `json:"input_summary,omitempty"`
	OutputSummary string `json:"output_summary,omitempty"`
}

// ExecutionFilter 是执行记录查询条件；零值表示不过滤。
type ExecutionFilter struct {
	PluginID     string
	CapabilityID string
	Status       string
	Source       string
	TaskID       string
	InstanceID   string
	From         time.Time
	To           time.Time
	Limit        int
}

// DB 是注册表需要的最小存储能力，便于测试替换。
type DB interface {
	Write(ctx context.Context, fn func(tx *sql.Tx) error) error
	Read(ctx context.Context, fn func(tx *sql.Tx) error) error
}

// Registry 封装插件注册表与执行记录持久化。
type Registry struct {
	st DB
}

// New 创建注册表访问器。
func New(st DB) *Registry {
	return &Registry{st: st}
}

// Action 描述一次登记的结果。
type Action string

const (
	ActionInserted  Action = "inserted"
	ActionUpdated   Action = "updated"
	ActionUnchanged Action = "unchanged"
)

// Upsert 幂等登记：摘要未变不写库；摘要变化只更新清单与版本，状态保持不变。
func (r *Registry) Upsert(ctx context.Context, p Plugin) (Action, error) {
	action := ActionUnchanged
	now := time.Now().UTC()
	if p.DiscoveredAt.IsZero() {
		p.DiscoveredAt = now
	}
	p.UpdatedAt = now

	err := r.st.Write(ctx, func(tx *sql.Tx) error {
		var sha string
		err := tx.QueryRow(`SELECT manifest_sha256 FROM plugins WHERE plugin_id = ?`, p.ID).Scan(&sha)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			_, err = tx.Exec(`INSERT INTO plugins
				(plugin_id, plugin_version, form, state, install_path, manifest_yaml, manifest_sha256,
				 contract_ok, contract_reason, discovered_at, updated_at)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				p.ID, p.Version, p.Form, StateDisabled, p.InstallPath, p.ManifestYAML, p.ManifestSHA256,
				boolToInt(p.ContractOK), p.ContractReason,
				p.DiscoveredAt.Format(time.RFC3339Nano), p.UpdatedAt.Format(time.RFC3339Nano))
			if err != nil {
				return fmt.Errorf("登记插件 %s 失败: %w", p.ID, err)
			}
			action = ActionInserted
			return nil
		case err != nil:
			return fmt.Errorf("查询插件 %s 失败: %w", p.ID, err)
		case sha == p.ManifestSHA256:
			// 幂等：同样内容不产生任何写入（也不改 updated_at）。
			return nil
		default:
			_, err = tx.Exec(`UPDATE plugins SET
				plugin_version = ?, form = ?, install_path = ?, manifest_yaml = ?, manifest_sha256 = ?,
				contract_ok = ?, contract_reason = ?, updated_at = ?
				WHERE plugin_id = ?`,
				p.Version, p.Form, p.InstallPath, p.ManifestYAML, p.ManifestSHA256,
				boolToInt(p.ContractOK), p.ContractReason, p.UpdatedAt.Format(time.RFC3339Nano), p.ID)
			if err != nil {
				return fmt.Errorf("更新插件 %s 失败: %w", p.ID, err)
			}
			action = ActionUpdated
			return nil
		}
	})
	if err != nil {
		return "", err
	}
	return action, nil
}

// Get 读取单个插件。
func (r *Registry) Get(ctx context.Context, id string) (Plugin, error) {
	var p Plugin
	var contractOK int
	var discovered, updated string
	err := r.st.Read(ctx, func(tx *sql.Tx) error {
		return tx.QueryRow(`SELECT plugin_id, plugin_version, form, state, install_path, manifest_yaml,
			manifest_sha256, contract_ok, contract_reason, discovered_at, updated_at
			FROM plugins WHERE plugin_id = ?`, id).
			Scan(&p.ID, &p.Version, &p.Form, &p.State, &p.InstallPath, &p.ManifestYAML,
				&p.ManifestSHA256, &contractOK, &p.ContractReason, &discovered, &updated)
	})
	if errors.Is(err, sql.ErrNoRows) {
		return Plugin{}, ErrNotFound
	}
	if err != nil {
		return Plugin{}, fmt.Errorf("读取插件 %s 失败: %w", id, err)
	}
	p.ContractOK = contractOK != 0
	p.DiscoveredAt, _ = time.Parse(time.RFC3339Nano, discovered)
	p.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updated)
	return p, nil
}

// List 返回全部已登记插件，按标识排序。
func (r *Registry) List(ctx context.Context) ([]Plugin, error) {
	var out []Plugin
	err := r.st.Read(ctx, func(tx *sql.Tx) error {
		rows, err := tx.Query(`SELECT plugin_id, plugin_version, form, state, install_path, manifest_yaml,
			manifest_sha256, contract_ok, contract_reason, discovered_at, updated_at
			FROM plugins ORDER BY plugin_id`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var p Plugin
			var contractOK int
			var discovered, updated string
			if err := rows.Scan(&p.ID, &p.Version, &p.Form, &p.State, &p.InstallPath, &p.ManifestYAML,
				&p.ManifestSHA256, &contractOK, &p.ContractReason, &discovered, &updated); err != nil {
				return err
			}
			p.ContractOK = contractOK != 0
			p.DiscoveredAt, _ = time.Parse(time.RFC3339Nano, discovered)
			p.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updated)
			out = append(out, p)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("列出插件失败: %w", err)
	}
	return out, nil
}

// SetState 更新插件状态；状态机只允许 disabled ↔ enabled。
func (r *Registry) SetState(ctx context.Context, id, state string) error {
	if state != StateDisabled && state != StateEnabled {
		return fmt.Errorf("非法插件状态 %q", state)
	}
	return r.st.Write(ctx, func(tx *sql.Tx) error {
		res, err := tx.Exec(`UPDATE plugins SET state = ?, updated_at = ? WHERE plugin_id = ?`,
			state, time.Now().UTC().Format(time.RFC3339Nano), id)
		if err != nil {
			return fmt.Errorf("更新插件 %s 状态失败: %w", id, err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if n == 0 {
			return ErrNotFound
		}
		return nil
	})
}

// EnqueuePayload 把执行记录序列化为队列载荷。
func (e Execution) EnqueuePayload() ([]byte, error) { return json.Marshal(e) }

// CountByTask 统计某任务在指定状态、起始时间之后的执行次数（M4 错过提示）。
func (r *Registry) CountByTask(ctx context.Context, taskID, status string, from time.Time) (int, error) {
	var n int
	err := r.st.Read(ctx, func(tx *sql.Tx) error {
		return tx.QueryRow(`SELECT count(*) FROM plugin_executions
			WHERE task_id = ? AND status = ? AND started_at >= ?`,
			taskID, status, from.UTC().Format(time.RFC3339Nano)).Scan(&n)
	})
	if err != nil {
		return 0, fmt.Errorf("统计任务执行次数失败: %w", err)
	}
	return n, nil
}

// WriteExecution 在给定事务内插入一条执行记录；供批量写队列的 BatchWriter 调用。
func WriteExecution(tx *sql.Tx, payload []byte) error {
	var e Execution
	if err := json.Unmarshal(payload, &e); err != nil {
		return fmt.Errorf("解析执行记录失败: %w", err)
	}
	_, err := tx.Exec(`INSERT INTO plugin_executions
		(invocation_id, plugin_id, capability_id, source, subject, started_at, finished_at,
		 duration_ms, status, error_category, task_id, instance_id, input_summary, output_summary)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		e.InvocationID, e.PluginID, e.CapabilityID, e.Source, e.Subject,
		e.StartedAt.UTC().Format(time.RFC3339Nano), e.FinishedAt.UTC().Format(time.RFC3339Nano),
		e.DurationMS, e.Status, e.ErrorCategory, e.TaskID, e.InstanceID, e.InputSummary, e.OutputSummary)
	if err != nil {
		return fmt.Errorf("写入执行记录失败: %w", err)
	}
	return nil
}

// ListExecutions 按过滤条件查询执行记录，默认最近 100 条，上限 500 条。
func (r *Registry) ListExecutions(ctx context.Context, f ExecutionFilter) ([]Execution, error) {
	limit := f.Limit
	if limit <= 0 {
		limit = 100
	}
	if limit > 500 {
		limit = 500
	}

	var where []string
	var args []any
	if f.PluginID != "" {
		where = append(where, "plugin_id = ?")
		args = append(args, f.PluginID)
	}
	if f.CapabilityID != "" {
		where = append(where, "capability_id = ?")
		args = append(args, f.CapabilityID)
	}
	if f.Status != "" {
		where = append(where, "status = ?")
		args = append(args, f.Status)
	}
	if f.Source != "" {
		where = append(where, "source = ?")
		args = append(args, f.Source)
	}
	if f.TaskID != "" {
		where = append(where, "task_id = ?")
		args = append(args, f.TaskID)
	}
	if f.InstanceID != "" {
		where = append(where, "instance_id = ?")
		args = append(args, f.InstanceID)
	}
	if !f.From.IsZero() {
		where = append(where, "started_at >= ?")
		args = append(args, f.From.UTC().Format(time.RFC3339Nano))
	}
	if !f.To.IsZero() {
		where = append(where, "started_at <= ?")
		args = append(args, f.To.UTC().Format(time.RFC3339Nano))
	}
	query := `SELECT invocation_id, plugin_id, capability_id, source, subject, started_at, finished_at,
		duration_ms, status, error_category, task_id, instance_id, input_summary, output_summary FROM plugin_executions`
	if len(where) > 0 {
		query += " WHERE " + strings.Join(where, " AND ")
	}
	query += " ORDER BY started_at DESC LIMIT ?"
	args = append(args, limit)

	var out []Execution
	err := r.st.Read(ctx, func(tx *sql.Tx) error {
		rows, err := tx.Query(query, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var e Execution
			var started, finished string
			if err := rows.Scan(&e.InvocationID, &e.PluginID, &e.CapabilityID, &e.Source, &e.Subject,
				&started, &finished, &e.DurationMS, &e.Status, &e.ErrorCategory, &e.TaskID,
				&e.InstanceID, &e.InputSummary, &e.OutputSummary); err != nil {
				return err
			}
			e.StartedAt, _ = time.Parse(time.RFC3339Nano, started)
			e.FinishedAt, _ = time.Parse(time.RFC3339Nano, finished)
			out = append(out, e)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("查询执行记录失败: %w", err)
	}
	return out, nil
}

// CountExecutions 统计时间窗口内的执行结果，供总览使用。
func (r *Registry) CountExecutions(ctx context.Context, from, to time.Time) (total, failed int, err error) {
	err = r.st.Read(ctx, func(tx *sql.Tx) error {
		if err := tx.QueryRow(`SELECT count(*) FROM plugin_executions WHERE started_at >= ? AND started_at <= ?`,
			from.UTC().Format(time.RFC3339Nano), to.UTC().Format(time.RFC3339Nano)).Scan(&total); err != nil {
			return err
		}
		return tx.QueryRow(`SELECT count(*) FROM plugin_executions
			WHERE started_at >= ? AND started_at <= ? AND status NOT IN ('ok', 'skipped')`,
			from.UTC().Format(time.RFC3339Nano), to.UTC().Format(time.RFC3339Nano)).Scan(&failed)
	})
	return total, failed, err
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
