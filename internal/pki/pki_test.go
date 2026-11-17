package pki

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"net"
	"sort"
	"sync"
	"testing"
	"time"

	"operations-platform/internal/secret"
)

// memStore 是 pki.Store 的内存实现，便于在不依赖 SQLite 的情况下验证签发与吊销。
type memStore struct {
	mu       sync.Mutex
	bySerial map[string]Binding
}

func newMemStore() *memStore { return &memStore{bySerial: make(map[string]Binding)} }

func (m *memStore) SaveCertificate(_ context.Context, b Binding) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.bySerial[b.Serial] = b
	return nil
}

func (m *memStore) GetCertificate(_ context.Context, serial string) (Binding, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	b, ok := m.bySerial[serial]
	if !ok {
		return Binding{}, ErrCertificateNotFound
	}
	return b, nil
}

func (m *memStore) FindActiveCertificate(_ context.Context, instanceID, purpose string) (Binding, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var found []Binding
	for _, b := range m.bySerial {
		if b.InstanceID == instanceID && b.Purpose == purpose && !b.Revoked() {
			found = append(found, b)
		}
	}
	if len(found) == 0 {
		return Binding{}, ErrCertificateNotFound
	}
	sort.Slice(found, func(i, j int) bool { return found[i].IssuedAt.After(found[j].IssuedAt) })
	return found[0], nil
}

func (m *memStore) RevokeCertificate(_ context.Context, serial, reason string, at time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	b, ok := m.bySerial[serial]
	if !ok {
		return ErrCertificateNotFound
	}
	if !b.Revoked() {
		b.RevokedAt, b.RevokedReason = at, reason
		m.bySerial[serial] = b
	}
	return nil
}

func testManager(t *testing.T) (*Manager, *memStore) {
	t.Helper()
	box, err := secret.New(make([]byte, secret.KeySize))
	if err != nil {
		t.Fatalf("创建加密器失败: %v", err)
	}
	st := newMemStore()
	m, err := New(Options{DataDir: t.TempDir(), Box: box, Store: st, BaseID: "base-1"})
	if err != nil {
		t.Fatalf("初始化 CA 失败: %v", err)
	}
	return m, st
}

func TestNewRequiresMasterKey(t *testing.T) {
	_, err := New(Options{DataDir: t.TempDir(), Store: newMemStore()})
	if !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("缺少主密钥应返回 ErrNotConfigured，实际 %v", err)
	}
}

func TestIssueServerBindsIdentityAndPurpose(t *testing.T) {
	m, _ := testManager(t)
	ctx := context.Background()

	issued, err := m.IssueServer(ctx, "demo", "host-1", "remote:host-1:demo")
	if err != nil {
		t.Fatalf("签发服务端证书失败: %v", err)
	}
	if issued.URI != "opsplatform://plugin/demo/host/host-1" {
		t.Fatalf("URI SAN = %q", issued.URI)
	}
	block, _ := pem.Decode(issued.CertPEM)
	if block == nil {
		t.Fatal("证书 PEM 无法解析")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("解析证书失败: %v", err)
	}
	if len(cert.URIs) != 1 || cert.URIs[0].String() != issued.URI {
		t.Fatalf("证书 URI SAN 不符: %v", cert.URIs)
	}
	if len(cert.ExtKeyUsage) != 1 || cert.ExtKeyUsage[0] != x509.ExtKeyUsageServerAuth {
		t.Fatalf("证书 EKU 不符: %v", cert.ExtKeyUsage)
	}
	// 证书必须能由内置 CA 验证，且仅限服务端用途。
	if err := cert.CheckSignatureFrom(m.caCert); err != nil {
		t.Fatalf("证书不是内置 CA 签发: %v", err)
	}
	if _, err := cert.Verify(x509.VerifyOptions{
		Roots:     poolWith(m.caCert),
		KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}); err == nil {
		t.Fatal("服务端证书不应通过客户端用途校验")
	}
}

