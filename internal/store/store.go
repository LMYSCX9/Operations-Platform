package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite" // 纯 Go SQLite 驱动，满足 ADR-0007「免 cgo」约束
)

// DBFileName 在规格中仍是「实现时确认」项（DOC-08 §4）。
// M0 基线取 platform.db，待 OQ-036 确认。
const DBFileName = "platform.db"

// CurrentSchemaVersion 是当前二进制支持的 schema 版本。
// 库版本高于它时必须拒绝启动（DOC-08 ST-M-01）。
// v2=M1 插件注册表与执行记录，v3=M2 身份与审计，v4=M3 主机与下发，v5=M4 定时任务，v6=M5 执行摘要，
// v7=M6 插件类型/实例/配置/探测/证书绑定与注册审计（DOC-17、ADR-0023）。
const CurrentSchemaVersion = 7

type migration struct {
	Version    int
	Name       string
	Statements []string
}

// migrations 按版本升序排列。禁止修改已发布的迁移内容（DOC-08 ST-M-05）。
// 表结构与字段属于实现基线（OQ-039~OQ-042），与 docs/12~15 同批次维护。
var migrations = []migration{
	{Version: 1, Name: "baseline"},
	{Version: 2, Name: "plugin-registry", Statements: []string{
		`CREATE TABLE plugins (
			plugin_id        TEXT NOT NULL PRIMARY KEY,
			plugin_version   TEXT NOT NULL,
			form             TEXT NOT NULL,
			state            TEXT NOT NULL CHECK (state IN ('disabled', 'enabled')),
			install_path     TEXT NOT NULL,
			manifest_yaml    TEXT NOT NULL,
			manifest_sha256  TEXT NOT NULL,
			contract_ok      INTEGER NOT NULL,
			contract_reason  TEXT NOT NULL DEFAULT '',
			discovered_at    TEXT NOT NULL,
			updated_at       TEXT NOT NULL
		)`,
		`CREATE TABLE plugin_executions (
			id              INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
			invocation_id   TEXT NOT NULL,
			plugin_id       TEXT NOT NULL,
			capability_id   TEXT NOT NULL,
			source          TEXT NOT NULL,
			subject         TEXT NOT NULL,
			started_at      TEXT NOT NULL,
			finished_at     TEXT NOT NULL,
			duration_ms     INTEGER NOT NULL,
			status          TEXT NOT NULL,
			error_category  TEXT NOT NULL DEFAULT '',
			task_id         TEXT NOT NULL DEFAULT ''
		)`,
		`CREATE INDEX idx_plugin_executions_plugin ON plugin_executions(plugin_id, started_at DESC)`,
		`CREATE INDEX idx_plugin_executions_started ON plugin_executions(started_at DESC)`,
		`CREATE INDEX idx_plugin_executions_task ON plugin_executions(task_id, started_at DESC)`,
	}},
	{Version: 3, Name: "identity-and-audit", Statements: []string{
		`CREATE TABLE users (
			user_id       TEXT NOT NULL PRIMARY KEY,
			username      TEXT NOT NULL UNIQUE,
			display_name  TEXT NOT NULL,
			role          TEXT NOT NULL CHECK (role IN ('admin', 'operator', 'viewer')),
			password_hash TEXT NOT NULL,
			disabled      INTEGER NOT NULL DEFAULT 0,
			created_at    TEXT NOT NULL,
			updated_at    TEXT NOT NULL
		)`,
		`CREATE TABLE sessions (
			session_id   TEXT NOT NULL PRIMARY KEY,
			user_id      TEXT NOT NULL REFERENCES users(user_id),
			created_at   TEXT NOT NULL,
			expires_at   TEXT NOT NULL,
			last_seen_at TEXT NOT NULL
		)`,
		`CREATE INDEX idx_sessions_expires ON sessions(expires_at)`,
		`CREATE TABLE audit_events (
			id          INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
			occurred_at TEXT NOT NULL,
			subject     TEXT NOT NULL,
			action      TEXT NOT NULL,
			target_type TEXT NOT NULL,
			target_id   TEXT NOT NULL,
			result      TEXT NOT NULL,
			reason      TEXT NOT NULL DEFAULT '',
			details     TEXT NOT NULL DEFAULT '{}'
		)`,
		`CREATE INDEX idx_audit_occurred ON audit_events(occurred_at DESC)`,
	}},
	{Version: 4, Name: "hosts-and-distribution", Statements: []string{
		`CREATE TABLE hosts (
			host_id          TEXT NOT NULL PRIMARY KEY,
			name             TEXT NOT NULL UNIQUE,
			address          TEXT NOT NULL,
			port             INTEGER NOT NULL,
			username         TEXT NOT NULL,
			auth_method      TEXT NOT NULL CHECK (auth_method IN ('key', 'password')),
			host_key_fp      TEXT NOT NULL DEFAULT '',
			host_key_trusted INTEGER NOT NULL DEFAULT 0,
			status           TEXT NOT NULL DEFAULT 'unknown',
			last_test_at     TEXT NOT NULL DEFAULT '',
			last_test_error  TEXT NOT NULL DEFAULT '',
			created_by       TEXT NOT NULL,
			created_at       TEXT NOT NULL,
			updated_at       TEXT NOT NULL
		)`,
		`CREATE TABLE host_credentials (
			host_id    TEXT NOT NULL PRIMARY KEY REFERENCES hosts(host_id) ON DELETE CASCADE,
			ciphertext BLOB NOT NULL,
			updated_at TEXT NOT NULL
		)`,
		`CREATE TABLE distributions (
			distribution_id TEXT NOT NULL PRIMARY KEY,
			host_id         TEXT NOT NULL,
			plugin_id       TEXT NOT NULL,
			plugin_version  TEXT NOT NULL,
			state           TEXT NOT NULL,
			stage           TEXT NOT NULL DEFAULT '',
			error_category  TEXT NOT NULL DEFAULT '',
			error_message   TEXT NOT NULL DEFAULT '',
			remote_path     TEXT NOT NULL DEFAULT '',
			idempotency_key TEXT NOT NULL,
			created_by      TEXT NOT NULL,
			created_at      TEXT NOT NULL,
			started_at      TEXT NOT NULL DEFAULT '',
			finished_at     TEXT NOT NULL DEFAULT '',
			UNIQUE (host_id, plugin_id, plugin_version, idempotency_key)
		)`,
		`CREATE INDEX idx_distributions_host ON distributions(host_id, created_at DESC)`,
	}},
	{Version: 5, Name: "scheduled-tasks", Statements: []string{
		`CREATE TABLE tasks (
			task_id          TEXT NOT NULL PRIMARY KEY,
			name             TEXT NOT NULL UNIQUE,
			description      TEXT NOT NULL DEFAULT '',
			plugin_id        TEXT NOT NULL,
			capability_id    TEXT NOT NULL,
			input_json       TEXT NOT NULL DEFAULT '{}',
			trigger_type     TEXT NOT NULL CHECK (trigger_type IN ('cron', 'interval')),
			cron_expr        TEXT NOT NULL DEFAULT '',
			interval_seconds INTEGER NOT NULL DEFAULT 0,
			timezone         TEXT NOT NULL,
			timeout_seconds  INTEGER NOT NULL,
			retries          INTEGER NOT NULL,
			overlap_policy   TEXT NOT NULL DEFAULT 'skip',
			state            TEXT NOT NULL CHECK (state IN ('enabled', 'paused', 'unavailable')),
			next_run_at      TEXT NOT NULL DEFAULT '',
			last_run_at      TEXT NOT NULL DEFAULT '',
			claimed_at       TEXT NOT NULL DEFAULT '',
			claim_token      TEXT NOT NULL DEFAULT '',
			created_by       TEXT NOT NULL,
			created_at       TEXT NOT NULL,
			updated_at       TEXT NOT NULL
		)`,
		`CREATE INDEX idx_tasks_next_run ON tasks(state, next_run_at)`,
	}},
	{Version: 6, Name: "execution-summaries", Statements: []string{
		`ALTER TABLE plugin_executions ADD COLUMN input_summary TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE plugin_executions ADD COLUMN output_summary TEXT NOT NULL DEFAULT ''`,
	}},
	{Version: 7, Name: "plugin-types-instances-pki", Statements: []string{
		`CREATE TABLE plugin_types (
			type_id            TEXT NOT NULL PRIMARY KEY,
			display_name       TEXT NOT NULL,
			description        TEXT NOT NULL DEFAULT '',
			form               TEXT NOT NULL CHECK (form IN ('command', 'daemon')),
			latest_version     TEXT NOT NULL,
			custody            TEXT NOT NULL CHECK (custody IN ('local', 'remote')),
			manifest_yaml      TEXT NOT NULL,
			manifest_sha256    TEXT NOT NULL,
			contract_version   TEXT NOT NULL,
			contract_range     TEXT NOT NULL,
			runtime_port       INTEGER NOT NULL DEFAULT 0,
			config_schema_json TEXT NOT NULL DEFAULT '[]',
			created_at         TEXT NOT NULL,
			updated_at         TEXT NOT NULL
		)`,
		`CREATE TABLE plugin_type_capabilities (
			type_id              TEXT NOT NULL,
			capability_id        TEXT NOT NULL,
			description          TEXT NOT NULL DEFAULT '',
			input_schema_ref     TEXT NOT NULL DEFAULT '',
			output_schema_ref    TEXT NOT NULL DEFAULT '',
			idempotent           INTEGER NOT NULL DEFAULT 0,
			long_running         INTEGER NOT NULL DEFAULT 0,
			supports_cancel      INTEGER NOT NULL DEFAULT 0,
			concurrency          TEXT NOT NULL DEFAULT 'serial',
			sensitive            INTEGER NOT NULL DEFAULT 0,
			risk_level           TEXT NOT NULL DEFAULT 'normal',
			suggested_timeout_ms INTEGER NOT NULL DEFAULT 0,
			PRIMARY KEY (type_id, capability_id)
		)`,
		`CREATE TABLE plugin_instances (
			instance_id        TEXT NOT NULL PRIMARY KEY,
			type_id            TEXT NOT NULL,
			host_id            TEXT NOT NULL DEFAULT '',
			custody            TEXT NOT NULL CHECK (custody IN ('local', 'remote')),
			package_version    TEXT NOT NULL,
			listen_port        INTEGER NOT NULL DEFAULT 0,
			enabled            INTEGER NOT NULL DEFAULT 0,
			installed          INTEGER NOT NULL DEFAULT 0,
			started            INTEGER NOT NULL DEFAULT 0,
			callable           INTEGER NOT NULL DEFAULT 0,
			state              TEXT NOT NULL DEFAULT 'installed',
			state_reason       TEXT NOT NULL DEFAULT '',
			drift              TEXT NOT NULL DEFAULT '',
			cert_serial        TEXT NOT NULL DEFAULT '',
			cert_fingerprint   TEXT NOT NULL DEFAULT '',
			cert_not_after     TEXT NOT NULL DEFAULT '',
			last_probe_at      TEXT NOT NULL DEFAULT '',
			last_probe_result  TEXT NOT NULL DEFAULT '',
			last_probe_reason  TEXT NOT NULL DEFAULT '',
			last_error         TEXT NOT NULL DEFAULT '',
			created_at         TEXT NOT NULL,
			updated_at         TEXT NOT NULL,
			UNIQUE (host_id, type_id)
		)`,
		`CREATE INDEX idx_plugin_instances_host ON plugin_instances(host_id, type_id)`,
		`CREATE TABLE plugin_instance_configs (
			instance_id TEXT NOT NULL,
			config_key  TEXT NOT NULL,
			ciphertext  BLOB NOT NULL,
			updated_at  TEXT NOT NULL,
			updated_by  TEXT NOT NULL,
			PRIMARY KEY (instance_id, config_key)
		)`,
		`CREATE TABLE instance_probes (
			id          INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
			instance_id TEXT NOT NULL,
			probe_at    TEXT NOT NULL,
			stage       TEXT NOT NULL DEFAULT '',
			ok          INTEGER NOT NULL,
			latency_ms  INTEGER NOT NULL DEFAULT 0,
			detail      TEXT NOT NULL DEFAULT ''
		)`,
		`CREATE INDEX idx_instance_probes ON instance_probes(instance_id, probe_at DESC)`,
		`CREATE TABLE cert_bindings (
			serial         TEXT NOT NULL PRIMARY KEY,
			fingerprint    TEXT NOT NULL,
			purpose        TEXT NOT NULL CHECK (purpose IN ('server', 'client')),
			plugin_id      TEXT NOT NULL DEFAULT '',
			host_id        TEXT NOT NULL DEFAULT '',
			instance_id    TEXT NOT NULL DEFAULT '',
			subject        TEXT NOT NULL DEFAULT '',
			san            TEXT NOT NULL DEFAULT '',
			not_before     TEXT NOT NULL,
			not_after      TEXT NOT NULL,
			cert_pem       TEXT NOT NULL,
			key_ciphertext BLOB NOT NULL,
			revoked_at     TEXT NOT NULL DEFAULT '',
			revoked_reason TEXT NOT NULL DEFAULT '',
			issued_at      TEXT NOT NULL
		)`,
		`CREATE INDEX idx_cert_bindings_instance ON cert_bindings(instance_id)`,
		`CREATE TABLE registration_events (
			id          INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
			occurred_at TEXT NOT NULL,
			action      TEXT NOT NULL,
			type_id     TEXT NOT NULL DEFAULT '',
			instance_id TEXT NOT NULL DEFAULT '',
			host_id     TEXT NOT NULL DEFAULT '',
			actor       TEXT NOT NULL DEFAULT '',
			result      TEXT NOT NULL,
			reason      TEXT NOT NULL DEFAULT '',
			details     TEXT NOT NULL DEFAULT '{}'
		)`,
		`CREATE INDEX idx_registration_events ON registration_events(occurred_at DESC)`,
		`ALTER TABLE distributions ADD COLUMN instance_id TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE distributions ADD COLUMN cert_serial TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE distributions ADD COLUMN started_ok INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE tasks ADD COLUMN instance_id TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE plugin_executions ADD COLUMN instance_id TEXT NOT NULL DEFAULT ''`,
	}},
}

