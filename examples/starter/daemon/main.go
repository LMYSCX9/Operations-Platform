// 常驻插件起步模板（远端 daemon 形态，plugin-spec/03）。
//
// 进程模型：
//   - 由目标机 systemd 托管（底座下发 unit 并 enable --now），前台运行、不 daemonize、不写 pid 文件；
//   - 监听地址、端口、mTLS 证书路径**只**来自 --ops-config 指向的按主机配置文件；
//   - 证书或配置缺失/不可读 → 启动失败退出，绝不回退明文监听（plugin-spec/03 §3.7）；
//   - 必须容忍未识别的 --ops-* 参数；不得从环境变量读取注入信息；
//   - 本机形态（--ops-socket + 凭据文件）为 ADR-0018 规划，底座实现未落地；
//     本骨架按远端形态交付，未来底座支持本机 daemon 托管时再补 UDS 分支。
//
// 复制改造：改 plugin.yaml 身份/能力/端口，实现 service.go 中的能力路由；
// gen/pluginv1/ 为入库生成物三件套 + 契约常量，升级方式见 README。
package main

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"

	pluginv1 "example.com/starter-daemon/gen/pluginv1"
)

// remoteConfig 对应底座 renderRemoteConfig 生成的按主机配置（plugin-spec/03 §3.2）。
type remoteConfig struct {
	Deployment string `yaml:"deployment"`
	PluginID   string `yaml:"plugin_id"`
	InstanceID string `yaml:"instance_id"`
	Listen     struct {
		Address string `yaml:"address"`
		Port    int    `yaml:"port"`
	} `yaml:"listen"`
	TLS struct {
		CertFile       string `yaml:"cert_file"`
		KeyFile        string `yaml:"key_file"`
		CAFile         string `yaml:"ca_file"`
		ExpectedClient string `yaml:"expected_client"`
	} `yaml:"tls"`
	DataDir string         `yaml:"data_dir"`
	Config  map[string]any `yaml:"config"`
}

const (
	pluginVersion = "0.1.0"
	uriScheme     = "opsplatform" // 底座内置 CA 的 URI SAN scheme（ADR-0022）
	clientURIPre  = uriScheme + "://base/"
	clientCNPre   = "base:"
)

func main() {
	flags := parseOpsArgs()
	cfgPath := flags["config"]
	if cfgPath == "" {
		exitStartup("--ops-config 缺失：远端形态只允许从按主机配置读取监听与证书，不得使用内置默认值")
	}

	cfg, err := loadConfig(cfgPath)
	if err != nil {
		exitStartup(err.Error())
	}
	if err := cfg.validate(); err != nil {
		exitStartup(err.Error())
	}

	tlsCfg, err := buildServerTLS(cfg)
	if err != nil {
		exitStartup(err.Error())
	}

	addr := fmt.Sprintf("%s:%d", cfg.Listen.Address, cfg.Listen.Port)
	lis, err := net.Listen("tcp", addr)
	if err != nil {
		exitStartup("监听 " + addr + " 失败: " + err.Error())
	}

	srv := grpc.NewServer(grpc.Creds(credentials.NewTLS(tlsCfg)))
	svc := newService(cfg, srv)
	pluginv1.RegisterPluginServiceServer(srv, svc)

	logf("starter-daemon 启动: instance=%s listen=%s data_dir=%s",
		cfg.InstanceID, addr, cfg.DataDir)

	go waitForTerm(func() { svc.shutdown() })

	// grpc.Serve 在 GracefulStop 后返回；systemd 依据退出码判断重启策略。
	if err := srv.Serve(lis); err != nil {
		logf("服务退出: %v", err)
		os.Exit(1)
	}
	logf("starter-daemon 已停止")
}

// parseOpsArgs 只摘取已知的 --ops-* 注入参数；未识别的参数（含非 --ops-* 参数）一律忽略，
// 不因此启动失败（plugin-spec/03 §2.2 规则，远端参数同规则适用）。
func parseOpsArgs() map[string]string {
	out := map[string]string{}
	args := os.Args[1:]
	for i := 0; i < len(args); i++ {
		a := args[i]
		if !strings.HasPrefix(a, "--ops-") {
			continue
		}
		kv := strings.TrimPrefix(a, "--ops-")
		if eq := strings.Index(kv, "="); eq >= 0 {
			out[kv[:eq]] = kv[eq+1:]
			continue
		}
		if i+1 < len(args) && !strings.HasPrefix(args[i+1], "--") {
			out[kv] = args[i+1]
			i++
		} else {
			out[kv] = ""
		}
	}
	return out
}