func TestIssueServerReusesActiveCertificate(t *testing.T) {
	m, _ := testManager(t)
	ctx := context.Background()

	first, err := m.IssueServer(ctx, "demo", "host-1", "remote:host-1:demo")
	if err != nil {
		t.Fatalf("首次签发失败: %v", err)
	}
	second, err := m.IssueServer(ctx, "demo", "host-1", "remote:host-1:demo")
	if err != nil {
		t.Fatalf("复用签发失败: %v", err)
	}
	if first.Serial != second.Serial {
		t.Fatalf("未过期证书应复用，序列号 %s != %s", first.Serial, second.Serial)
	}
}

func TestRevokeForcesReissueAndBlocksLoad(t *testing.T) {
	m, _ := testManager(t)
	ctx := context.Background()

	first, err := m.IssueServer(ctx, "demo", "host-1", "remote:host-1:demo")
	if err != nil {
		t.Fatalf("首次签发失败: %v", err)
	}
	if err := m.Revoke(ctx, first.Serial, "test"); err != nil {
		t.Fatalf("吊销失败: %v", err)
	}
	if _, _, err := m.Load(ctx, first.Serial); !errors.Is(err, ErrRevoked) {
		t.Fatalf("已吊销证书不应可加载，实际 %v", err)
	}
	second, err := m.IssueServer(ctx, "demo", "host-1", "remote:host-1:demo")
	if err != nil {
		t.Fatalf("重签失败: %v", err)
	}
	if second.Serial == first.Serial {
		t.Fatal("吊销后应签发新序列号")
	}
}

func TestClientTLSConfigRejectsUnexpectedURI(t *testing.T) {
	m, _ := testManager(t)
	ctx := context.Background()

	issued, err := m.IssueServer(ctx, "demo", "host-1", "remote:host-1:demo")
	if err != nil {
		t.Fatalf("签发失败: %v", err)
	}
	cfg, err := m.ClientTLSConfig(ctx, "opsplatform://plugin/other/host/host-1")
	if err != nil {
		t.Fatalf("构造客户端 TLS 失败: %v", err)
	}
	block, _ := pem.Decode(issued.CertPEM)
	if err := cfg.VerifyPeerCertificate([][]byte{block.Bytes}, nil); err == nil {
		t.Fatal("URI 不符的服务端证书应被拒绝")
	}
}

func TestMutualTLSHandshakeFailsAfterRevocation(t *testing.T) {
	m, _ := testManager(t)
	ctx := context.Background()

	issued, err := m.IssueServer(ctx, "demo", "host-1", "remote:host-1:demo")
	if err != nil {
		t.Fatalf("签发失败: %v", err)
	}
	serverCfg, err := m.ServerTLSConfig(ctx, issued)
	if err != nil {
		t.Fatalf("构造服务端 TLS 失败: %v", err)
	}
	clientCfg, err := m.ClientTLSConfig(ctx, issued.URI)
	if err != nil {
		t.Fatalf("构造客户端 TLS 失败: %v", err)
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("监听失败: %v", err)
	}
	defer ln.Close()
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				_ = tls.Server(c, serverCfg).Handshake()
			}(conn)
		}
	}()
	dialer := &net.Dialer{Timeout: 3 * time.Second}

	if conn, err := tls.DialWithDialer(dialer, "tcp", ln.Addr().String(), clientCfg); err != nil {
		t.Fatalf("吊销前握手应成功: %v", err)
	} else {
		conn.Close()
	}

	if err := m.Revoke(ctx, issued.Serial, "test"); err != nil {
		t.Fatalf("吊销失败: %v", err)
	}
	if conn, err := tls.DialWithDialer(dialer, "tcp", ln.Addr().String(), clientCfg); err == nil {
		conn.Close()
		t.Fatal("吊销后握手必须失败")
	}
}

func TestNeedsRenewal(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	if !NeedsRenewal(now.Add(24*time.Hour), now, DefaultLeafValidity) {
		t.Fatal("剩余一天应提示重签")
	}
	if NeedsRenewal(now.Add(DefaultLeafValidity-time.Hour), now, DefaultLeafValidity) {
		t.Fatal("剩余接近全周期不应提示重签")
	}
}

func poolWith(cert *x509.Certificate) *x509.CertPool {
	pool := x509.NewCertPool()
	pool.AddCert(cert)
	return pool
}
