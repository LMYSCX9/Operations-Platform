package scheduler

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"operations-platform/internal/plugin"
	"operations-platform/internal/plugin/authz"
	"operations-platform/internal/plugin/registry"
	"operations-platform/internal/store"
)

type fakeInvoker struct {
	mu       sync.Mutex
	calls    []string
	tasks    []string
	recorded []registry.Execution
	result   plugin.InvokeResult
	err      error
}

func (f *fakeInvoker) InvokeWithTask(_ context.Context, _ authz.Subject, pluginID, capabilityID string,
	_ json.RawMessage, source, taskID string) (plugin.InvokeResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, pluginID+"/"+capabilityID+"/"+source)
	f.tasks = append(f.tasks, taskID)
	return f.result, f.err
}

func (f *fakeInvoker) InvokeInstanceWithTask(_ context.Context, _ authz.Subject, instanceID, capabilityID string,
	_ json.RawMessage, source, taskID string) (plugin.InvokeResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, instanceID+"/"+capabilityID+"/"+source)
	f.tasks = append(f.tasks, taskID)
	return f.result, f.err
}

func (f *fakeInvoker) RecordExecution(e registry.Execution) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.recorded = append(f.recorded, e)
}

func (f *fakeInvoker) snapshot() ([]string, []registry.Execution) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...), append([]registry.Execution(nil), f.recorded...)
}

func newTestScheduler(t *testing.T, fake *fakeInvoker) *Service {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	dir := t.TempDir()
	if err := store.InitDataDir(context.Background(), logger, dir, nil); err != nil {
		t.Fatalf("初始化测试数据目录失败: %v", err)
	}
	db, err := store.Open(context.Background(), logger, dir)
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	now := time.Date(2026, 3, 1, 10, 2, 30, 0, time.UTC)
	svc, err := NewService(Options{
		Store:          NewStore(db),
		Plugins:        fake,
		Authorizer:     allowAll{},
		Logger:         logger,
		MinInterval:    time.Minute,
		DefaultTimeout: 5 * time.Minute,
		DefaultRetries: 1,
		Now:            func() time.Time { return now },
	})
	if err != nil {
		t.Fatalf("构造调度服务失败: %v", err)
	}
	return svc
}

type allowAll struct{}

func (allowAll) Authorize(context.Context, authz.Subject, authz.Action, authz.Target) authz.Decision {
	return authz.Decision{Allowed: true}
}

var testSubject = authz.Subject{ID: "user:admin", Roles: []string{"admin"}}

func cronInput() TaskInput {
	return TaskInput{
		Name:         "每五分钟",
		PluginID:     "demo-echo",
		CapabilityID: "demo-echo.echo",
		TriggerType:  TriggerCron,
		CronExpr:     "*/5 * * * *",
		Timezone:     "UTC",
	}
}

func TestCreateDefaultsAndIntervalFloor(t *testing.T) {
	svc := newTestScheduler(t, &fakeInvoker{})
	ctx := context.Background()

	task, err := svc.Create(ctx, testSubject, cronInput())
	if err != nil {
		t.Fatalf("创建任务失败: %v", err)
	}
	if task.State != StatePaused {
		t.Fatalf("新建任务默认状态 = %q，期望 %q", task.State, StatePaused)
	}
	if task.TimeoutSeconds != 300 {
		t.Fatalf("默认超时 = %d，期望 300", task.TimeoutSeconds)
	}
	if task.Retries != 1 {
		t.Fatalf("默认重试 = %d，期望 1", task.Retries)
	}
	if task.NextRunAt != nil {
		t.Fatal("暂停任务不应排期")
	}

	bad := cronInput()
	bad.Name = "间隔太小"
	bad.TriggerType = TriggerInterval
	bad.CronExpr = ""
	bad.IntervalSeconds = 30
	if _, err := svc.Create(ctx, testSubject, bad); err == nil {
		t.Fatal("低于最小间隔应被拒绝")
	}
}