func loadConfig(path string) (*remoteConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("读取按主机配置失败: %w", err)
	}
	cfg := &remoteConfig{}
	if err := yamlUnmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("按主机配置 %s 不是合法 YAML: %w", path, err)
	}
	return cfg, nil
}

func (c *remoteConfig) validate() error {
	if c.Listen.Address == "" || c.Listen.Port <= 0 || c.Listen.Port > 65535 {
		return errors.New("配置缺少 listen.address / listen.port；禁止在缺失时使用内置默认地址或端口")
	}
	if c.TLS.CertFile == "" || c.TLS.KeyFile == "" || c.TLS.CAFile == "" {
		return errors.New("配置缺少 tls.cert_file / key_file / ca_file；证书缺失时不得降级为明文监听")
	}
	if c.TLS.ExpectedClient == "" {
		return errors.New("配置缺少 tls.expected_client；必须校验底座客户端证书身份")
	}
	return nil
}

// buildServerTLS 构造 mTLS 服务端配置：证书链到底座内置 CA，强制并校验客户端证书，
// 并按 expected_client（格式 base:<底座ID>）核对客户端证书 CN 与 URI SAN。
// 客户端证书身份由底座内置 CA 签发：CN = base:<baseID>，URI SAN = opsplatform://base/<baseID>。
func buildServerTLS(cfg *remoteConfig) (*tls.Config, error) {
	cert, err := tls.LoadX509KeyPair(cfg.TLS.CertFile, cfg.TLS.KeyFile)
	if err != nil {
		return nil, fmt.Errorf("加载服务端证书失败（证书缺失或不可读时必须启动失败，不得回退明文）: %w", err)
	}
	caPEM, err := os.ReadFile(cfg.TLS.CAFile)
	if err != nil {
		return nil, fmt.Errorf("读取 CA 证书失败: %w", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caPEM) {
		return nil, errors.New("CA 证书文件不是合法 PEM")
	}

	expectedCN := cfg.TLS.ExpectedClient
	if !strings.HasPrefix(expectedCN, clientCNPre) || len(expectedCN) <= len(clientCNPre) {
		return nil, fmt.Errorf("expected_client %q 不是 base:<底座ID> 形式", expectedCN)
	}
	expectedURI := clientURIPre + strings.TrimPrefix(expectedCN, clientCNPre)

	return &tls.Config{
		MinVersion:   tls.VersionTLS12,
		Certificates: []tls.Certificate{cert},
		ClientAuth:   tls.RequireAndVerifyClientCert,
		ClientCAs:    pool,
		VerifyPeerCertificate: func(rawCerts [][]byte, _ [][]*x509.Certificate) error {
			return verifyClientIdentity(rawCerts, expectedCN, expectedURI)
		},
	}, nil
}

// verifyClientIdentity 在握手链校验（ClientCAs + RequireAndVerifyClientCert）之上，
// 再核对客户端证书的 EKU、CN 与 URI SAN；任何不符都拒绝连接，不进入授权。
func verifyClientIdentity(rawCerts [][]byte, expectedCN, expectedURI string) error {
	if len(rawCerts) == 0 {
		return errors.New("客户端未提供证书")
	}
	leaf, err := x509.ParseCertificate(rawCerts[0])
	if err != nil {
		return fmt.Errorf("解析客户端证书失败: %w", err)
	}
	hasClientEKU := false
	for _, u := range leaf.ExtKeyUsage {
		if u == x509.ExtKeyUsageClientAuth {
			hasClientEKU = true
			break
		}
	}
	if !hasClientEKU {
		return errors.New("客户端证书缺少 ClientAuth EKU")
	}
	if leaf.Subject.CommonName != expectedCN {
		return fmt.Errorf("客户端证书 CN %q 与 expected_client %q 不符", leaf.Subject.CommonName, expectedCN)
	}
	foundURI := false
	for _, u := range leaf.URIs {
		if u.String() == expectedURI {
			foundURI = true
			break
		}
	}
	if !foundURI {
		return fmt.Errorf("客户端证书缺少 URI SAN %s", expectedURI)
	}
	return nil
}

func exitStartup(reason string) {
	// 启动失败快速、明确退出：systemd 按 Restart=on-failure 处理，
	// 底座探测将阶段定位在 connect/ready 并记录 start_failed。
	logf("启动失败: %s", reason)
	os.Exit(1)
}

func logf(format string, args ...any) {
	// stdout/stderr 进入 journald；只输出诊断信息，不得含凭据、私钥或敏感输入。
	fmt.Fprintf(os.Stderr, format+"\n", args...)
}
