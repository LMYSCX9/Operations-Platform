package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"
)

// 记录类型：队列只负责搬运，具体 SQL 由写入方通过 BatchWriter 注入，
// 避免 store 包反向依赖业务包（DOC-08 ST-W-01）。
const (
	RecordKindPluginExecution = "plugin_execution"
	RecordKindAudit           = "audit_event"
)

// ErrQueueClosed 表示队列已关闭，调用方必须把记录视为未落盘。
var ErrQueueClosed = errors.New("批量写队列已关闭")

// Record 是写入队列的最小单元。
type Record struct {
	Kind    string
	Payload []byte
}

// BatchWriter 在单个事务内落盘一批记录。实现方负责按 Kind 分发。
type BatchWriter func(ctx context.Context, tx *sql.Tx, recs []Record) error

// BatchQueue 实现 DOC-08 §6 的批量写模型：调用方只入队，由单一后台协程
// 按批合并写入，避免每条记录各抢一次写锁。
type BatchQueue struct {
	st       *Store
	logger   *slog.Logger
	writer   BatchWriter
	batch    int
	interval time.Duration

	ch   chan Record
	done chan struct{}

	closed    atomic.Bool
	closeOnce sync.Once
	wg        sync.WaitGroup

	flushMu sync.Mutex
	errMu   sync.Mutex
	errs    []error
}

// NewBatchQueue 创建并启动后台写入协程。
func NewBatchQueue(st *Store, logger *slog.Logger, batch int, interval time.Duration, writer BatchWriter) *BatchQueue {
	if batch <= 0 {
		batch = 50
	}
	if interval <= 0 {
		interval = 200 * time.Millisecond
	}
	q := &BatchQueue{
		st:       st,
		logger:   logger,
		writer:   writer,
		batch:    batch,
		interval: interval,
		ch:       make(chan Record, 1024),
		done:     make(chan struct{}),
	}
	q.wg.Add(1)
	go q.loop()
	return q
}

// Enqueue 入队一条记录。队列满时阻塞等待而不是丢弃（ST-W-02）。
func (q *BatchQueue) Enqueue(ctx context.Context, rec Record) error {
	if q.closed.Load() {
		return ErrQueueClosed
	}
	select {
	case q.ch <- rec:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("写入队列等待超时: %w", ctx.Err())
	case <-q.done:
		return ErrQueueClosed
	}
}

// Flush 立即把当前已入队记录写入数据库。
func (q *BatchQueue) Flush(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	for {
		n, err := q.flushOnce(ctx)
		if err != nil {
			return err
		}
		if n == 0 {
			return nil
		}
	}
}

// Close 停止接收新记录，排空队列后返回聚合错误。
func (q *BatchQueue) Close(ctx context.Context) error {
	q.closed.Store(true)
	q.closeOnce.Do(func() { close(q.done) })
	q.wg.Wait()
	if err := q.Flush(ctx); err != nil {
		return err
	}
	q.errMu.Lock()
	defer q.errMu.Unlock()
	if len(q.errs) > 0 {
		return errors.Join(q.errs...)
	}
	return nil
}

func (q *BatchQueue) loop() {
	defer q.wg.Done()
	ticker := time.NewTicker(q.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			if _, err := q.flushOnce(context.Background()); err != nil {
				q.recordErr(err)
			}
		case <-q.done:
			// 关闭前必须排空，否则已确认的调用会丢失记录。
			for {
				n, err := q.flushOnce(context.Background())
				if err != nil {
					q.recordErr(err)
				}
				if n == 0 {
					return
				}
			}
		}
	}
}

func (q *BatchQueue) flushOnce(ctx context.Context) (int, error) {
	q.flushMu.Lock()
	defer q.flushMu.Unlock()

	recs := make([]Record, 0, q.batch)
fill:
	for len(recs) < q.batch {
		select {
		case rec := <-q.ch:
			recs = append(recs, rec)
		default:
			break fill
		}
	}
	if len(recs) == 0 {
		return 0, nil
	}

	write := func() error {
		return q.st.Write(ctx, func(tx *sql.Tx) error {
			return q.writer(ctx, tx, recs)
		})
	}
	err := write()
	if err != nil {
		// 一次重试：SQLite 在检查点等场景可能短暂 BUSY，不应立即丢记录。
		err = write()
	}
	if err != nil {
		return len(recs), fmt.Errorf("批量写入 %d 条记录失败: %w", len(recs), err)
	}
	return len(recs), nil
}

func (q *BatchQueue) recordErr(err error) {
	q.logger.Error("批量写队列落盘失败", "error", err)
	q.errMu.Lock()
	q.errs = append(q.errs, err)
	q.errMu.Unlock()
}
