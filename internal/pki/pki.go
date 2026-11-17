// Package pki 实现底座内置 CA 与远端插件 mTLS 证书签发（ADR-0022）。
//
// 边界：
//   - CA 私钥只以密文落盘（AES-256-GCM，主密钥来自 credential_key），永不下发、不入库、不写日志；
//   - 叶子证书按「插件标识 + 主机标识 + 用途」绑定到 URI SAN，不绑定端口；
//   - 吊销以序列号为准，底座在拨号前与 TLS 校验后都会检查；
//   - 算法、有效期、轮换等数值属实现基线（OQ-047），可通过 Options 覆盖。
package pki

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"operations-platform/internal/secret"
)

// 实现基线（OQ-047）：ECDSA P-256、CA 10 年、叶子 365 天、不自动轮换（重签由重新下发触发）。
const (
	DefaultCAValidity   = 10 * 365 * 24 * time.Hour
	DefaultLeafValidity = 365 * 24 * time.Hour
	DefaultAlgorithm    = "ecdsa-p256"
	caCertFileName      = "ca.crt"
	caKeyFileName       = "ca.key.enc"
	dirName             = "pki"
	uriScheme           = "opsplatform"
	purposeServer       = "server"
	purposeClient       = "client"
	renewBeforeRatio    = 0.1 // 剩余有效期低于 10% 时提示重签（不自动轮换）
	serialBytes         = 16
)

// ErrNotConfigured 表示主密钥缺失，证书读写必须显式失败。
var ErrNotConfigured = secret.ErrKeyNotConfigured

// ErrRevoked 表示证书已被吊销。
var ErrRevoked = errors.New("证书已吊销")

// ErrCertificateNotFound 表示没有匹配的证书记录。
var ErrCertificateNotFound = errors.New("证书不存在")

// Binding 是一条证书绑定记录（cert_bindings）。
type Binding struct {
	Serial        string    `json:"serial"`
	Fingerprint   string    `json:"fingerprint"`
	Purpose       string    `json:"purpose"`
	PluginID      string    `json:"plugin_id,omitempty"`
	HostID        string    `json:"host_id,omitempty"`
	InstanceID    string    `json:"instance_id,omitempty"`
	Subject       string    `json:"subject"`
	SAN           string    `json:"san"`
	NotBefore     time.Time `json:"not_before"`
	NotAfter      time.Time `json:"not_after"`
	CertPEM       string    `json:"-"`
	KeyCiphertext []byte    `json:"-"`
	RevokedAt     time.Time `json:"revoked_at,omitempty"`
	RevokedReason string    `json:"revoked_reason,omitempty"`
	IssuedAt      time.Time `json:"issued_at"`
}

// Revoked 判断记录是否已吊销。
func (b Binding) Revoked() bool { return !b.RevokedAt.IsZero() }

// Store 是 PKI 需要的最小持久化能力，便于测试替换。
type Store interface {
	SaveCertificate(ctx context.Context, b Binding) error
	GetCertificate(ctx context.Context, serial string) (Binding, error)
	FindActiveCertificate(ctx context.Context, instanceID, purpose string) (Binding, error)
	RevokeCertificate(ctx context.Context, serial, reason string, at time.Time) error
}

// Options 是 CA 的构造参数。
type Options struct {
	DataDir        string
	Box            *secret.Box
	Store          Store
	BaseID         string
	Now            func() time.Time
	CAValidity     time.Duration
	LeafValidity   time.Duration
	AlgorithmLabel string
}

// Manager 持有内置 CA 与签发逻辑；并发安全（CA 私钥只读）。
type Manager struct {
	box          *secret.Box
	store        Store
	caCert       *x509.Certificate
	caKey        *ecdsa.PrivateKey
	caPEM        []byte
	baseID       string
	now          func() time.Time
	leafValidity time.Duration
	algorithm    string
}

// Issued 是签发结果；KeyPEM 只允许写入目标机或加密落库，不得写日志。
type Issued struct {
	Serial      string
	Fingerprint string
	CertPEM     []byte
	KeyPEM      []byte
	NotBefore   time.Time
	NotAfter    time.Time
	URI         string
}

