// Package remote 实现底座到远端常驻插件的 gRPC over TCP + mTLS 调用（ADR-0005、ADR-0022）。
//
// 约束：
//   - 只走 TLS，任何失败都不得回退为明文；
//   - 对端身份由内置 CA 签发的证书 + URI SAN 绑定「插件 + 主机」承担；
//   - 连接、握手、Ready/GetVersion、Invoke 分阶段超时，失败阶段可定位；
//   - 底座只探测，不通过 SSH 对远端进程发信号。
package remote

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/status"

	pluginv1 "operations-platform/sdk/plugin/v1"

	"operations-platform/internal/pki"
	"operations-platform/internal/plugin/runtime"
)

// 实现基线（OQ-048/OQ-049）：连接 5s、Ready 3s、握手总预算含在连接内。
const (
	DefaultDialTimeout  = 5 * time.Second
	DefaultReadyTimeout = 3 * time.Second
)

// TLSProvider 是可替换的证书来源，便于测试。
type TLSProvider interface {
	ClientTLSConfig(ctx context.Context, expectedURI string) (*tls.Config, error)
}

// Options 构造客户端参数。
type Options struct {
	TLS          TLSProvider
	DialTimeout  time.Duration
	ReadyTimeout time.Duration
}

// Target 描述一次远端调用目标。
type Target struct {
	HostID      string
	PluginID    string
	InstanceID  string
	Address     string // IP 或域名
	Port        int
	ContractVer string
}

// Endpoint 返回 host:port。
func (t Target) Endpoint() string { return fmt.Sprintf("%s:%d", t.Address, t.Port) }

// ServerURI 返回服务端证书应携带的 URI SAN。
func (t Target) ServerURI() string {
	return fmt.Sprintf("opsplatform://plugin/%s/host/%s", t.PluginID, t.HostID)
}

// Client 是线程安全的远端调用器。
type Client struct {
	tls          TLSProvider
	dialTimeout  time.Duration
	readyTimeout time.Duration

	mu    sync.Mutex
	cache map[string]*grpc.ClientConn
}

// New 创建远端客户端。
func New(opts Options) *Client {
	if opts.DialTimeout <= 0 {
		opts.DialTimeout = DefaultDialTimeout
	}
	if opts.ReadyTimeout <= 0 {
		opts.ReadyTimeout = DefaultReadyTimeout
	}
	return &Client{
		tls:          opts.TLS,
		dialTimeout:  opts.DialTimeout,
		readyTimeout: opts.ReadyTimeout,
		cache:        make(map[string]*grpc.ClientConn),
	}
}

// Close 关闭所有缓存连接。
func (c *Client) Close() {
	c.mu.Lock()
	defer c.mu.Unlock()
	for k, conn := range c.cache {
		_ = conn.Close()
		delete(c.cache, k)
	}
}

func (c *Client) conn(ctx context.Context, t Target) (*grpc.ClientConn, error) {
	if c.tls == nil {
		return nil, errors.New("远端调用缺少 TLS 配置")
	}
	key := t.Endpoint() + "|" + t.ServerURI()
	c.mu.Lock()
	if conn, ok := c.cache[key]; ok {
		c.mu.Unlock()
		return conn, nil
	}
	c.mu.Unlock()

	cfg, err := c.tls.ClientTLSConfig(ctx, t.ServerURI())
	if err != nil {
		return nil, err
	}
	dialCtx, cancel := context.WithTimeout(ctx, c.dialTimeout)
	defer cancel()
	conn, err := grpc.DialContext(dialCtx, t.Endpoint(),
		grpc.WithTransportCredentials(credentials.NewTLS(cfg)),
		grpc.WithBlock(),
	)
	if err != nil {
		return nil, fmt.Errorf("连接远端插件 %s 失败: %w", t.Endpoint(), err)
	}
	c.mu.Lock()
	c.cache[key] = conn
	c.mu.Unlock()
	return conn, nil
}

// ProbeResult 是探测结果；Stage 指出失败阶段（connect/tls/ready/version）。
type ProbeResult struct {
	OK          bool
	Stage       string
	Detail      string
	Latency     time.Duration
	Version     string
	ContractVer string
	Fingerprint string
}

