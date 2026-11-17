package store

import (
	"context"
	"database/sql"
	"testing"
	"time"
)

func TestBatchQueueDrainsOnClose(t *testing.T) {
	dataDir := t.TempDir()
	st := openTestStore(t, dataDir)
	defer st.Close()

	ctx := context.Background()
	if err := st.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec(`CREATE TABLE queue_probe (kind TEXT NOT NULL, payload TEXT NOT NULL)`)
		return err
	}); err != nil {
		t.Fatalf("创建探针表失败: %v", err)
	}

	q := NewBatchQueue(st, testLogger(), 25, 10*time.Millisecond, func(_ context.Context, tx *sql.Tx, recs []Record) error {
		for _, rec := range recs {
			if _, err := tx.Exec(`INSERT INTO queue_probe (kind, payload) VALUES (?, ?)`, rec.Kind, string(rec.Payload)); err != nil {
				return err
			}
		}
		return nil
	})

	const total = 120
	for i := 0; i < total; i++ {
		if err := q.Enqueue(ctx, Record{Kind: RecordKindPluginExecution, Payload: []byte{byte(i)}}); err != nil {
			t.Fatalf("入队第 %d 条失败: %v", i, err)
		}
	}

	if err := q.Close(ctx); err != nil {
		t.Fatalf("关闭队列失败: %v", err)
	}

	var count int
	if err := st.db.QueryRow(`SELECT count(*) FROM queue_probe`).Scan(&count); err != nil {
		t.Fatalf("统计记录失败: %v", err)
	}
	if count != total {
		t.Fatalf("落盘 %d 条，期望 %d 条（关闭必须排空）", count, total)
	}
}

func TestBatchQueueRejectsEnqueueAfterClose(t *testing.T) {
	dataDir := t.TempDir()
	st := openTestStore(t, dataDir)
	defer st.Close()

	ctx := context.Background()
	q := NewBatchQueue(st, testLogger(), 10, 10*time.Millisecond, func(context.Context, *sql.Tx, []Record) error { return nil })
	if err := q.Close(ctx); err != nil {
		t.Fatalf("关闭队列失败: %v", err)
	}
	if err := q.Enqueue(ctx, Record{Kind: RecordKindAudit}); err != ErrQueueClosed {
		t.Fatalf("关闭后入队应返回 ErrQueueClosed，实际: %v", err)
	}
}