// Store 封装底座唯一的数据库连接。
//
// M0 用单一连接串行化读写，落实 DOC-08 §6 的单写者模型；
// 批量写队列与读写分离在后续里程碑实现。
type Store struct {
	db      *sql.DB
	dataDir string
	path    string
	logger  *slog.Logger
}

// Open 打开数据库并完成 schema 版本检查与迁移。
func Open(ctx context.Context, logger *slog.Logger, dataDir string) (*Store, error) {
	if logger == nil {
		return nil, errors.New("store: logger 不能为空")
	}
	restrictProcessUmask()

	dbDir := filepath.Join(dataDir, DirDB)
	dbPath := filepath.Join(dbDir, DBFileName)

	// 在 SQLite 打开前判断是否已有非空库：空文件视为全新库，
	// 避免把预创建的空文件误判为「无法读取版本的旧库」。
	existed := false
	if info, err := os.Stat(dbPath); err == nil {
		existed = info.Size() > 0
	} else if !errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("读取数据库文件信息失败: %w", err)
	}

	// 预先创建库文件并收紧权限；SQLite 新建文件时只能继承进程 umask。
	f, err := os.OpenFile(dbPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("创建数据库文件 %q 失败: %w", dbPath, err)
	}
	if err := f.Close(); err != nil {
		return nil, fmt.Errorf("关闭数据库文件句柄失败: %w", err)
	}
	restrictFilePermissions(logger, dbPath)

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("打开数据库失败: %w", err)
	}
	db.SetMaxOpenConns(1)

	s := &Store{db: db, dataDir: dataDir, path: dbPath, logger: logger}

	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("数据库不可用: %w", err)
	}

	if err := s.enableWAL(ctx); err != nil {
		db.Close()
		return nil, err
	}
	if _, err := db.ExecContext(ctx, "PRAGMA foreign_keys=ON"); err != nil {
		db.Close()
		return nil, fmt.Errorf("启用外键约束失败: %w", err)
	}

	hasTable, version, err := s.schemaState(ctx)
	if err != nil {
		db.Close()
		return nil, err
	}
	if !hasTable {
		if existed {
			db.Close()
			return nil, errors.New("数据库文件非空但缺少 schema_version 表，无法读取版本；请恢复备份后重试（DOC-08 §8）")
		}
		if err := s.createSchemaVersionTable(ctx); err != nil {
			db.Close()
			return nil, err
		}
	}
	if err := s.migrate(ctx, existed, version); err != nil {
		db.Close()
		return nil, err
	}

	finalVersion, err := s.SchemaVersion(ctx)
	if err != nil {
		db.Close()
		return nil, err
	}
	logger.Info("数据库已就绪", "path", dbPath, "schema_version", finalVersion)
	return s, nil
}

