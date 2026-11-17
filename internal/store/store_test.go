package store

import (
	"context"
	"database/sql"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func openTestStore(t *testing.T, dataDir string) *Store {
	t.Helper()
	ctx := context.Background()
	if err := InitDataDir(ctx, testLogger(), dataDir, NoopFilesystemChecker{}); err != nil {
		t.Fatalf("初始化数据目录失败: %v", err)
	}
	st, err := Open(ctx, testLogger(), dataDir)
	if err != nil {
		t.Fatalf("打开存储失败: %v", err)
	}
	return st
}

func TestOpenFreshCreatesSchemaVersion(t *testing.T) {
	dataDir := t.TempDir()
	st := openTestStore(t, dataDir)
	defer st.Close()

	version, err := st.SchemaVersion(context.Background())
	if err != nil {
		t.Fatalf("读取 schema 版本失败: %v", err)
	}
	if version != CurrentSchemaVersion {
		t.Fatalf("schema 版本 = %d，期望 %d", version, CurrentSchemaVersion)
	}

	for _, name := range managedDirs {
		info, err := os.Stat(filepath.Join(dataDir, name))
		if err != nil {
			t.Fatalf("目录 %s 未创建: %v", name, err)
		}
		if !info.IsDir() {
			t.Fatalf("%s 不是目录", name)
		}
	}
}

func TestOpenExistingDatabaseWithoutMigration(t *testing.T) {
	dataDir := t.TempDir()
	st := openTestStore(t, dataDir)
	if err := st.Close(); err != nil {
		t.Fatalf("关闭存储失败: %v", err)
	}

	st2 := openTestStore(t, dataDir)
	defer st2.Close()
	version, err := st2.SchemaVersion(context.Background())
	if err != nil {
		t.Fatalf("读取 schema 版本失败: %v", err)
	}
	if version != CurrentSchemaVersion {
		t.Fatalf("重新打开后 schema 版本 = %d，期望 %d", version, CurrentSchemaVersion)
	}
}

func TestOpenRejectsHigherSchemaVersion(t *testing.T) {
	dataDir := t.TempDir()
	st := openTestStore(t, dataDir)
	if _, err := st.db.Exec("UPDATE schema_version SET version = ? WHERE id = 1", CurrentSchemaVersion+1); err != nil {
		t.Fatalf("构造高版本库失败: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("关闭存储失败: %v", err)
	}

	_, err := Open(context.Background(), testLogger(), dataDir)
	if err == nil || !strings.Contains(err.Error(), "不支持降级启动") {
		t.Fatalf("期望拒绝高版本 schema，实际: %v", err)
	}
}

func TestMigrationCreatesBackup(t *testing.T) {
	dataDir := t.TempDir()
	if err := InitDataDir(context.Background(), testLogger(), dataDir, NoopFilesystemChecker{}); err != nil {
		t.Fatalf("初始化数据目录失败: %v", err)
	}

	// 直接构造一个 v1 时代的库：只有 schema_version 表，没有 M1+ 的业务表。
	// 不能简单把当前库的版本号改小，否则重放迁移会与既有表冲突（迁移只允许向前追加）。
	db, err := sql.Open("sqlite", filepath.Join(dataDir, DirDB, DBFileName))
	if err != nil {
		t.Fatalf("打开原始库失败: %v", err)
	}
	const ddl = `CREATE TABLE schema_version (
		id INTEGER NOT NULL PRIMARY KEY CHECK (id = 1),
		version INTEGER NOT NULL,
		updated_at TEXT NOT NULL
	);
	INSERT INTO schema_version (id, version, updated_at) VALUES (1, 1, '2026-01-01T00:00:00Z');`
	if _, err := db.Exec(ddl); err != nil {
		db.Close()
		t.Fatalf("构造 v1 库失败: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("关闭原始库失败: %v", err)
	}

	st2 := openTestStore(t, dataDir)
	defer st2.Close()

	backups, err := filepath.Glob(filepath.Join(dataDir, DirBackups, "pre-migration-*.db"))
	if err != nil {
		t.Fatalf("扫描备份目录失败: %v", err)
	}
	if len(backups) == 0 {
		t.Fatal("迁移前未生成备份（DOC-08 ST-M-02）")
	}
}