func TestEnableComputesNextRunAndRunNow(t *testing.T) {
	fake := &fakeInvoker{result: plugin.InvokeResult{Status: registry.StatusOK}}
	svc := newTestScheduler(t, fake)
	ctx := context.Background()

	task, err := svc.Create(ctx, testSubject, cronInput())
	if err != nil {
		t.Fatalf("创建任务失败: %v", err)
	}
	enabled, err := svc.Enable(ctx, testSubject, task.ID)
	if err != nil {
		t.Fatalf("启用任务失败: %v", err)
	}
	if enabled.State != StateEnabled || enabled.NextRunAt == nil {
		t.Fatalf("启用后状态/排期不正确: %+v", enabled)
	}
	if got := enabled.NextRunAt.UTC().Format("15:04"); got != "10:05" {
		t.Fatalf("下次触发 = %s，期望 10:05", got)
	}

	res, err := svc.RunNow(ctx, testSubject, task.ID)
	if err != nil {
		t.Fatalf("手动执行失败: %v", err)
	}
	if res.Status != registry.StatusOK {
		t.Fatalf("手动执行状态 = %q", res.Status)
	}
	calls, _ := fake.snapshot()
	if len(calls) != 1 || calls[0] != "local:demo-echo/demo-echo.echo/manual" {
		t.Fatalf("调用记录 = %v", calls)
	}
}

func TestClaimIsSingleWriter(t *testing.T) {
	svc := newTestScheduler(t, &fakeInvoker{})
	ctx := context.Background()
	now := svc.now()

	next := now.Add(-time.Minute)
	task := Task{
		ID:            "task-claim",
		Name:          "认领",
		PluginID:      "demo-echo",
		CapabilityID:  "demo-echo.echo",
		Input:         []byte("{}"),
		TriggerType:   TriggerCron,
		CronExpr:      "*/5 * * * *",
		Timezone:      "UTC",
		OverlapPolicy: OverlapSkip,
		State:         StateEnabled,
		NextRunAt:     &next,
	}
	if err := svc.store.Create(ctx, task); err != nil {
		t.Fatalf("创建任务失败: %v", err)
	}

	first, err := svc.store.Claim(ctx, task.ID, "a", now, next)
	if err != nil || !first {
		t.Fatalf("第一次认领 = %v, %v；期望成功", first, err)
	}
	second, err := svc.store.Claim(ctx, task.ID, "b", now, next)
	if err != nil {
		t.Fatalf("第二次认领报错: %v", err)
	}
	if second {
		t.Fatal("同一任务只能被一个执行者认领")
	}
	claimed, err := svc.store.ListClaimed(ctx)
	if err != nil || len(claimed) != 1 {
		t.Fatalf("认领列表 = %d, %v；期望 1 条", len(claimed), err)
	}
}

func TestMarkMissedRecordsWithoutRunning(t *testing.T) {
	fake := &fakeInvoker{}
	svc := newTestScheduler(t, fake)
	ctx := context.Background()
	now := svc.now()

	next := now.Add(-10 * time.Minute)
	task := Task{
		ID:            "task-missed",
		Name:          "错过",
		PluginID:      "demo-echo",
		CapabilityID:  "demo-echo.echo",
		Input:         []byte("{}"),
		TriggerType:   TriggerCron,
		CronExpr:      "*/5 * * * *",
		Timezone:      "UTC",
		OverlapPolicy: OverlapSkip,
		State:         StateEnabled,
		NextRunAt:     &next,
	}
	if err := svc.store.Create(ctx, task); err != nil {
		t.Fatalf("创建任务失败: %v", err)
	}

	if err := svc.markMissed(ctx); err != nil {
		t.Fatalf("记录错过执行失败: %v", err)
	}
	calls, recorded := fake.snapshot()
	if len(calls) != 0 {
		t.Fatalf("错过执行不应调用插件，实际调用 %v", calls)
	}
	if len(recorded) != 1 || recorded[0].Status != registry.StatusMissed || recorded[0].TaskID != task.ID {
		t.Fatalf("错过记录 = %+v", recorded)
	}

	got, err := svc.store.Get(ctx, task.ID)
	if err != nil {
		t.Fatalf("读取任务失败: %v", err)
	}
	if got.NextRunAt == nil || !got.NextRunAt.After(now) {
		t.Fatalf("错过执行后应推进下次触发时间，实际 %v", got.NextRunAt)
	}
}