// PeerFingerprint 与目标机做一次独立 TLS 握手，返回服务端叶子证书的 SHA-256 指纹。
//
// 用途：漂移检测需要比对「目标机实际出示的证书」与下发时的绑定；gRPC 连接不暴露
// 对端证书，因此单独握手一次。校验仍复用 ClientTLSConfig（链、EKU、URI、有效期、吊销）。
func (c *Client) PeerFingerprint(ctx context.Context, t Target) (string, error) {
	if c.tls == nil {
		return "", errors.New("远端调用缺少 TLS 配置")
	}
	cfg, err := c.tls.ClientTLSConfig(ctx, t.ServerURI())
	if err != nil {
		return "", err
	}
	dialCtx, cancel := context.WithTimeout(ctx, c.dialTimeout)
	defer cancel()
	raw, err := (&net.Dialer{}).DialContext(dialCtx, "tcp", t.Endpoint())
	if err != nil {
		return "", fmt.Errorf("连接远端插件 %s 失败: %w", t.Endpoint(), err)
	}
	defer raw.Close()
	conn := tls.Client(raw, cfg)
	if err := conn.HandshakeContext(dialCtx); err != nil {
		return "", fmt.Errorf("远端 TLS 握手失败: %w", err)
	}
	state := conn.ConnectionState()
	if len(state.PeerCertificates) == 0 {
		return "", errors.New("远端未出示证书")
	}
	sum := sha256.Sum256(state.PeerCertificates[0].Raw)
	return hex.EncodeToString(sum[:]), nil
}

// Probe 依次执行连接、TLS 握手、Ready 与 GetVersion。
func (c *Client) Probe(ctx context.Context, t Target) ProbeResult {
	start := time.Now()
	res := func(stage string, err error) ProbeResult {
		return ProbeResult{OK: false, Stage: stage, Detail: err.Error(), Latency: time.Since(start)}
	}
	conn, err := c.conn(ctx, t)
	if err != nil {
		stage := "connect"
		if strings.Contains(err.Error(), "证书") || strings.Contains(err.Error(), "URI") ||
			strings.Contains(err.Error(), "tls") || strings.Contains(err.Error(), "x509") {
			stage = "tls"
		}
		return res(stage, err)
	}
	cli := pluginv1.NewPluginServiceClient(conn)

	readyCtx, cancel := context.WithTimeout(ctx, c.readyTimeout)
	defer cancel()
	ready, err := cli.Ready(readyCtx, &pluginv1.ReadyRequest{})
	if err != nil {
		return res("ready", mapGRPCError(err))
	}
	if !ready.GetReady() {
		return res("ready", fmt.Errorf("插件未就绪: %s", ready.GetReason()))
	}
	ver, err := cli.GetVersion(readyCtx, &pluginv1.GetVersionRequest{})
	if err != nil {
		return res("version", mapGRPCError(err))
	}
	if t.ContractVer != "" && !sameMinor(ver.GetContractVersion(), t.ContractVer) {
		return res("version", fmt.Errorf("插件契约版本 %q 与底座 %q 不兼容", ver.GetContractVersion(), t.ContractVer))
	}
	fp, err := c.PeerFingerprint(ctx, t)
	if err != nil {
		return res("tls", err)
	}
	return ProbeResult{
		OK:          true,
		Stage:       "version",
		Detail:      "握手与就绪探测通过",
		Latency:     time.Since(start),
		Version:     ver.GetPluginVersion(),
		ContractVer: ver.GetContractVersion(),
		Fingerprint: fp,
	}
}

