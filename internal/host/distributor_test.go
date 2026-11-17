package host

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type fakeConn struct {
	fingerprint string
	archiveSHA  string
	failOn      string
	uploaded    int
	installed   bool
}

func (c *fakeConn) Fingerprint() string { return c.fingerprint }
func (c *fakeConn) Close() error        { return nil }

func (c *fakeConn) Run(_ context.Context, command string, stdin io.Reader) ([]byte, []byte, error) {
	switch {
	case strings.Contains(command, "cat >"):
		if c.failOn == "upload" {
			return nil, []byte("disk full"), errors.New("exit 1")
		}
		if stdin != nil {
			data, _ := io.ReadAll(stdin)
			c.uploaded = len(data)
		}
		return nil, nil, nil
	case strings.HasPrefix(command, "sha256sum"):
		return []byte(c.archiveSHA + "  file\n"), nil, nil
	case strings.Contains(command, "tar -xzf"):
		if c.failOn == "install" {
			return nil, []byte("tar: unexpected EOF"), errors.New("exit 2")
		}
		c.installed = true
		return nil, nil, nil
	default:
		return nil, nil, nil
	}
}

type fakeDialer struct {
	conn *fakeConn
	err  error
}

func (d *fakeDialer) Dial(context.Context, Host, Credential) (Conn, error) {
	if d.err != nil {
		return nil, d.err
	}
	return d.conn, nil
}

func testPluginDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	manifest := "schema_version: 1\nidentity:\n  id: demo\n  name: 示例\nversion:\n  plugin: 1.0.0\n"
	if err := os.WriteFile(filepath.Join(dir, "plugin.yaml"), []byte(manifest), 0o644); err != nil {
		t.Fatalf("写入清单失败: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "run.sh"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("写入文件失败: %v", err)
	}
	return dir
}

func newDistributorFixture(t *testing.T, conn *fakeConn) (*Distributor, *Store, Distribution, Package) {
	t.Helper()
	s := newTestStore(t)
	ctx := context.Background()
	h := newHostFixture()
	if err := s.Create(ctx, h, []byte("cipher")); err != nil {
		t.Fatalf("创建主机失败: %v", err)
	}
	d, err := s.CreateDistribution(ctx, Distribution{
		ID: "dist-1", HostID: h.ID, PluginID: "demo", PluginVersion: "1.0.0", IdempotencyKey: "demo@1.0.0",
	})
	if err != nil {
		t.Fatalf("创建下发任务失败: %v", err)
	}
	pkg, err := ArchiveDir(testPluginDir(t))
	if err != nil {
		t.Fatalf("打包插件失败: %v", err)
	}
	pkg.PluginID, pkg.Version = "demo", "1.0.0"
	conn.archiveSHA = pkg.ArchiveSHA

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	dist := NewDistributor(DistributorOptions{
		Store:         s,
		Dialer:        &fakeDialer{conn: conn},
		Logger:        logger,
		RemoteRoot:    "/opt/ops-platform/plugins",
		UploadTimeout: 10 * time.Second,
	})
	return dist, s, d, pkg
}

func TestDistributorSuccessFlow(t *testing.T) {
	conn := &fakeConn{fingerprint: "SHA256:test"}
	dist, s, d, pkg := newDistributorFixture(t, conn)
	h := newHostFixture()
	dist.process(d, h, Credential{Method: AuthMethodPassword, Password: "x"}, pkg, "user-admin", nil)

	got, err := s.GetDistribution(context.Background(), d.ID)
	if err != nil {
		t.Fatalf("读取下发任务失败: %v", err)
	}
	if got.State != DistInstalled {
		t.Fatalf("状态 = %q，期望 %q（错误：%s）", got.State, DistInstalled, got.ErrorMessage)
	}
	if !conn.installed || conn.uploaded != len(pkg.Archive) {
		t.Fatalf("归档未完整上传或安装未执行：uploaded=%d installed=%v", conn.uploaded, conn.installed)
	}
	if got.RemotePath != "/opt/ops-platform/plugins/demo/1.0.0" {
		t.Fatalf("RemotePath = %q", got.RemotePath)
	}
	if got.Callable || got.StartedAt == nil || got.FinishedAt == nil {
		t.Fatalf("终态缺少时间或错误标记可调用: %+v", got)
	}

	// TOFU：成功连接后应锁存指纹。
	hostRec, err := s.Get(context.Background(), h.ID)
	if err != nil {
		t.Fatalf("读取主机失败: %v", err)
	}
	if !hostRec.HostKeyTrusted || hostRec.HostKeyFP != "SHA256:test" {
		t.Fatalf("首次连接未锁存指纹: %+v", hostRec)
	}
}

func TestDistributorVerifyMismatch(t *testing.T) {
	conn := &fakeConn{fingerprint: "SHA256:test"}
	dist, s, d, pkg := newDistributorFixture(t, conn)
	conn.archiveSHA = "deadbeef"
	dist.process(d, newHostFixture(), Credential{Method: AuthMethodPassword, Password: "x"}, pkg, "user-admin", nil)

	got, _ := s.GetDistribution(context.Background(), d.ID)
	if got.State != DistFailed || got.ErrorCategory != CatVerifyFailed {
		t.Fatalf("状态 = %q / %q，期望 failed/verify_failed", got.State, got.ErrorCategory)
	}
	if conn.installed {
		t.Fatal("摘要不一致时不得执行安装")
	}
}

func TestDistributorUploadFailure(t *testing.T) {
	conn := &fakeConn{fingerprint: "SHA256:test", failOn: "upload"}
	dist, s, d, pkg := newDistributorFixture(t, conn)
	dist.process(d, newHostFixture(), Credential{Method: AuthMethodPassword, Password: "x"}, pkg, "user-admin", nil)

	got, _ := s.GetDistribution(context.Background(), d.ID)
	if got.State != DistFailed || got.ErrorCategory != CatUploadFailed {
		t.Fatalf("状态 = %q / %q，期望 failed/upload_failed", got.State, got.ErrorCategory)
	}
}

func TestDistributorConnectFailure(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	h := newHostFixture()
	if err := s.Create(ctx, h, []byte("cipher")); err != nil {
		t.Fatalf("创建主机失败: %v", err)
	}
	d, _ := s.CreateDistribution(ctx, Distribution{ID: "dist-1", HostID: h.ID, PluginID: "demo", PluginVersion: "1.0.0", IdempotencyKey: "k"})
	pkg, _ := ArchiveDir(testPluginDir(t))
	pkg.PluginID, pkg.Version = "demo", "1.0.0"

	dist := NewDistributor(DistributorOptions{
		Store:      s,
		Dialer:     &fakeDialer{err: &ConnectError{Category: CatAuthFailed, Err: errors.New("unable to authenticate")}},
		Logger:     slog.New(slog.NewTextHandler(io.Discard, nil)),
		RemoteRoot: "/opt/ops-platform/plugins",
	})
	dist.process(d, h, Credential{Method: AuthMethodPassword, Password: "x"}, pkg, "user-admin", nil)
	got, _ := s.GetDistribution(ctx, d.ID)
	if got.State != DistFailed || got.ErrorCategory != CatAuthFailed {
		t.Fatalf("状态 = %q / %q，期望 failed/auth_failed", got.State, got.ErrorCategory)
	}
}

func TestArchiveDirRequiresManifest(t *testing.T) {
	dir := t.TempDir()
	if _, err := ArchiveDir(dir); err == nil {
		t.Fatal("缺少 plugin.yaml 时打包应失败")
	}
}