func TestScanTriggersDueTaskAndSkipsOverlap(t *testing.T) {
	fake := &fakeInvoker{result: plugin.InvokeResult{Status: registry.StatusOK}}
	svc := newTestScheduler(t, fake)
	ctx := context.Background()
	now := svc.now()

	next := now.Add(-time.Minute)
	task := Task{
		ID:            "task-due",
		Name:          "到期",
		PluginID:      "demo-echo",
		CapabilityID:  "demo-echo.echo",
		Input:         []byte("{}"),
		TriggerType:   TriggerCron,
		CronExpr:      "*/5 * * * *",
		Timezone:      "UTC",
		OverlapPolicy: OverlapSkip,
		State:         StateEnabled,
		NextRunAt:     &next,
	}
	if err := svc.store.Create(ctx, task); err != nil {
		t.Fatalf("创建任务失败: %v", err)
	}

	svc.scan(ctx, now)
	deadline := time.Now().Add(3 * time.Second)
	for {
		calls, _ := fake.snapshot()
		if len(calls) == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("到期任务未被触发，调用 = %v", calls)
		}
		time.Sleep(20 * time.Millisecond)
	}
	_ = svc.Stop(ctx)
	// scan 内部按 goroutine 执行，WaitGroup 在 Stop 后应已归零。
	if claimed, err := svc.store.ListClaimed(ctx); err != nil || len(claimed) != 0 {
		t.Fatalf("执行结束应释放认领，实际 %d 条, %v", len(claimed), err)
	}

	// 同一任务仍在执行时再次到期 → 记 skipped，不重复调用。
	again := now.Add(-time.Minute)
	if err := svc.store.SetNextRun(ctx, task.ID, again, again); err != nil {
		t.Fatalf("重置排期失败: %v", err)
	}
	if !svc.markRunning(task.ID) {
		t.Fatal("标记运行态失败")
	}
	svc.scan(ctx, now)
	svc.unmarkRunning(task.ID)

	calls, recorded := fake.snapshot()
	if len(calls) != 1 {
		t.Fatalf("重叠触发不应再次调用插件，实际 %v", calls)
	}
	found := false
	for _, r := range recorded {
		if r.Status == registry.StatusSkipped {
			found = true
		}
	}
	if !found {
		t.Fatalf("重叠跳过应留痕，记录 = %+v", recorded)
	}
}

func TestRecoverClaimsWritesInterrupted(t *testing.T) {
	fake := &fakeInvoker{}
	svc := newTestScheduler(t, fake)
	ctx := context.Background()
	now := svc.now()

	next := now.Add(-time.Minute)
	task := Task{
		ID:            "task-crash",
		Name:          "崩溃",
		PluginID:      "demo-echo",
		CapabilityID:  "demo-echo.echo",
		Input:         []byte("{}"),
		TriggerType:   TriggerCron,
		CronExpr:      "*/5 * * * *",
		Timezone:      "UTC",
		OverlapPolicy: OverlapSkip,
		State:         StateEnabled,
		NextRunAt:     &next,
	}
	if err := svc.store.Create(ctx, task); err != nil {
		t.Fatalf("创建任务失败: %v", err)
	}
	if ok, err := svc.store.Claim(ctx, task.ID, "crash", now, next); err != nil || !ok {
		t.Fatalf("构造崩溃认领失败: %v", err)
	}

	if err := svc.recoverClaims(ctx); err != nil {
		t.Fatalf("恢复认领失败: %v", err)
	}
	_, recorded := fake.snapshot()
	if len(recorded) != 1 || recorded[0].Status != registry.StatusInterrupted {
		t.Fatalf("崩溃恢复记录 = %+v", recorded)
	}
	if claimed, err := svc.store.ListClaimed(ctx); err != nil || len(claimed) != 0 {
		t.Fatalf("恢复后应清空认领，实际 %d 条, %v", len(claimed), err)
	}
}