// Invoke 通过 mTLS 调用远端能力：连接与 Ready 失败会映射为契约错误类别。
func (c *Client) Invoke(ctx context.Context, t Target, capability string, input json.RawMessage,
	subject, invocationID string, timeout time.Duration, maxResultBytes int64) runtime.Result {
	start := time.Now()
	res := func(status, code, msg string) runtime.Result {
		r := runtime.Result{Status: status, Duration: time.Since(start), ExitCode: -1}
		if code != "" {
			r.Error = &runtime.HostError{Code: code, Message: msg}
		}
		return r
	}
	if timeout <= 0 {
		timeout = runtime.DefaultTimeout
	}
	if maxResultBytes <= 0 {
		maxResultBytes = runtime.DefaultMaxResultBytes
	}
	if len(input) == 0 {
		input = json.RawMessage("{}")
	}

	conn, err := c.conn(ctx, t)
	if err != nil {
		code := runtime.CodeDependencyUnavailable
		if errors.Is(err, context.DeadlineExceeded) || isTimeout(err) {
			code = runtime.CodeTimeout
		}
		return res("error", code, err.Error())
	}
	cli := pluginv1.NewPluginServiceClient(conn)

	callCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req := &pluginv1.InvokeRequest{
		ContractVersion: defaultString(t.ContractVer, "1.2"),
		Capability:      capability,
		InputJson:       input,
		Context: &pluginv1.InvocationContext{
			InvocationId: invocationID,
			DeadlineMs:   timeout.Milliseconds(),
			Subject:      subject,
		},
	}
	resp, err := cli.Invoke(callCtx, req)
	if err != nil {
		mapped := mapGRPCError(err)
		switch {
		case errors.Is(mapped, errTimeout):
			return res("error", runtime.CodeTimeout, mapped.Error())
		case errors.Is(mapped, errUnavailable):
			return res("error", runtime.CodeDependencyUnavailable, mapped.Error())
		case errors.Is(mapped, errPermission):
			return res("error", runtime.CodePermissionDenied, mapped.Error())
		case errors.Is(mapped, errInvalid):
			return res("error", runtime.CodeInputInvalid, mapped.Error())
		default:
			return res("error", runtime.CodeInternalError, mapped.Error())
		}
	}
	if resp.GetStatus() != "ok" {
		code := resp.GetError().GetCode()
		if !runtime.ValidCode(code) {
			code = runtime.CodeInternalError
		}
		return res("error", code, resp.GetError().GetMessage())
	}
	if int64(len(resp.GetOutputJson())) > maxResultBytes {
		return res("error", runtime.CodeResultTooLarge,
			fmt.Sprintf("结果体积 %d 字节超过上限 %d", len(resp.GetOutputJson()), maxResultBytes))
	}
	out := runtime.Result{Status: "ok", Duration: time.Since(start), ExitCode: 0,
		PluginVersion: resp.GetPluginVersion(), Output: append(json.RawMessage(nil), resp.GetOutputJson()...)}
	if len(out.Output) == 0 {
		out.Output = json.RawMessage("{}")
	}
	return out
}

// mapGRPCError 把 gRPC 状态码折算为契约错误类别（plugin-spec/05 §2）。
func mapGRPCError(err error) error {
	err = status.Convert(err).Err()
	switch status.Code(err) {
	case codes.DeadlineExceeded:
		return fmt.Errorf("%w: %s", errTimeout, status.Convert(err).Message())
	case codes.Unavailable, codes.Aborted:
		return fmt.Errorf("%w: %s", errUnavailable, status.Convert(err).Message())
	case codes.PermissionDenied, codes.Unauthenticated:
		return fmt.Errorf("%w: %s", errPermission, status.Convert(err).Message())
	case codes.InvalidArgument, codes.FailedPrecondition:
		return fmt.Errorf("%w: %s", errInvalid, status.Convert(err).Message())
	case codes.NotFound, codes.Unimplemented:
		return fmt.Errorf("%w: %s", errUnavailable, status.Convert(err).Message())
	default:
		return err
	}
}

var (
	errTimeout     = errors.New("timeout")
	errUnavailable = errors.New("dependency_unavailable")
	errPermission  = errors.New("permission_denied")
	errInvalid     = errors.New("input_invalid")
)

func isTimeout(err error) bool {
	var de interface{ Timeout() bool }
	return errors.As(err, &de) && de.Timeout()
}

func sameMinor(a, b string) bool {
	return strings.TrimSpace(a) == strings.TrimSpace(b)
}

func defaultString(v, fallback string) string {
	if v == "" {
		return fallback
	}
	return v
}

// NewTLSProvider 用 PKI 管理器构造 TLS 提供者（便于在 main 中装配）。
func NewTLSProvider(m *pki.Manager) TLSProvider { return pkiTLS{m: m} }

type pkiTLS struct{ m *pki.Manager }

func (p pkiTLS) ClientTLSConfig(ctx context.Context, uri string) (*tls.Config, error) {
	return p.m.ClientTLSConfig(ctx, uri)
}