func (s *Store) enableWAL(ctx context.Context) error {
	var mode string
	if err := s.db.QueryRowContext(ctx, "PRAGMA journal_mode=WAL").Scan(&mode); err != nil {
		return fmt.Errorf("设置 WAL 日志模式失败: %w", err)
	}
	if !strings.EqualFold(mode, "wal") {
		return fmt.Errorf("数据库未切换到 WAL 模式（当前 %q）；数据目录可能位于不支持可靠文件锁的文件系统，禁止继续启动（DOC-08 §5）", mode)
	}
	return nil
}

func (s *Store) createSchemaVersionTable(ctx context.Context) error {
	const ddl = `CREATE TABLE schema_version (
		id INTEGER NOT NULL PRIMARY KEY CHECK (id = 1),
		version INTEGER NOT NULL,
		updated_at TEXT NOT NULL
	)`
	if _, err := s.db.ExecContext(ctx, ddl); err != nil {
		return fmt.Errorf("创建 schema_version 表失败: %w", err)
	}
	return nil
}

// schemaState 返回版本表是否存在及其记录的版本；表存在但无行时版本为 0。
func (s *Store) schemaState(ctx context.Context) (bool, int, error) {
	var count int
	const existsSQL = `SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = 'schema_version'`
	if err := s.db.QueryRowContext(ctx, existsSQL).Scan(&count); err != nil {
		return false, 0, fmt.Errorf("检查 schema_version 表失败: %w", err)
	}
	if count == 0 {
		return false, 0, nil
	}
	version, err := s.SchemaVersion(ctx)
	if err != nil {
		return false, 0, err
	}
	return true, version, nil
}