// New 加载或创建内置 CA。缺少主密钥时返回错误，不降级为明文私钥。
func New(opts Options) (*Manager, error) {
	if opts.DataDir == "" {
		return nil, errors.New("pki: DataDir 不能为空")
	}
	if opts.Box == nil {
		return nil, ErrNotConfigured
	}
	if opts.Store == nil {
		return nil, errors.New("pki: Store 不能为空")
	}
	if opts.BaseID == "" {
		opts.BaseID = "ops-platform"
	}
	if opts.Now == nil {
		opts.Now = func() time.Time { return time.Now().UTC() }
	}
	if opts.CAValidity <= 0 {
		opts.CAValidity = DefaultCAValidity
	}
	if opts.LeafValidity <= 0 {
		opts.LeafValidity = DefaultLeafValidity
	}
	if opts.AlgorithmLabel == "" {
		opts.AlgorithmLabel = DefaultAlgorithm
	}

	dir := filepath.Join(opts.DataDir, dirName)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("创建 PKI 目录失败: %w", err)
	}

	m := &Manager{
		box:          opts.Box,
		store:        opts.Store,
		baseID:       opts.BaseID,
		now:          opts.Now,
		leafValidity: opts.LeafValidity,
		algorithm:    opts.AlgorithmLabel,
	}

	certPath := filepath.Join(dir, caCertFileName)
	keyPath := filepath.Join(dir, caKeyFileName)
	certPEM, certErr := os.ReadFile(certPath)
	keyPEM, keyErr := os.ReadFile(keyPath)

	switch {
	case certErr == nil && keyErr == nil:
		if err := m.loadCA(certPEM, keyPEM); err != nil {
			return nil, err
		}
		return m, nil
	case errors.Is(certErr, os.ErrNotExist) && errors.Is(keyErr, os.ErrNotExist):
		if err := m.createCA(dir, opts.CAValidity); err != nil {
			return nil, err
		}
		return m, nil
	case certErr == nil && errors.Is(keyErr, os.ErrNotExist):
		return nil, errors.New("PKI 状态不一致：CA 证书存在但私钥缺失；请从备份恢复或人工重建 CA（重建会使所有已下发证书失效）")
	case errors.Is(certErr, os.ErrNotExist) && keyErr == nil:
		return nil, errors.New("PKI 状态不一致：CA 私钥存在但证书缺失；请从备份恢复")
	default:
		return nil, fmt.Errorf("读取 CA 材料失败: cert=%v key=%v", certErr, keyErr)
	}
}

