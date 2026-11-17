package observability

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"operations-platform/internal/store"
)

// AuditEvent 是一条审计记录。Details 必须是可 JSON 序列化的附加信息，
// 禁止放入口令、令牌、凭据或主密钥（DOC-06 §7）。
type AuditEvent struct {
	OccurredAt time.Time      `json:"occurred_at"`
	Subject    string         `json:"subject"`
	Action     string         `json:"action"`
	TargetType string         `json:"target_type"`
	TargetID   string         `json:"target_id"`
	Result     string         `json:"result"`
	Reason     string         `json:"reason,omitempty"`
	Details    map[string]any `json:"details,omitempty"`
}

// EnqueuePayload 序列化为批量队列载荷。
func (e AuditEvent) EnqueuePayload() ([]byte, error) {
	return json.Marshal(e)
}

// WriteAuditEvent 在批量写事务中落盘一条审计事件。
func WriteAuditEvent(tx *sql.Tx, payload []byte) error {
	var e AuditEvent
	if err := json.Unmarshal(payload, &e); err != nil {
		return fmt.Errorf("解析审计事件失败: %w", err)
	}
	details := "{}"
	if len(e.Details) > 0 {
		raw, err := json.Marshal(e.Details)
		if err != nil {
			return fmt.Errorf("序列化审计详情失败: %w", err)
		}
		details = string(raw)
	}
	_, err := tx.Exec(
		`INSERT INTO audit_events (occurred_at, subject, action, target_type, target_id, result, reason, details)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		e.OccurredAt.UTC().Format(time.RFC3339), e.Subject, e.Action,
		e.TargetType, e.TargetID, e.Result, e.Reason, details)
	return err
}

// DecodeDetails 把库内 details 还原为 map；非法内容按空对象处理，避免读路径整体失败。
func DecodeDetails(raw string) map[string]any {
	if raw == "" {
		return nil
	}
	out := map[string]any{}
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil
	}
	return out
}

// AuditRecorder 通过批量写队列落盘审计事件。
//
// 审计不允许因为队列问题静默丢失：入队失败会写错误日志并计数。
type AuditRecorder struct {
	queue  *store.BatchQueue
	logger *slog.Logger
}

func NewAuditRecorder(queue *store.BatchQueue, logger *slog.Logger) *AuditRecorder {
	return &AuditRecorder{queue: queue, logger: logger}
}

// Record 记录一条审计事件；调用方不阻塞在落盘上。
func (r *AuditRecorder) Record(ctx context.Context, e AuditEvent) {
	if e.OccurredAt.IsZero() {
		e.OccurredAt = time.Now().UTC()
	}
	if r.queue == nil {
		r.logger.Warn("审计未落盘：队列未装配", "action", e.Action, "result", e.Result)
		return
	}
	payload, err := e.EnqueuePayload()
	if err != nil {
		r.logger.Error("序列化审计事件失败", "action", e.Action, "error", err)
		return
	}
	// 审计独立于请求生命周期：请求取消也要留下记录。
	if err := r.queue.Enqueue(context.Background(), store.Record{
		Kind:    store.RecordKindAudit,
		Payload: payload,
	}); err != nil {
		r.logger.Error("审计事件入队失败", "action", e.Action, "error", err)
	}
}
