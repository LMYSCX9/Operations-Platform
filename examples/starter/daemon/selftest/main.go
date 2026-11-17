// selftest 是 daemon 骨架的本地端到端自测工具（开发用，不随插件交付）。
//
// 流程：生成一次性测试证书与按主机配置 → 以底座注入参数拉起插件进程 →
// 走 mTLS 完成 Ready / GetVersion / Invoke / 错误映射 / Shutdown → 断言全部通过。
//
// 红线说明：这里的 CA 与证书只服务于本地自测；生产环境证书一律由底座内置 CA 签发并下发，
// 插件不得自行生成或替换（plugin-spec/03 §3.7、ADR-0022）。
package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"

	pluginv1 "example.com/starter-daemon/gen/pluginv1"
)

const (
	serverURI   = "opsplatform://plugin/starter-daemon/host/selftest"
	clientCN    = "base:selftest"
	clientURI   = "opsplatform://base/selftest"
	expectedCap = "starter-daemon.now"
)

func fail(format string, args ...any) {
	if pluginProc != nil {
		_ = pluginProc.Kill()
	}
	fmt.Printf("selftest FAIL: "+format+"\n", args...)
	os.Exit(1)
}

// pluginProc 是被测插件进程；fail() 退出前兜底终止，避免残留后台进程。
var pluginProc *os.Process

func main() {
	bin := "bin/starter-daemon"
	if runtime.GOOS == "windows" {
		bin = "bin/starter-daemon.exe"
	}
	if len(os.Args) > 1 {
		bin = os.Args[1]
	}
	if _, err := os.Stat(bin); err != nil {
		fail("插件二进制 %s 不存在：先用 go build 构建到插件目录 bin/ 下", bin)
	}

	dir, err := os.MkdirTemp("", "starter-daemon-selftest-")
	if err != nil {
		fail("创建临时目录失败: %v", err)
	}
	defer os.RemoveAll(dir)

	// --- 1. 一次性测试 CA + 服务端/客户端证书（ECDSA P-256，仅本地自测）---
	ca, err := issue(nil, nil, "starter-daemon-selftest-ca", "", true, nil)
	if err != nil {
		fail("生成测试 CA 失败: %v", err)
	}
	serverIss, err := issue(ca.cert, ca.key, "starter-daemon-selftest-server", serverURI, false,
		[]x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth})
	if err != nil {
		fail("生成服务端证书失败: %v", err)
	}
	clientIss, err := issue(ca.cert, ca.key, clientCN, clientURI, false,
		[]x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth})
	if err != nil {
		fail("生成客户端证书失败: %v", err)
	}

	tlsDir := filepath.Join(dir, "tls")
	dataDir := filepath.Join(dir, "data")
	for _, d := range []string{tlsDir, dataDir} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			fail("创建目录失败: %v", err)
		}
	}
	certPath := filepath.Join(tlsDir, "server.crt")
	keyPath := filepath.Join(tlsDir, "server.key")
	caPath := filepath.Join(tlsDir, "ca.crt")
	writeFile(certPath, certPEM(serverIss.cert))
	writeFile(keyPath, keyPEM(serverIss.key))
	writeFile(caPath, certPEM(ca.cert))

	// --- 2. 按主机配置（形状与底座 renderRemoteConfig 一致）---
	cfgPath := filepath.Join(dir, "config.yaml")
	port := freePort()
	cfg := fmt.Sprintf(`deployment: remote
plugin_id: starter-daemon
instance_id: selftest-1
listen:
  address: 127.0.0.1
  port: %d
tls:
  cert_file: %s
  key_file: %s
  ca_file: %s
  expected_client: %s
data_dir: %s
config:
  label: selftest-label
`, port, certPath, keyPath, caPath, clientCN, dataDir)
	writeFile(cfgPath, []byte(cfg))

	// --- 3. 以底座注入参数拉起插件（多传一个未声明的 --ops-log-level，验证容忍性）---
	cmd := exec.Command(bin,
		"--ops-plugin-id", "starter-daemon",
		"--ops-instance-id", "selftest-1",
		"--ops-config", cfgPath,
		"--ops-data-dir", dataDir,
		"--ops-contract-version", "1.2",
		"--ops-log-level", "debug",
	)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		fail("启动插件进程失败: %v", err)
	}
	// 失败路径经 fail() 统一杀进程（os.Exit 不执行 defer）；成功路径进程已退出。
	pluginProc = cmd.Process
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	// --- 4. mTLS 客户端：校验服务端 URI SAN 与链，出示客户端证书 ---
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	conn, err := dial(ctx, fmt.Sprintf("127.0.0.1:%d", port), ca.cert, clientIss)
	if err != nil {
		fail("mTLS 连接失败: %v", err)
	}
	defer conn.Close()
	cli := pluginv1.NewPluginServiceClient(conn)

	ready, err := cli.Ready(ctx, &pluginv1.ReadyRequest{})
	if err != nil || !ready.GetReady() {
		fail("Ready 未通过: err=%v resp=%v", err, ready)
	}
	ver, err := cli.GetVersion(ctx, &pluginv1.GetVersionRequest{})
	if err != nil {
		fail("GetVersion 失败: %v", err)
	}
	if ver.GetContractVersion() != pluginv1.ContractVersion {
		fail("契约版本 %q 与预期 %s 不符", ver.GetContractVersion(), pluginv1.ContractVersion)
	}

	// 5a. 正常调用：输出 label 来自按主机配置的 config 段（实例配置作用域）。
	out, err := cli.Invoke(ctx, &pluginv1.InvokeRequest{
		ContractVersion: "1.2",
		Capability:      expectedCap,
		InputJson:       []byte(`{"format":"rfc3339"}`),
		Context:         &pluginv1.InvocationContext{InvocationId: "t1", Subject: "user:selftest"},
	})
	if err != nil || out.GetStatus() != pluginv1.StatusOK {
		fail("Invoke now 失败: err=%v resp=%v", err, out)
	}
	var payload struct {
		Now   string `json:"now"`
		Label string `json:"label"`
	}
	if err := json.Unmarshal(out.GetOutputJson(), &payload); err != nil {
		fail("输出不是合法 JSON: %v", err)
	}
	if payload.Label != "selftest-label" {
		fail("实例配置 label 未生效: %q", payload.Label)
	}
	if _, err := time.Parse(time.RFC3339Nano, payload.Now); err != nil {
		fail("now 不是 RFC3339: %q", payload.Now)
	}

	// 5b. 错误映射：非法 format → input_invalid；未知能力 → capability_unknown。
	bad, err := cli.Invoke(ctx, &pluginv1.InvokeRequest{
		ContractVersion: "1.2", Capability: expectedCap, InputJson: []byte(`{"format":"rfc1123"}`),
	})
	if err != nil || bad.GetError().GetCode() != pluginv1.CodeInputInvalid {
		fail("期望 input_invalid: err=%v code=%q", err, bad.GetError().GetCode())
	}
	unknown, err := cli.Invoke(ctx, &pluginv1.InvokeRequest{
		ContractVersion: "1.2", Capability: "starter-daemon.nope", InputJson: []byte(`{}`),
	})
	if err != nil || unknown.GetError().GetCode() != pluginv1.CodeCapabilityUnknown {
		fail("期望 capability_unknown: err=%v code=%q", err, unknown.GetError().GetCode())
	}

	// 6. Shutdown 后进程应自行退出（退出码 0）。
	shut, err := cli.Shutdown(ctx, &pluginv1.ShutdownRequest{GraceMs: 3000})
	if err != nil || !shut.GetAccepted() {
		fail("Shutdown 未被接受: err=%v resp=%v", err, shut)
	}
	select {
	case err := <-done:
		if err != nil {
			fail("插件退出码非 0: %v", err)
		}
	case <-time.After(10 * time.Second):
		fail("插件未在 10s 内退出")
	}

	fmt.Println("selftest ok: Ready/GetVersion/Invoke(label+now)/input_invalid/capability_unknown/Shutdown")
}