func (m *Manager) createCA(dir string, validity time.Duration) error {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return fmt.Errorf("生成 CA 私钥失败: %w", err)
	}
	serial, err := randomSerial()
	if err != nil {
		return err
	}
	now := m.now()
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "ops-platform-ca", Organization: []string{"operations-platform"}},
		NotBefore:             now.Add(-5 * time.Minute),
		NotAfter:              now.Add(validity),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLenZero:        true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return fmt.Errorf("创建 CA 证书失败: %w", err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return fmt.Errorf("解析 CA 证书失败: %w", err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return fmt.Errorf("序列化 CA 私钥失败: %w", err)
	}
	enc, err := m.box.Encrypt(keyDER)
	if err != nil {
		return fmt.Errorf("加密 CA 私钥失败: %w", err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	if err := writeFile0600(filepath.Join(dir, caCertFileName), certPEM); err != nil {
		return err
	}
	if err := writeFile0600(filepath.Join(dir, caKeyFileName), enc); err != nil {
		return err
	}
	m.caCert, m.caKey, m.caPEM = cert, key, certPEM
	return nil
}

func (m *Manager) loadCA(certPEM, keyCipher []byte) error {
	block, _ := pem.Decode(certPEM)
	if block == nil || block.Type != "CERTIFICATE" {
		return errors.New("CA 证书格式非法")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return fmt.Errorf("解析 CA 证书失败: %w", err)
	}
	if !cert.IsCA {
		return errors.New("CA 证书缺少 CA 标记")
	}
	keyDER, err := m.box.Decrypt(keyCipher)
	if err != nil {
		return fmt.Errorf("解密 CA 私钥失败（主密钥可能已变更）: %w", err)
	}
	key, err := x509.ParseECPrivateKey(keyDER)
	if err != nil {
		return fmt.Errorf("解析 CA 私钥失败: %w", err)
	}
	if cert.CheckSignatureFrom(cert) != nil {
		return errors.New("CA 证书自签名校验失败")
	}
	m.caCert, m.caKey, m.caPEM = cert, key, certPEM
	return nil
}

// CACertPEM 返回 CA 证书 PEM，供下发与客户端校验。
func (m *Manager) CACertPEM() []byte {
	return append([]byte(nil), m.caPEM...)
}

// Algorithm 返回当前实现使用的算法基线（供接口展示）。
func (m *Manager) Algorithm() string { return m.algorithm }

// BaseID 返回本底座节点标识。
func (m *Manager) BaseID() string { return m.baseID }

// IssueServer 为「插件 + 主机」签发服务端证书；同一实例重复下发时复用未过期证书。
func (m *Manager) IssueServer(ctx context.Context, pluginID, hostID, instanceID string) (Issued, error) {
	if instanceID != "" {
		if issued, ok, err := m.reuse(ctx, instanceID, purposeServer); err == nil && ok {
			return issued, nil
		}
	}
	uri := fmt.Sprintf("%s://plugin/%s/host/%s", uriScheme, pluginID, hostID)
	return m.issue(ctx, purposeServer, pluginID, hostID, instanceID, "plugin:"+pluginID+"@"+hostID, uri,
		[]x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth})
}

// IssueClient 为底座节点签发客户端证书。
func (m *Manager) IssueClient(ctx context.Context, baseID string) (Issued, error) {
	if baseID == "" {
		baseID = m.baseID
	}
	uri := fmt.Sprintf("%s://base/%s", uriScheme, baseID)
	return m.issue(ctx, purposeClient, "", "", "", "base:"+baseID, uri,
		[]x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth})
}

// ReuseServer 返回实例下未吊销、未过期的服务端证书；不存在时返回 ok=false。
func (m *Manager) ReuseServer(ctx context.Context, instanceID string) (Issued, bool, error) {
	return m.reuse(ctx, instanceID, purposeServer)
}

func (m *Manager) reuse(ctx context.Context, instanceID, purpose string) (Issued, bool, error) {
	b, err := m.store.FindActiveCertificate(ctx, instanceID, purpose)
	if errors.Is(err, ErrCertificateNotFound) {
		return Issued{}, false, nil
	}
	if err != nil {
		return Issued{}, false, err
	}
	if b.Revoked() || b.NotAfter.Before(m.now()) || NeedsRenewal(b.NotAfter, m.now(), m.leafValidity) {
		return Issued{}, false, nil
	}
	issued, _, err := m.Load(ctx, b.Serial)
	if err != nil {
		return Issued{}, false, err
	}
	return issued, true, nil
}

// Load 读取指定序列号的证书材料（含私钥）。
func (m *Manager) Load(ctx context.Context, serial string) (Issued, Binding, error) {
	b, err := m.store.GetCertificate(ctx, serial)
	if err != nil {
		return Issued{}, Binding{}, err
	}
	if b.Revoked() {
		return Issued{}, b, ErrRevoked
	}
	keyDER, err := m.box.Decrypt(b.KeyCiphertext)
	if err != nil {
		return Issued{}, b, fmt.Errorf("解密证书私钥失败: %w", err)
	}
	key, err := x509.ParseECPrivateKey(keyDER)
	if err != nil {
		return Issued{}, b, fmt.Errorf("解析证书私钥失败: %w", err)
	}
	keyPEM, err := pemEncodeKey(key)
	if err != nil {
		return Issued{}, b, err
	}
	return Issued{
		Serial:      b.Serial,
		Fingerprint: b.Fingerprint,
		CertPEM:     []byte(b.CertPEM),
		KeyPEM:      keyPEM,
		NotBefore:   b.NotBefore,
		NotAfter:    b.NotAfter,
		URI:         b.SAN,
	}, b, nil
}

// Revoke 吊销证书序列号；重复吊销保持幂等。
func (m *Manager) Revoke(ctx context.Context, serial, reason string) error {
	if serial == "" {
		return nil
	}
	return m.store.RevokeCertificate(ctx, serial, reason, m.now())
}

