package host

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"operations-platform/internal/store"
)

func newTestStore(t *testing.T) *Store {
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
	return NewStore(db)
}

func newHostFixture() Host {
	return Host{
		ID:         "host-1",
		Name:       "测试主机",
		Address:    "127.0.0.1",
		Port:       22,
		Username:   "root",
		AuthMethod: AuthMethodPassword,
		CreatedBy:  "user-admin",
	}
}

func TestStoreCreateAndGet(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	if err := s.Create(ctx, newHostFixture(), []byte("cipher")); err != nil {
		t.Fatalf("创建主机失败: %v", err)
	}
	got, err := s.Get(ctx, "host-1")
	if err != nil {
		t.Fatalf("读取主机失败: %v", err)
	}
	if got.Name != "测试主机" || got.Port != 22 || got.AuthMethod != AuthMethodPassword {
		t.Fatalf("主机字段未正确落库: %+v", got)
	}
	if !got.CredentialConfigured {
		t.Fatal("凭据存在时 CredentialConfigured 应为 true")
	}
	if got.Status != StatusUnknown {
		t.Fatalf("初始状态 = %q，期望 %q", got.Status, StatusUnknown)
	}
	if _, err := s.Get(ctx, "missing"); err != ErrNotFound {
		t.Fatalf("不存在的主机错误 = %v，期望 ErrNotFound", err)
	}
}

func TestStoreUpdateKeepsCredentialWhenNil(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	h := newHostFixture()
	if err := s.Create(ctx, h, []byte("cipher-1")); err != nil {
		t.Fatalf("创建主机失败: %v", err)
	}
	h.Name = "改名后"
	if err := s.Update(ctx, h, nil); err != nil {
		t.Fatalf("更新主机失败: %v", err)
	}
	got, _ := s.CredentialCiphertext(ctx, h.ID)
	if string(got) != "cipher-1" {
		t.Fatalf("nil 密文不应替换原凭据，实际 %q", got)
	}
	if err := s.Update(ctx, h, []byte("cipher-2")); err != nil {
		t.Fatalf("替换凭据失败: %v", err)
	}
	got, _ = s.CredentialCiphertext(ctx, h.ID)
	if string(got) != "cipher-2" {
		t.Fatalf("凭据未替换，实际 %q", got)
	}
}

func TestStoreTrustFingerprintAndTestResult(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	h := newHostFixture()
	if err := s.Create(ctx, h, []byte("cipher")); err != nil {
		t.Fatalf("创建主机失败: %v", err)
	}
	if err := s.TrustFingerprint(ctx, h.ID, "SHA256:abc"); err != nil {
		t.Fatalf("锁存指纹失败: %v", err)
	}
	at := time.Now().UTC().Truncate(time.Second)
	if err := s.SetTestResult(ctx, h.ID, StatusReachable, "", at); err != nil {
		t.Fatalf("记录测试结果失败: %v", err)
	}
	got, _ := s.Get(ctx, h.ID)
	if !got.HostKeyTrusted || got.HostKeyFP != "SHA256:abc" {
		t.Fatalf("指纹未正确落库: %+v", got)
	}
	if got.Status != StatusReachable || got.LastTestAt == nil {
		t.Fatalf("测试结果未正确落库: %+v", got)
	}
}

func TestStoreDistributionIdempotent(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	h := newHostFixture()
	if err := s.Create(ctx, h, []byte("cipher")); err != nil {
		t.Fatalf("创建主机失败: %v", err)
	}
	base := Distribution{
		ID:             "dist-1",
		HostID:         h.ID,
		PluginID:       "demo",
		PluginVersion:  "1.0.0",
		IdempotencyKey: "demo@1.0.0",
	}
	first, err := s.CreateDistribution(ctx, base)
	if err != nil {
		t.Fatalf("创建下发任务失败: %v", err)
	}
	if first.ID != "dist-1" {
		t.Fatalf("首次创建应返回新记录，实际 %s", first.ID)
	}
	second, err := s.CreateDistribution(ctx, Distribution{
		ID:             "dist-2",
		HostID:         h.ID,
		PluginID:       "demo",
		PluginVersion:  "1.0.0",
		IdempotencyKey: "demo@1.0.0",
	})
	if err != nil {
		t.Fatalf("重复创建失败: %v", err)
	}
	if second.ID != "dist-1" {
		t.Fatalf("重复提交应返回既有记录 dist-1，实际 %s", second.ID)
	}
	if second.Callable {
		t.Fatal("下发记录不得标记为可调用")
	}
	list, err := s.ListDistributions(ctx, h.ID)
	if err != nil || len(list) != 1 {
		t.Fatalf("下发记录数 = %d, err = %v，期望 1 条", len(list), err)
	}
}

func TestStoreMarkInterrupted(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	h := newHostFixture()
	if err := s.Create(ctx, h, []byte("cipher")); err != nil {
		t.Fatalf("创建主机失败: %v", err)
	}
	d, err := s.CreateDistribution(ctx, Distribution{
		ID: "dist-1", HostID: h.ID, PluginID: "demo", PluginVersion: "1.0.0", IdempotencyKey: "k",
	})
	if err != nil {
		t.Fatalf("创建下发任务失败: %v", err)
	}
	d.State = DistUploading
	if err := s.UpdateDistribution(ctx, d); err != nil {
		t.Fatalf("更新下发状态失败: %v", err)
	}
	n, err := s.MarkInterrupted(ctx)
	if err != nil || n != 1 {
		t.Fatalf("标记中断 = %d, err = %v，期望 1", n, err)
	}
	got, _ := s.GetDistribution(ctx, "dist-1")
	if got.State != DistInterrupted {
		t.Fatalf("状态 = %q，期望 %q", got.State, DistInterrupted)
	}
}

func TestStoreDeleteCascadesCredential(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	h := newHostFixture()
	if err := s.Create(ctx, h, []byte("cipher")); err != nil {
		t.Fatalf("创建主机失败: %v", err)
	}
	if err := s.Delete(ctx, h.ID); err != nil {
		t.Fatalf("删除主机失败: %v", err)
	}
	if _, err := s.CredentialCiphertext(ctx, h.ID); err != ErrNotFound {
		t.Fatalf("凭据应随主机删除，实际错误 %v", err)
	}
}