// SchemaVersion 返回库内记录的 schema 版本；没有记录时返回 0。
func (s *Store) SchemaVersion(ctx context.Context) (int, error) {
	var version int
	err := s.db.QueryRowContext(ctx, "SELECT version FROM schema_version WHERE id = 1").Scan(&version)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("读取 schema 版本失败: %w", err)
	}
	return version, nil
}

func (s *Store) migrate(ctx context.Context, existed bool, current int) error {
	if current > CurrentSchemaVersion {
		return fmt.Errorf(
			"数据库 schema 版本为 v%d，高于当前二进制支持的 v%d；不支持降级启动，请使用对应版本的二进制或恢复备份（DOC-08 ST-M-01）",
			current, CurrentSchemaVersion)
	}
	if current == CurrentSchemaVersion {
		return nil
	}

	// ST-M-02：迁移前必须备份，备份失败则中止。首次创建的空库没有可备份内容。
	if existed {
		if err := s.backupBeforeMigration(ctx, current, CurrentSchemaVersion); err != nil {
			return fmt.Errorf("迁移前备份失败，已中止迁移: %w", err)
		}
	}

	for _, m := range migrations {
		if m.Version <= current {
			continue
		}
		if err := s.applyMigration(ctx, m); err != nil {
			return err
		}
		s.logger.Info("已应用数据库迁移", "version", m.Version, "name", m.Name)
	}
	return nil
}