// IsRevoked 查询吊销状态；查询失败按「已吊销」处理，避免不确定放行。
func (m *Manager) IsRevoked(ctx context.Context, serial string) bool {
	b, err := m.store.GetCertificate(ctx, serial)
	if err != nil {
		return true
	}
	return b.Revoked()
}

// ClientTLSConfig 构造底座侧客户端 TLS 配置：校验服务端证书链、EKU、URI 与吊销。
func (m *Manager) ClientTLSConfig(ctx context.Context, expectedURI string) (*tls.Config, error) {
	issued, err := m.ensureClientCert(ctx)
	if err != nil {
		return nil, err
	}
	cert, err := tls.X509KeyPair(issued.CertPEM, issued.KeyPEM)
	if err != nil {
		return nil, fmt.Errorf("加载客户端证书失败: %w", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(m.caPEM) {
		return nil, errors.New("CA 证书无法加入信任池")
	}
	return &tls.Config{
		MinVersion:   tls.VersionTLS12,
		RootCAs:      pool,
		Certificates: []tls.Certificate{cert},
		// 服务端证书以 URI SAN 绑定「插件 + 主机」，不使用 DNS 主机名，
		// 因此关闭默认主机名校验并在此处做更严格的链 + EKU + URI + 吊销校验。
		InsecureSkipVerify: true,
		VerifyPeerCertificate: func(rawCerts [][]byte, _ [][]*x509.Certificate) error {
			return m.verifyPeer(ctx, rawCerts, expectedURI, x509.ExtKeyUsageServerAuth)
		},
	}, nil
}

// ServerTLSConfig 构造服务端 TLS 配置（测试与本地验证使用）。
func (m *Manager) ServerTLSConfig(ctx context.Context, issued Issued) (*tls.Config, error) {
	cert, err := tls.X509KeyPair(issued.CertPEM, issued.KeyPEM)
	if err != nil {
		return nil, fmt.Errorf("加载服务端证书失败: %w", err)
	}
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(m.caPEM)
	return &tls.Config{
		MinVersion:   tls.VersionTLS12,
		Certificates: []tls.Certificate{cert},
		ClientAuth:   tls.RequireAndVerifyClientCert,
		ClientCAs:    pool,
		VerifyPeerCertificate: func(rawCerts [][]byte, _ [][]*x509.Certificate) error {
			return m.verifyPeer(ctx, rawCerts, "", x509.ExtKeyUsageClientAuth)
		},
	}, nil
}

func (m *Manager) verifyPeer(ctx context.Context, rawCerts [][]byte, expectedURI string, usage x509.ExtKeyUsage) error {
	if len(rawCerts) == 0 {
		return errors.New("对端未提供证书")
	}
	cert, err := x509.ParseCertificate(rawCerts[0])
	if err != nil {
		return fmt.Errorf("解析对端证书失败: %w", err)
	}
	if len(cert.URIs) == 0 {
		return errors.New("对端证书缺少 URI SAN")
	}
	allowed := false
	for _, u := range cert.URIs {
		if expectedURI == "" || u.String() == expectedURI {
			allowed = true
			break
		}
	}
	if !allowed {
		return fmt.Errorf("对端证书 URI 与预期不符：%s", cert.URIs[0].String())
	}
	inter := x509.NewCertPool()
	for _, raw := range rawCerts[1:] {
		if c, err := x509.ParseCertificate(raw); err == nil {
			inter.AddCert(c)
		}
	}
	pool := x509.NewCertPool()
	pool.AddCert(m.caCert)
	if _, err := cert.Verify(x509.VerifyOptions{
		Roots:         pool,
		Intermediates: inter,
		KeyUsages:     []x509.ExtKeyUsage{usage},
		CurrentTime:   m.now(),
	}); err != nil {
		return fmt.Errorf("对端证书链校验失败: %w", err)
	}
	if m.IsRevoked(ctx, cert.SerialNumber.Text(16)) {
		return ErrRevoked
	}
	return nil
}

func (m *Manager) ensureClientCert(ctx context.Context) (Issued, error) {
	// 客户端证书按底座标识复用；过期或接近过期时重签。
	instanceID := "base:" + m.baseID
	if issued, ok, err := m.reuse(ctx, instanceID, purposeClient); err == nil && ok {
		return issued, nil
	}
	return m.issue(ctx, purposeClient, "", "", instanceID, "base:"+m.baseID,
		fmt.Sprintf("%s://base/%s", uriScheme, m.baseID), []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth})
}

func (m *Manager) issue(ctx context.Context, purpose, pluginID, hostID, instanceID, cn, uri string,
	usages []x509.ExtKeyUsage) (Issued, error) {
	if m.caCert == nil || m.caKey == nil {
		return Issued{}, errors.New("CA 未初始化")
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return Issued{}, fmt.Errorf("生成插件私钥失败: %w", err)
	}
	serial, err := randomSerial()
	if err != nil {
		return Issued{}, err
	}
	now := m.now()
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: cn, Organization: []string{"operations-platform"}},
		NotBefore:    now.Add(-5 * time.Minute),
		NotAfter:     now.Add(m.leafValidity),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  usages,
	}
	u, err := url.Parse(uri)
	if err != nil {
		return Issued{}, fmt.Errorf("构造证书 URI 失败: %w", err)
	}
	tmpl.URIs = []*url.URL{u}

	der, err := x509.CreateCertificate(rand.Reader, tmpl, m.caCert, &key.PublicKey, m.caKey)
	if err != nil {
		return Issued{}, fmt.Errorf("签发证书失败: %w", err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM, err := pemEncodeKey(key)
	if err != nil {
		return Issued{}, err
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return Issued{}, fmt.Errorf("序列化证书私钥失败: %w", err)
	}
	keyCipher, err := m.box.Encrypt(keyDER)
	if err != nil {
		return Issued{}, fmt.Errorf("加密证书私钥失败: %w", err)
	}
	sum := sha256.Sum256(der)
	issued := Issued{
		Serial:      serial.Text(16),
		Fingerprint: colonHex(sum[:]),
		CertPEM:     certPEM,
		KeyPEM:      keyPEM,
		NotBefore:   tmpl.NotBefore,
		NotAfter:    tmpl.NotAfter,
		URI:         uri,
	}
	binding := Binding{
		Serial:        issued.Serial,
		Fingerprint:   issued.Fingerprint,
		Purpose:       purpose,
		PluginID:      pluginID,
		HostID:        hostID,
		InstanceID:    instanceID,
		Subject:       cn,
		SAN:           uri,
		NotBefore:     tmpl.NotBefore,
		NotAfter:      tmpl.NotAfter,
		CertPEM:       string(certPEM),
		KeyCiphertext: keyCipher,
		IssuedAt:      now,
	}
	if err := m.store.SaveCertificate(ctx, binding); err != nil {
		return Issued{}, fmt.Errorf("登记证书失败: %w", err)
	}
	return issued, nil
}

// NeedsRenewal 判断证书是否接近过期（基线：剩余不足 10%）。
func NeedsRenewal(notAfter time.Time, now time.Time, total time.Duration) bool {
	if total <= 0 {
		total = DefaultLeafValidity
	}
	return notAfter.Sub(now) < time.Duration(float64(total)*renewBeforeRatio)
}

func randomSerial() (*big.Int, error) {
	buf := make([]byte, serialBytes)
	if _, err := rand.Read(buf); err != nil {
		return nil, fmt.Errorf("生成证书序列号失败: %w", err)
	}
	buf[0] &= 0x7f // 保证正数
	n := new(big.Int).SetBytes(buf)
	if n.Sign() == 0 {
		n.SetInt64(1)
	}
	return n, nil
}

func pemEncodeKey(key *ecdsa.PrivateKey) ([]byte, error) {
	der, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, fmt.Errorf("序列化证书私钥失败: %w", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der}), nil
}

func writeFile0600(path string, data []byte) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("写入 PKI 文件 %q 失败: %w", path, err)
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return fmt.Errorf("写入 PKI 文件 %q 失败: %w", path, err)
	}
	return f.Close()
}

func colonHex(b []byte) string {
	parts := make([]string, len(b))
	for i, v := range b {
		parts[i] = strings.ToUpper(hex.EncodeToString([]byte{v}))
	}
	return strings.Join(parts, ":")
}