func dial(ctx context.Context, addr string, caCert *x509.Certificate, client *issued) (*grpc.ClientConn, error) {
	cfg := &tls.Config{
		MinVersion:   tls.VersionTLS12,
		RootCAs:      poolOf(caCert),
		Certificates: []tls.Certificate{client.tlsCertificate()},
		// 服务端证书用 URI SAN 绑定身份（无 DNS 名），关闭默认主机名校验并自做链 + EKU + URI 校验。
		InsecureSkipVerify: true,
		VerifyPeerCertificate: func(rawCerts [][]byte, _ [][]*x509.Certificate) error {
			return verifyServer(rawCerts, serverURI, caCert)
		},
	}
	return grpc.DialContext(ctx, addr, grpc.WithTransportCredentials(credentials.NewTLS(cfg)), grpc.WithBlock())
}

func verifyServer(rawCerts [][]byte, expectedURI string, caCert *x509.Certificate) error {
	if len(rawCerts) == 0 {
		return fmt.Errorf("服务端未出示证书")
	}
	leaf, err := x509.ParseCertificate(rawCerts[0])
	if err != nil {
		return err
	}
	found := false
	for _, u := range leaf.URIs {
		if u.String() == expectedURI {
			found = true
			break
		}
	}
	if !found {
		return fmt.Errorf("服务端证书缺少 URI SAN %s", expectedURI)
	}
	if _, err := leaf.Verify(x509.VerifyOptions{
		Roots:     poolOf(caCert),
		KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}); err != nil {
		return fmt.Errorf("服务端证书链校验失败: %w", err)
	}
	return nil
}

// issued 是签发结果：证书对象 + 私钥。
type issued struct {
	cert *x509.Certificate
	key  *ecdsa.PrivateKey
}

func (c *issued) tlsCertificate() tls.Certificate {
	return tls.Certificate{Certificate: [][]byte{c.cert.Raw}, PrivateKey: c.key}
}

// issue 用给定父证书签发；parent 为 nil 时自签为测试 CA（仅本地自测使用）。
func issue(parent *x509.Certificate, parentKey *ecdsa.PrivateKey, cn, uri string, isCA bool,
	usages []x509.ExtKeyUsage) (*issued, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(time.Now().UnixNano()),
		Subject:               pkix.Name{CommonName: cn, Organization: []string{"starter-daemon-selftest"}},
		NotBefore:             time.Now().Add(-time.Minute),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           usages,
		BasicConstraintsValid: true,
		IsCA:                  isCA,
	}
	if parent == nil {
		parent, parentKey = tmpl, key
	}
	if uri != "" {
		u, err := url.Parse(uri)
		if err != nil {
			return nil, err
		}
		tmpl.URIs = []*url.URL{u}
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, parent, &key.PublicKey, parentKey)
	if err != nil {
		return nil, err
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, err
	}
	return &issued{cert: cert, key: key}, nil
}

func certPEM(cert *x509.Certificate) []byte {
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Raw})
}

func keyPEM(key *ecdsa.PrivateKey) []byte {
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		fail("编码私钥失败: %v", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
}

func poolOf(cert *x509.Certificate) *x509.CertPool {
	pool := x509.NewCertPool()
	pool.AddCert(cert)
	return pool
}

func freePort() int {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		fail("分配端口失败: %v", err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

func writeFile(path string, data []byte) {
	if err := os.WriteFile(path, data, 0o600); err != nil {
		fail("写入 %s 失败: %v", path, err)
	}
}