func (s *Store) applyMigration(ctx context.Context, m migration) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("开启迁移 v%d 事务失败: %w", m.Version, err)
	}
	// 已提交后 Rollback 返回 sql.ErrTxDone，属于正常路径，无需处理。
	defer func() { _ = tx.Rollback() }()

	for _, stmt := range m.Statements {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("迁移 v%d（%s）执行失败: %w", m.Version, m.Name, err)
		}
	}

	const upsert = `INSERT INTO schema_version (id, version, updated_at) VALUES (1, ?, ?)
		ON CONFLICT(id) DO UPDATE SET version = excluded.version, updated_at = excluded.updated_at`
	if _, err := tx.ExecContext(ctx, upsert, m.Version, time.Now().UTC().Format(time.RFC3339)); err != nil {
		return fmt.Errorf("记录 schema 版本 v%d 失败: %w", m.Version, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("提交迁移 v%d 事务失败: %w", m.Version, err)
	}
	return nil
}

// backupBeforeMigration 使用 VACUUM INTO 生成一致性快照。
//
// 这是 DOC-08 §7.1 允许的「一致性快照」方式之一；后续可替换为驱动提供的在线备份接口。
func (s *Store) backupBeforeMigration(ctx context.Context, from, to int) error {
	name := fmt.Sprintf("pre-migration-v%d-to-v%d-%s.db", from, to, time.Now().UTC().Format("20060102T150405Z"))
	dst := filepath.Join(s.dataDir, DirBackups, name)

	// VACUUM INTO 不接受绑定参数，只能拼接到 SQL；路径来自配置，仍需转义单引号。
	quoted := strings.ReplaceAll(dst, "'", "''")
	if _, err := s.db.ExecContext(ctx, "VACUUM INTO '"+quoted+"'"); err != nil {
		return err
	}
	restrictFilePermissions(s.logger, dst)
	s.logger.Info("迁移前备份完成", "path", dst, "schema_version", from)
	return nil
}

