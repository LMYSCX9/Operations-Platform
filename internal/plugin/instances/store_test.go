package instances

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"operations-platform/internal/pki"
	"operations-platform/internal/store"
)

func newTestStore(t *testing.T) (*Store, *store.Store) {
	t.Helper()
	ctx := context.Background()
	dataDir := t.TempDir()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	if err := store.InitDataDir(ctx, logger, dataDir, store.NoopFilesystemChecker{}); err != nil {
		t.Fatalf("初始化数据目录失败: %v", err)
	}
	st, err := store.Open(ctx, logger, dataDir)
	if err != nil {
		t.Fatalf("打开存储失败: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	return NewStore(st), st
}

func sampleType() (Type, []Capability) {
	t := Type{
		TypeID: "demo", DisplayName: "示例", Form: "daemon", LatestVersion: "1.0.0",
		Custody: CustodyRemote, ManifestYAML: "identity: {id: demo}",
		ManifestSHA256: "sha-1", ContractVersion: "1.2", ContractRange: ">=1.2,<2.0",
		RuntimePort: 9443, ConfigSchema: "[]",
	}
	caps := []Capability{{TypeID: "demo", CapabilityID: "echo", Concurrency: "serial", RiskLevel: "normal"}}
	return t, caps
}

func TestUpsertTypeIsIdempotent(t *testing.T) {
	is, _ := newTestStore(t)
	ctx := context.Background()
	ty, caps := sampleType()

	changed, err := is.UpsertType(ctx, ty, caps)
	if err != nil || !changed {
		t.Fatalf("首次登记应变更，changed=%v err=%v", changed, err)
	}
	changed, err = is.UpsertType(ctx, ty, caps)
	if err != nil || changed {
		t.Fatalf("清单未变不应变更，changed=%v err=%v", changed, err)
	}
	got, err := is.ListCapabilities(ctx, "demo")
	if err != nil || len(got) != 1 || got[0].CapabilityID != "echo" {
		t.Fatalf("能力清单不符: %+v err=%v", got, err)
	}
}

func TestLocalInstanceStartsDisabledAndBecomesCallable(t *testing.T) {
	is, _ := newTestStore(t)
	ctx := context.Background()
	ty, caps := sampleType()
	ty.Form, ty.Custody = "command", CustodyLocal
	if _, err := is.UpsertType(ctx, ty, caps); err != nil {
		t.Fatalf("登记类型失败: %v", err)
	}

	inst, created, err := is.UpsertLocalInstance(ctx, "demo", "1.0.0")
	if err != nil || !created {
		t.Fatalf("创建本机实例失败: created=%v err=%v", created, err)
	}
	if inst.InstanceID != "local:demo" || inst.Enabled || !inst.Installed || inst.State != StateInstalled {
		t.Fatalf("本机实例初始状态不符: %+v", inst)
	}

	if err := is.SetEnabled(ctx, inst.InstanceID, true); err != nil {
		t.Fatalf("启用失败: %v", err)
	}
	inst, err = is.GetInstance(ctx, inst.InstanceID)
	if err != nil {
		t.Fatalf("读取实例失败: %v", err)
	}
	if !inst.Enabled || !inst.Callable || inst.State != StateCallable {
		t.Fatalf("本机实例启用后应可调用: %+v", inst)
	}
}

func TestRemoteInstanceRequiresProbeBeforeCallable(t *testing.T) {
	is, _ := newTestStore(t)
	ctx := context.Background()
	ty, caps := sampleType()
	if _, err := is.UpsertType(ctx, ty, caps); err != nil {
		t.Fatalf("登记类型失败: %v", err)
	}
	inst := Instance{TypeID: "demo", HostID: "host-1", PackageVersion: "1.0.0",
		ListenPort: 9443, Installed: true, Started: true, State: StateStarted}
	if err := is.UpsertRemoteInstance(ctx, inst); err != nil {
		t.Fatalf("写入远端实例失败: %v", err)
	}
	id := RemoteInstanceID("host-1", "demo")
	if err := is.SetEnabled(ctx, id, true); err != nil {
		t.Fatalf("启用失败: %v", err)
	}
	got, err := is.GetInstance(ctx, id)
	if err != nil {
		t.Fatalf("读取实例失败: %v", err)
	}
	if !got.Enabled || got.Callable {
		t.Fatalf("远端实例启用不等于可调用: %+v", got)
	}
	if got.State != StateStarted {
		t.Fatalf("远端实例状态应为 started，实际 %s", got.State)
	}
}

func TestProbeAndConfigRoundTrip(t *testing.T) {
	is, _ := newTestStore(t)
	ctx := context.Background()
	ty, caps := sampleType()
	if _, err := is.UpsertType(ctx, ty, caps); err != nil {
		t.Fatalf("登记类型失败: %v", err)
	}
	inst := Instance{TypeID: "demo", HostID: "host-1", Installed: true, Started: true, State: StateStarted}
	if err := is.UpsertRemoteInstance(ctx, inst); err != nil {
		t.Fatalf("写入远端实例失败: %v", err)
	}
	id := RemoteInstanceID("host-1", "demo")

	if err := is.RecordProbe(ctx, Probe{InstanceID: id, Stage: "ready", OK: true, LatencyMS: 12}); err != nil {
		t.Fatalf("写入探测失败: %v", err)
	}
	got, err := is.GetInstance(ctx, id)
	if err != nil || got.LastProbeResult != ProbeOK || got.LastProbeAt.IsZero() {
		t.Fatalf("探测信息未同步: %+v err=%v", got, err)
	}
	probes, err := is.ListProbes(ctx, id, 10)
	if err != nil || len(probes) != 1 || !probes[0].OK {
		t.Fatalf("探测历史不符: %+v err=%v", probes, err)
	}

	if err := is.SetConfig(ctx, id, "token", []byte("cipher"), "admin"); err != nil {
		t.Fatalf("写入配置失败: %v", err)
	}
	entries, err := is.ListConfig(ctx, id)
	if err != nil || len(entries) != 1 || entries[0].Key != "token" || entries[0].UpdatedBy != "admin" {
		t.Fatalf("配置读取不符: %+v err=%v", entries, err)
	}
	if err := is.DeleteConfig(ctx, id, "token"); err != nil {
		t.Fatalf("删除配置失败: %v", err)
	}
	entries, _ = is.ListConfig(ctx, id)
	if len(entries) != 0 {
		t.Fatalf("配置未删除: %+v", entries)
	}
}

func TestCertificateRevocation(t *testing.T) {
	is, _ := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	b := pki.Binding{
		Serial: "abc123", Fingerprint: "AA:BB", Purpose: "server", PluginID: "demo",
		HostID: "host-1", InstanceID: RemoteInstanceID("host-1", "demo"),
		Subject: "plugin:demo@host-1", SAN: "opsplatform://plugin/demo/host/host-1",
		NotBefore: now, NotAfter: now.Add(24 * time.Hour), CertPEM: "PEM", KeyCiphertext: []byte("k"),
		IssuedAt: now,
	}
	if err := is.SaveCertificate(ctx, b); err != nil {
		t.Fatalf("保存证书失败: %v", err)
	}
	if _, err := is.FindActiveCertificate(ctx, b.InstanceID, "server"); err != nil {
		t.Fatalf("应找到有效证书: %v", err)
	}
	if err := is.RevokeCertificate(ctx, b.Serial, "test", now); err != nil {
		t.Fatalf("吊销失败: %v", err)
	}
	if _, err := is.FindActiveCertificate(ctx, b.InstanceID, "server"); !errors.Is(err, pki.ErrCertificateNotFound) {
		t.Fatalf("吊销后不应再返回有效证书，实际 %v", err)
	}
	got, err := is.GetCertificate(ctx, b.Serial)
	if err != nil || !got.Revoked() {
		t.Fatalf("吊销状态未落库: %+v err=%v", got, err)
	}
}
