package host

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
)

// Conn 是一次已建立的 SSH 连接；Fingerprint 返回握手时观察到的主机公钥指纹。
type Conn interface {
	Fingerprint() string
	Run(ctx context.Context, command string, stdin io.Reader) (stdout, stderr []byte, err error)
	Close() error
}

// Dialer 抽象建立连接的方式，便于在测试中注入假实现（不依赖真实 sshd）。
type Dialer interface {
	Dial(ctx context.Context, h Host, cred Credential) (Conn, error)
}

// ConnectError 携带可分类的连接失败原因（DOC-09 HOST-A-05）。
type ConnectError struct {
	Category string
	Err      error
}

func (e *ConnectError) Error() string {
	if e.Err == nil {
		return e.Category
	}
	return e.Category + ": " + e.Err.Error()
}

func (e *ConnectError) Unwrap() error { return e.Err }

// SSHDialer 使用 golang.org/x/crypto/ssh 建立连接（ADR-0017）。
type SSHDialer struct {
	ConnectTimeout time.Duration
}

// Dial 建立连接；指纹策略为 TOFU：
//   - 已锁存指纹：必须完全一致，不一致直接失败，没有忽略开关（ADR-0014）；
//   - 未锁存：接受并在握手后由调用方写入信任指纹。
func (d *SSHDialer) Dial(ctx context.Context, h Host, cred Credential) (Conn, error) {
	timeout := d.ConnectTimeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	addr := net.JoinHostPort(h.Address, strconv.Itoa(h.Port))

	var observed string
	hostKeyCallback := func(_ string, _ net.Addr, key ssh.PublicKey) error {
		fp := ssh.FingerprintSHA256(key)
		if h.HostKeyTrusted && h.HostKeyFP != "" && fp != h.HostKeyFP {
			return &ConnectError{
				Category: CatFingerprintMismatch,
				Err:      fmt.Errorf("主机指纹已变更：记录 %s，实际 %s", h.HostKeyFP, fp),
			}
		}
		observed = fp
		return nil
	}

	auth, err := authMethods(cred)
	if err != nil {
		return nil, err
	}
	config := &ssh.ClientConfig{
		User:            h.Username,
		Auth:            auth,
		HostKeyCallback: hostKeyCallback,
		Timeout:         timeout,
	}

	dialer := &net.Dialer{Timeout: timeout}
	raw, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, classifyDialError(err)
	}
	// 握手阶段同样受超时约束，避免对端只接受 TCP 却不应答。
	if err := raw.SetDeadline(time.Now().Add(timeout)); err != nil {
		raw.Close()
		return nil, classifyDialError(err)
	}
	clientConn, chans, reqs, err := ssh.NewClientConn(raw, addr, config)
	if err != nil {
		raw.Close()
		return nil, classifyDialError(err)
	}
	_ = raw.SetDeadline(time.Time{})

	return &sshConn{
		client:      ssh.NewClient(clientConn, chans, reqs),
		fingerprint: observed,
	}, nil
}

func authMethods(cred Credential) ([]ssh.AuthMethod, error) {
	switch cred.Method {
	case AuthMethodPassword:
		if cred.Password == "" {
			return nil, &Error{Code: CodeInputInvalid, Message: "账号密码认证缺少密码", Details: map[string]any{"field": "credential.password"}}
		}
		return []ssh.AuthMethod{ssh.Password(cred.Password)}, nil
	case AuthMethodKey:
		if strings.TrimSpace(cred.PrivateKey) == "" {
			return nil, &Error{Code: CodeInputInvalid, Message: "密钥认证缺少私钥", Details: map[string]any{"field": "credential.private_key"}}
		}
		var (
			signer ssh.Signer
			err    error
		)
		if cred.Passphrase != "" {
			signer, err = ssh.ParsePrivateKeyWithPassphrase([]byte(cred.PrivateKey), []byte(cred.Passphrase))
		} else {
			signer, err = ssh.ParsePrivateKey([]byte(cred.PrivateKey))
		}
		if err != nil {
			// 不回显私钥内容；只说明无法解析。
			return nil, &Error{Code: CodeInputInvalid, Message: "私钥无法解析（可能格式错误或口令短语不正确）", Details: map[string]any{"field": "credential.private_key"}}
		}
		return []ssh.AuthMethod{ssh.PublicKeys(signer)}, nil
	default:
		return nil, &Error{Code: CodeInputInvalid, Message: "未知的认证方式", Details: map[string]any{"field": "auth_method"}}
	}
}

// classifyDialError 把底层错误归类为可区分的原因，不泄露凭据。
func classifyDialError(err error) error {
	var ce *ConnectError
	if errors.As(err, &ce) {
		return ce
	}
	if errors.Is(err, context.DeadlineExceeded) || os.IsTimeout(err) {
		return &ConnectError{Category: CatTimeout, Err: err}
	}
	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "unable to authenticate"),
		strings.Contains(msg, "no supported methods"),
		strings.Contains(msg, "handshake failed"):
		return &ConnectError{Category: CatAuthFailed, Err: err}
	case strings.Contains(msg, "timeout"), strings.Contains(msg, "i/o timeout"):
		return &ConnectError{Category: CatTimeout, Err: err}
	default:
		return &ConnectError{Category: CatUnreachable, Err: err}
	}
}

type sshConn struct {
	client      *ssh.Client
	fingerprint string
}

func (c *sshConn) Fingerprint() string { return c.fingerprint }

func (c *sshConn) Close() error { return c.client.Close() }

// Run 执行一条命令；stdin 可为 nil。超时后强制关闭会话，避免挂起。
func (c *sshConn) Run(ctx context.Context, command string, stdin io.Reader) ([]byte, []byte, error) {
	session, err := c.client.NewSession()
	if err != nil {
		return nil, nil, fmt.Errorf("创建 SSH 会话失败: %w", err)
	}
	defer session.Close()

	var stdout, stderr bytes.Buffer
	session.Stdout = &stdout
	session.Stderr = &stderr
	if stdin != nil {
		session.Stdin = stdin
	}

	if err := session.Start(command); err != nil {
		return nil, stderr.Bytes(), fmt.Errorf("启动远端命令失败: %w", err)
	}
	done := make(chan error, 1)
	go func() { done <- session.Wait() }()

	select {
	case err := <-done:
		return stdout.Bytes(), stderr.Bytes(), err
	case <-ctx.Done():
		_ = session.Signal(ssh.SIGKILL)
		_ = session.Close()
		<-done
		return stdout.Bytes(), stderr.Bytes(), ctx.Err()
	}
}