// Write 在单写者模型下执行一次写事务。
//
// 所有写路径必须经此入口，禁止调用方自行开事务或直接写库（DOC-08 §6）；
// 连接池已限制为 1，因此并发写入会被串行化。
func (s *Store) Write(ctx context.Context, fn func(tx *sql.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("开启写事务失败: %w", err)
	}
	// 提交成功后 Rollback 返回 sql.ErrTxDone，属于正常路径。
	defer func() { _ = tx.Rollback() }()

	if err := fn(tx); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("提交写事务失败: %w", err)
	}
	return nil
}

// Read 在只读事务中执行查询，供需要多语句一致快照的读路径使用。
func (s *Store) Read(ctx context.Context, fn func(tx *sql.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return fmt.Errorf("开启读事务失败: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	return fn(tx)
}

// Ping 供启动自检与健康检查使用。
func (s *Store) Ping(ctx context.Context) error {
	if err := s.db.PingContext(ctx); err != nil {
		return fmt.Errorf("数据库连接不可用: %w", err)
	}
	return nil
}

// Path 返回库文件绝对路径，便于日志与排障。
func (s *Store) Path() string {
	return s.path
}

// Close 关闭数据库连接。
func (s *Store) Close() error {
	if s.db == nil {
		return nil
	}
	if err := s.db.Close(); err != nil {
		return fmt.Errorf("关闭数据库失败: %w", err)
	}
	return nil
}
