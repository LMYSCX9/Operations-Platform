package host

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"path"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"operations-platform/internal/pki"
	"operations-platform/internal/plugin/authz"
	"operations-platform/internal/plugin/instances"
	"operations-platform/internal/plugin/manifest"
	"operations-platform/internal/plugin/registry"
	"operations-platform/internal/secret"
)

// Options 是主机服务的显式依赖。
type Options struct {
	Store       *Store
	Box         *secret.Box
	Dialer      Dialer
	Distributor *Distributor
	Plugins     *registry.Registry
	Authorizer  authz.Authorizer
	Logger      *slog.Logger
	// Audit 是审计钩子；由 main 注入真实实现。
	Audit func(ctx context.Context, subj authz.Subject, action authz.Action, target authz.Target, result, reason string)

	// M6：远端 daemon 的证书、实例与按主机产物（DOC-09 §6）。
	Instances   *instances.Store
	PKI         *pki.Manager
	ConfigRoot  string
	DataRoot    string
	UnitDir     string
	ServiceUser string
}

// Service 编排主机与下发操作。
type Service struct {
	store       *Store
	box         *secret.Box
	dialer      Dialer
	distributor *Distributor
	plugins     *registry.Registry
	authorizer  authz.Authorizer
	logger      *slog.Logger
	audit       func(ctx context.Context, subj authz.Subject, action authz.Action, target authz.Target, result, reason string)

	instances   *instances.Store
	pki         *pki.Manager
	configRoot  string
	dataRoot    string
	unitDir     string
	serviceUser string
}

// NewService 校验依赖并返回服务。
func NewService(o Options) (*Service, error) {
	if o.Store == nil || o.Dialer == nil || o.Distributor == nil || o.Plugins == nil || o.Logger == nil {
		return nil, errors.New("host: Store/Dialer/Distributor/Plugins/Logger 不能为空")
	}
	if o.Authorizer == nil {
		o.Authorizer = denyAll{}
	}
	return &Service{
		store:       o.Store,
		box:         o.Box,
		dialer:      o.Dialer,
		distributor: o.Distributor,
		plugins:     o.Plugins,
		authorizer:  o.Authorizer,
		logger:      o.Logger,
		audit:       o.Audit,
		instances:   o.Instances,
		pki:         o.PKI,
		configRoot:  o.ConfigRoot,
		dataRoot:    o.DataRoot,
		unitDir:     o.UnitDir,
		serviceUser: o.ServiceUser,
	}, nil
}

type denyAll struct{}

func (denyAll) Authorize(context.Context, authz.Subject, authz.Action, authz.Target) authz.Decision {
	return authz.Decision{Allowed: false, Reason: "no matching rule"}
}

func (s *Service) recordAudit(ctx context.Context, subj authz.Subject, action authz.Action, target authz.Target, result, reason string) {
	if s.audit != nil {
		s.audit(ctx, subj, action, target, result, reason)
	}
}

func (s *Service) authorize(ctx context.Context, subj authz.Subject, action authz.Action, target authz.Target, message string) error {
	d := s.authorizer.Authorize(ctx, subj, action, target)
	if d.Allowed {
		return nil
	}
	s.recordAudit(ctx, subj, action, target, "denied", d.Reason)
	return &Error{Code: CodePermissionDenied, Message: message, Details: map[string]any{"reason": d.Reason}}
}

// keyMissing 把「主密钥未配置」翻译成显式失败，绝不降级（ADR-0013 约束 2）。
func (s *Service) keyMissing() error {
	return &Error{
		Code:    CodeCredentialKeyMissing,
		Message: "凭据主密钥未配置：请设置 credential_key（base64 的 32 字节）后重试；不支持明文降级",
	}
}

func newID(prefix string) string {
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		return fmt.Sprintf("%s-%d", prefix, time.Now().UnixNano())
	}
	return prefix + "-" + hex.EncodeToString(buf)
}

func (s *Service) encryptCredential(cred Credential) ([]byte, error) {
	if s.box == nil {
		return nil, s.keyMissing()
	}
	raw, err := json.Marshal(cred)
	if err != nil {
		return nil, fmt.Errorf("序列化凭据失败: %w", err)
	}
	ciphertext, err := s.box.Encrypt(raw)
	if errors.Is(err, secret.ErrKeyNotConfigured) {
		return nil, s.keyMissing()
	}
	if err != nil {
		return nil, err
	}
	return ciphertext, nil
}

func (s *Service) decryptCredential(ciphertext []byte) (Credential, error) {
	if s.box == nil {
		return Credential{}, s.keyMissing()
	}
	raw, err := s.box.Decrypt(ciphertext)
	if errors.Is(err, secret.ErrKeyNotConfigured) {
		return Credential{}, s.keyMissing()
	}
	if errors.Is(err, secret.ErrDecryptFailed) {
		return Credential{}, &Error{Code: CodeDependencyUnavailable, Message: "凭据无法解密：主密钥可能与写入时不一致"}
	}
	if err != nil {
		return Credential{}, err
	}
	var cred Credential
	if err := json.Unmarshal(raw, &cred); err != nil {
		return Credential{}, &Error{Code: CodeDependencyUnavailable, Message: "凭据内容损坏，无法解析"}
	}
	return cred, nil
}

// Create 新增主机并加密保存凭据。
func (s *Service) Create(ctx context.Context, subj authz.Subject, in HostInput) (Host, error) {
	target := authz.Target{Type: "host"}
	if err := s.authorize(ctx, subj, authz.ActionHostWrite, target, "无主机新增权限"); err != nil {
		return Host{}, err
	}
	if err := in.Validate(); err != nil {
		return Host{}, err
	}
	if in.Credential == nil {
		return Host{}, &Error{Code: CodeInputInvalid, Message: "新增主机必须提供凭据", Details: map[string]any{"field": "credential"}}
	}
	ciphertext, err := s.encryptCredential(*in.Credential)
	if err != nil {
		return Host{}, err
	}
	h := Host{
		ID:         newID("host"),
		Name:       strings.TrimSpace(in.Name),
		Address:    strings.TrimSpace(in.Address),
		Port:       in.Port,
		Username:   strings.TrimSpace(in.Username),
		AuthMethod: in.AuthMethod,
		CreatedBy:  subj.ID,
	}
	if err := s.store.Create(ctx, h, ciphertext); err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return Host{}, &Error{Code: CodeConflict, Message: "主机名称已存在", Details: map[string]any{"field": "name"}}
		}
		return Host{}, err
	}
	s.recordAudit(ctx, subj, authz.ActionHostWrite, authz.Target{Type: "host", ID: h.ID}, "success", "创建主机")
	return s.store.Get(ctx, h.ID)
}

// Update 编辑主机；Credential 为 nil 时保留原凭据。
func (s *Service) Update(ctx context.Context, subj authz.Subject, id string, in HostInput) (Host, error) {
	target := authz.Target{Type: "host", ID: id}
	if err := s.authorize(ctx, subj, authz.ActionHostWrite, target, "无主机编辑权限"); err != nil {
		return Host{}, err
	}
	if err := in.Validate(); err != nil {
		return Host{}, err
	}
	existing, err := s.store.Get(ctx, id)
	if err != nil {
		return Host{}, notFound(err, id)
	}
	var ciphertext []byte
	reason := "更新主机"
	if in.Credential != nil {
		ciphertext, err = s.encryptCredential(*in.Credential)
		if err != nil {
			return Host{}, err
		}
		// 审计只记录「发生了替换」，绝不记录内容（DOC-09 §9）。
		reason = "更新主机并替换凭据"
	}
	existing.Name = strings.TrimSpace(in.Name)
	existing.Address = strings.TrimSpace(in.Address)
	existing.Port = in.Port
	existing.Username = strings.TrimSpace(in.Username)
	existing.AuthMethod = in.AuthMethod
	// 认证方式变化会使用户已信任的指纹失效，需要重新确认。
	if existing.AuthMethod != in.AuthMethod {
		existing.Status = StatusUnknown
	}
	if err := s.store.Update(ctx, existing, ciphertext); err != nil {
		return Host{}, notFound(err, id)
	}
	s.recordAudit(ctx, subj, authz.ActionHostWrite, target, "success", reason)
	return s.store.Get(ctx, id)
}

// Delete 删除主机；存在下发记录时必须 force=true。
func (s *Service) Delete(ctx context.Context, subj authz.Subject, id string, force bool) error {
	target := authz.Target{Type: "host", ID: id}
	if err := s.authorize(ctx, subj, authz.ActionHostWrite, target, "无主机删除权限"); err != nil {
		return err
	}
	if _, err := s.store.Get(ctx, id); err != nil {
		return notFound(err, id)
	}
	count, err := s.store.CountDistributions(ctx, id)
	if err != nil {
		return err
	}
	if count > 0 && !force {
		return &Error{
			Code:    CodeConflict,
			Message: "该主机存在下发记录；如确认删除主机，请追加 ?force=true。下发记录保留用于追溯",
			Details: map[string]any{"distributions": count},
		}
	}
	if err := s.store.Delete(ctx, id); err != nil {
		return notFound(err, id)
	}
	s.recordAudit(ctx, subj, authz.ActionHostWrite, target, "success", "删除主机")
	return nil
}

// List 返回主机列表（不含凭据）。
func (s *Service) List(ctx context.Context, subj authz.Subject) ([]Host, error) {
	if err := s.authorize(ctx, subj, authz.ActionHostRead, authz.Target{Type: "host"}, "无主机查看权限"); err != nil {
		return nil, err
	}
	return s.store.List(ctx)
}

// Get 返回主机详情（不含凭据）。
func (s *Service) Get(ctx context.Context, subj authz.Subject, id string) (Host, error) {
	if err := s.authorize(ctx, subj, authz.ActionHostRead, authz.Target{Type: "host", ID: id}, "无主机查看权限"); err != nil {
		return Host{}, err
	}
	h, err := s.store.Get(ctx, id)
	if err != nil {
		return Host{}, notFound(err, id)
	}
	return h, nil
}

// Test 执行连通性测试：只执行 uname -s，不做任何与本测试无关的远端操作。
func (s *Service) Test(ctx context.Context, subj authz.Subject, id string) (TestResult, error) {
	target := authz.Target{Type: "host", ID: id}
	if err := s.authorize(ctx, subj, authz.ActionHostTest, target, "无主机连通性测试权限"); err != nil {
		return TestResult{}, err
	}
	h, err := s.store.Get(ctx, id)
	if err != nil {
		return TestResult{}, notFound(err, id)
	}
	cred, err := s.credentialFor(ctx, id)
	if err != nil {
		return TestResult{}, err
	}

	result := TestResult{HostID: id, TestedAt: time.Now().UTC()}
	conn, dialErr := s.dialer.Dial(ctx, h, cred)
	if dialErr != nil {
		category := classifyCategory(dialErr)
		result.Status = StatusUnreachable
		if category == CatFingerprintMismatch {
			result.Status = StatusFingerprintMismatch
		}
		result.Category = category
		result.Error = dialErr.Error()
		_ = s.store.SetTestResult(ctx, id, result.Status, result.Error, result.TestedAt)
		s.recordAudit(ctx, subj, authz.ActionHostTest, target, "failure", "测试连接失败: "+category)
		return result, &Error{Code: category, Message: "连通性测试失败: " + result.Error, Details: map[string]any{"host_id": id}}
	}
	defer conn.Close()

	if !h.HostKeyTrusted && conn.Fingerprint() != "" {
		if err := s.store.TrustFingerprint(ctx, id, conn.Fingerprint()); err != nil {
			return TestResult{}, err
		}
		s.recordAudit(ctx, subj, authz.ActionHostTest, target, "success", "首次信任并锁存主机指纹")
	}

	runCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	out, _, runErr := conn.Run(runCtx, "uname -s", nil)
	if runErr != nil {
		result.Status = StatusReachable
		result.Error = "已建立连接，但无法执行 uname -s: " + runErr.Error()
		result.Category = CodeDependencyUnavailable
		_ = s.store.SetTestResult(ctx, id, result.Status, result.Error, result.TestedAt)
		s.recordAudit(ctx, subj, authz.ActionHostTest, target, "failure", "uname 执行失败")
		return result, nil
	}
	osName := strings.TrimSpace(string(out))
	result.OS = osName
	if !strings.EqualFold(osName, "linux") {
		result.Status = StatusReachable
		result.Category = CatUnsupportedOS
		result.Error = fmt.Sprintf("目标机系统为 %q，当前仅支持 Linux 下发（OQ-024）", osName)
		_ = s.store.SetTestResult(ctx, id, result.Status, result.Error, result.TestedAt)
		s.recordAudit(ctx, subj, authz.ActionHostTest, target, "failure", "不支持的操作系统")
		return result, nil
	}

	result.Status = StatusReachable
	_ = s.store.SetTestResult(ctx, id, StatusReachable, "", result.TestedAt)
	s.recordAudit(ctx, subj, authz.ActionHostTest, target, "success", "连通性测试通过")
	return result, nil
}

func (s *Service) credentialFor(ctx context.Context, id string) (Credential, error) {
	ciphertext, err := s.store.CredentialCiphertext(ctx, id)
	if err != nil {
		return Credential{}, notFound(err, id)
	}
	return s.decryptCredential(ciphertext)
}

// Distribute 创建并发起一次插件下发；同一主机 + 插件 + 版本重复提交返回既有任务。
// 端口为 0 时使用清单 runtime.listen.port（远端 daemon 必填）。
func (s *Service) Distribute(ctx context.Context, subj authz.Subject, hostID, pluginID, version string) (Distribution, error) {
	return s.DistributeWithPort(ctx, subj, hostID, pluginID, version, 0)
}

// DistributeWithPort 允许在下发时覆盖监听端口（实例级配置落到实例）。
func (s *Service) DistributeWithPort(ctx context.Context, subj authz.Subject, hostID, pluginID, version string, port int) (Distribution, error) {
	target := authz.Target{Type: "host", ID: hostID}
	if err := s.authorize(ctx, subj, authz.ActionHostDistribute, target, "无插件下发权限"); err != nil {
		return Distribution{}, err
	}
	h, err := s.store.Get(ctx, hostID)
	if err != nil {
		return Distribution{}, notFound(err, hostID)
	}
	p, err := s.plugins.Get(ctx, pluginID)
	if errors.Is(err, registry.ErrNotFound) {
		return Distribution{}, &Error{Code: CodeNotFound, Message: fmt.Sprintf("插件 %s 未登记，无法下发", pluginID)}
	}
	if err != nil {
		return Distribution{}, err
	}
	if version == "" {
		version = p.Version
	}
	if version != p.Version {
		return Distribution{}, &Error{Code: CodeInputInvalid, Message: fmt.Sprintf("插件 %s 当前登记版本为 %s，与请求版本 %s 不一致", pluginID, p.Version, version),
			Details: map[string]any{"field": "plugin_version"}}
	}
	var m manifest.Manifest
	if err := yaml.Unmarshal([]byte(p.ManifestYAML), &m); err != nil {
		return Distribution{}, &Error{Code: CodeInputInvalid, Message: "已登记清单无法解析: " + err.Error()}
	}
	if m.Form != "daemon" {
		return Distribution{}, &Error{Code: CodeInputInvalid,
			Message: "命令式插件不支持远端下发（只能本机实例）；远端实例要求 form: daemon 与 runtime.listen.port"}
	}
	if m.Runtime == nil || m.Runtime.Listen == nil || m.Runtime.Listen.Port == 0 {
		return Distribution{}, &Error{Code: CodeInputInvalid, Message: "远端 daemon 清单必须声明 runtime.listen.port"}
	}
	if port == 0 {
		port = m.Runtime.Listen.Port
	}
	if port < 1 || port > 65535 {
		return Distribution{}, &Error{Code: CodeInputInvalid, Message: fmt.Sprintf("监听端口 %d 非法", port),
			Details: map[string]any{"field": "port"}}
	}
	if s.instances == nil || s.pki == nil {
		return Distribution{}, &Error{Code: CodeDependencyUnavailable,
			Message: "远端实例能力未装配（缺少实例存储或内置 CA），拒绝下发"}
	}
	if _, err := s.instances.GetType(ctx, p.ID); errors.Is(err, instances.ErrNotFound) {
		return Distribution{}, &Error{Code: CodeNotFound, Message: fmt.Sprintf("插件类型 %s 未登记，请先扫描目录", p.ID)}
	}

	pkg, err := ArchiveDir(p.InstallPath)
	if err != nil {
		return Distribution{}, &Error{Code: CodeArchiveFailed, Message: err.Error()}
	}
	pkg.PluginID, pkg.Version = p.ID, p.Version

	instanceID := instances.RemoteInstanceID(hostID, p.ID)
	d := Distribution{
		ID:             newID("dist"),
		HostID:         hostID,
		PluginID:       p.ID,
		PluginVersion:  p.Version,
		IdempotencyKey: idempotencyKey(p.ID, p.Version),
		CreatedBy:      subj.ID,
		State:          DistQueued,
		InstanceID:     instanceID,
		ListenPort:     port,
	}
	created, err := s.store.CreateDistribution(ctx, d)
	if err != nil {
		return Distribution{}, err
	}
	// 已存在（幂等命中）：直接返回，不重复安装。
	if created.ID != d.ID {
		return created, nil
	}

	if err := s.checkPortConflict(ctx, hostID, port, instanceID); err != nil {
		return Distribution{}, err
	}
	if err := s.instances.UpsertRemoteInstance(ctx, instances.Instance{
		InstanceID: instanceID, TypeID: p.ID, HostID: hostID,
		Custody: instances.CustodyRemote, PackageVersion: p.Version, ListenPort: port,
		State: instances.StateInstalled,
	}); err != nil {
		return Distribution{}, err
	}
	issued, err := s.pki.IssueServer(ctx, p.ID, hostID, instanceID)
	if err != nil {
		s.markDistributionFailed(ctx, created, CodePKIFailed, "签发实例证书失败: "+err.Error())
		return Distribution{}, &Error{Code: CodePKIFailed, Message: "签发实例证书失败: " + err.Error()}
	}
	created.CertSerial = issued.Serial
	created.InstanceID = instanceID
	created.ListenPort = port
	if err := s.instances.SetCert(ctx, instanceID, issued.Serial, issued.Fingerprint, issued.NotAfter); err != nil {
		s.markDistributionFailed(ctx, created, CodePKIFailed, "记录证书绑定失败: "+err.Error())
		return Distribution{}, err
	}
	if err := s.store.UpdateDistribution(ctx, created); err != nil {
		return Distribution{}, err
	}

	values, err := s.instanceConfigValues(ctx, instanceID)
	if err != nil {
		s.markDistributionFailed(ctx, created, CodePKIFailed, "读取实例配置失败: "+err.Error())
		return Distribution{}, err
	}
	dep := &Deployment{
		InstanceID:      instanceID,
		PluginID:        p.ID,
		HostID:          hostID,
		Port:            port,
		ContractVersion: s.contractVersion(ctx, p.ID),
		BaseID:          s.pki.BaseID(),
		Executable:      path.Join(s.distributor.remoteRoot, p.ID, p.Version, m.Entry.Executable),
		ServerCertPEM:   issued.CertPEM,
		ServerKeyPEM:    issued.KeyPEM,
		CACertPEM:       s.pki.CACertPEM(),
		ConfigValues:    values,
		ConfigRoot:      s.configRoot,
		DataRoot:        s.dataRoot,
		UnitDir:         s.unitDir,
		ServiceUser:     s.serviceUser,
	}

	cred, err := s.credentialFor(ctx, hostID)
	if err != nil {
		return Distribution{}, err
	}
	s.recordAudit(ctx, subj, authz.ActionHostDistribute, target, "success",
		fmt.Sprintf("创建下发任务 %s：%s@%s -> %s（端口 %d）", created.ID, p.ID, p.Version, instanceID, port))
	s.distributor.StartWithDeployment(created, h, cred, pkg, subj.ID, dep)
	return created, nil
}

// checkPortConflict 同一主机上的远端实例端口必须唯一（OQ-041 基线）。
func (s *Service) checkPortConflict(ctx context.Context, hostID string, port int, instanceID string) error {
	rows, err := s.instances.ListInstances(ctx, instances.InstanceFilter{HostID: hostID, Custody: instances.CustodyRemote})
	if err != nil {
		return err
	}
	for _, row := range rows {
		if row.InstanceID == instanceID {
			continue
		}
		if row.ListenPort == port {
			return &Error{Code: CodeInputInvalid, Message: fmt.Sprintf("端口 %d 已被实例 %s 占用", port, row.InstanceID),
				Details: map[string]any{"field": "port", "conflict_instance": row.InstanceID}}
		}
	}
	return nil
}

func (s *Service) contractVersion(ctx context.Context, typeID string) string {
	if t, err := s.instances.GetType(ctx, typeID); err == nil && t.ContractVersion != "" {
		return t.ContractVersion
	}
	return "1.2"
}

// instanceConfigValues 解密实例配置；配置文件以 0600 落到目标机（DOC-09 §6.1）。
func (s *Service) instanceConfigValues(ctx context.Context, instanceID string) (map[string]any, error) {
	entries, err := s.instances.ListConfig(ctx, instanceID)
	if err != nil {
		return nil, err
	}
	if len(entries) == 0 {
		return nil, nil
	}
	if s.box == nil {
		return nil, errors.New("配置主密钥未配置，无法下发实例配置")
	}
	values := make(map[string]any, len(entries))
	for _, e := range entries {
		plaintext, err := s.box.Decrypt(e.Ciphertext)
		if err != nil {
			return nil, fmt.Errorf("解密配置项 %s 失败: %w", e.Key, err)
		}
		var v any
		if err := json.Unmarshal(plaintext, &v); err != nil {
			return nil, fmt.Errorf("解析配置项 %s 失败: %w", e.Key, err)
		}
		values[e.Key] = v
	}
	return values, nil
}

func (s *Service) markDistributionFailed(ctx context.Context, d Distribution, category, message string) {
	now := time.Now().UTC()
	d.State = DistFailed
	d.Stage = "certificate"
	d.ErrorCategory = category
	d.ErrorMessage = message
	d.FinishedAt = &now
	if err := s.store.UpdateDistribution(ctx, d); err != nil {
		s.logger.Warn("记录下发失败状态失败", "distribution_id", d.ID, "error", err)
	}
}

func idempotencyKey(pluginID, version string) string {
	return pluginID + "@" + version
}

// ListDistributions 返回下发记录；hostID 为空表示全部。
func (s *Service) ListDistributions(ctx context.Context, subj authz.Subject, hostID string) ([]Distribution, error) {
	if err := s.authorize(ctx, subj, authz.ActionHostRead, authz.Target{Type: "host", ID: hostID}, "无下发记录查看权限"); err != nil {
		return nil, err
	}
	return s.store.ListDistributions(ctx, hostID)
}

// GetDistribution 返回单条下发记录。
func (s *Service) GetDistribution(ctx context.Context, subj authz.Subject, id string) (Distribution, error) {
	if err := s.authorize(ctx, subj, authz.ActionHostRead, authz.Target{Type: "distribution", ID: id}, "无下发记录查看权限"); err != nil {
		return Distribution{}, err
	}
	d, err := s.store.GetDistribution(ctx, id)
	if err != nil {
		return Distribution{}, notFound(err, id)
	}
	return d, nil
}

// Retry 重试失败或中断的下发任务。
func (s *Service) Retry(ctx context.Context, subj authz.Subject, id string) (Distribution, error) {
	target := authz.Target{Type: "distribution", ID: id}
	if err := s.authorize(ctx, subj, authz.ActionHostDistribute, target, "无下发重试权限"); err != nil {
		return Distribution{}, err
	}
	d, err := s.store.GetDistribution(ctx, id)
	if err != nil {
		return Distribution{}, notFound(err, id)
	}
	if d.State != DistFailed && d.State != DistInterrupted {
		return Distribution{}, &Error{Code: CodeConflict, Message: fmt.Sprintf("仅失败或中断的下发可重试，当前状态为 %s", d.State)}
	}
	h, err := s.store.Get(ctx, d.HostID)
	if err != nil {
		return Distribution{}, notFound(err, d.HostID)
	}
	p, err := s.plugins.Get(ctx, d.PluginID)
	if err != nil {
		return Distribution{}, &Error{Code: CodeDependencyUnavailable, Message: "插件已不在注册表，无法重试下发"}
	}
	if p.Version != d.PluginVersion {
		return Distribution{}, &Error{Code: CodeInputInvalid, Message: fmt.Sprintf("插件版本已变为 %s，请重新创建下发任务", p.Version)}
	}
	pkg, err := ArchiveDir(p.InstallPath)
	if err != nil {
		return Distribution{}, &Error{Code: CodeArchiveFailed, Message: err.Error()}
	}
	pkg.PluginID, pkg.Version = p.ID, p.Version
	dep, err := s.deploymentFor(ctx, p, &d)
	if err != nil {
		return Distribution{}, err
	}
	cred, err := s.credentialFor(ctx, d.HostID)
	if err != nil {
		return Distribution{}, err
	}

	d.State = DistQueued
	d.Stage = "queued"
	d.ErrorCategory = ""
	d.ErrorMessage = ""
	d.StartedAt = nil
	d.FinishedAt = nil
	d.Installed = false
	d.StartedOK = false
	d.Callable = false
	if err := s.store.UpdateDistribution(ctx, d); err != nil {
		return Distribution{}, err
	}
	if s.instances != nil {
		_ = s.instances.UpdateStatus(ctx, d.InstanceID, instances.StateInstalled, "重新下发中",
			instances.DriftNone, "", false, false, false)
	}
	s.recordAudit(ctx, subj, authz.ActionHostDistribute, target, "success", "重试下发")
	s.distributor.StartWithDeployment(d, h, cred, pkg, subj.ID, dep)
	return d, nil
}

// deploymentFor 为已存在的下发记录重建部署产物；证书优先复用未过期的绑定。
func (s *Service) deploymentFor(ctx context.Context, p registry.Plugin, d *Distribution) (*Deployment, error) {
	if s.instances == nil || s.pki == nil {
		return nil, &Error{Code: CodeDependencyUnavailable, Message: "远端实例能力未装配（缺少实例存储或内置 CA）"}
	}
	var m manifest.Manifest
	if err := yaml.Unmarshal([]byte(p.ManifestYAML), &m); err != nil {
		return nil, &Error{Code: CodeInputInvalid, Message: "已登记清单无法解析: " + err.Error()}
	}
	if m.Runtime == nil || m.Runtime.Listen == nil || m.Runtime.Listen.Port == 0 {
		return nil, &Error{Code: CodeInputInvalid, Message: "远端 daemon 清单必须声明 runtime.listen.port"}
	}
	if d.InstanceID == "" {
		d.InstanceID = instances.RemoteInstanceID(d.HostID, p.ID)
	}
	if d.ListenPort == 0 {
		d.ListenPort = m.Runtime.Listen.Port
	}
	if err := s.checkPortConflict(ctx, d.HostID, d.ListenPort, d.InstanceID); err != nil {
		return nil, err
	}
	issued, err := s.pki.IssueServer(ctx, p.ID, d.HostID, d.InstanceID)
	if err != nil {
		return nil, &Error{Code: CodePKIFailed, Message: "签发实例证书失败: " + err.Error()}
	}
	d.CertSerial = issued.Serial
	if err := s.instances.SetCert(ctx, d.InstanceID, issued.Serial, issued.Fingerprint, issued.NotAfter); err != nil {
		return nil, err
	}
	values, err := s.instanceConfigValues(ctx, d.InstanceID)
	if err != nil {
		return nil, err
	}
	return &Deployment{
		InstanceID:      d.InstanceID,
		PluginID:        p.ID,
		HostID:          d.HostID,
		Port:            d.ListenPort,
		ContractVersion: s.contractVersion(ctx, p.ID),
		BaseID:          s.pki.BaseID(),
		Executable:      path.Join(s.distributor.remoteRoot, p.ID, p.Version, m.Entry.Executable),
		ServerCertPEM:   issued.CertPEM,
		ServerKeyPEM:    issued.KeyPEM,
		CACertPEM:       s.pki.CACertPEM(),
		ConfigValues:    values,
		ConfigRoot:      s.configRoot,
		DataRoot:        s.dataRoot,
		UnitDir:         s.unitDir,
		ServiceUser:     s.serviceUser,
	}, nil
}

// UnitPresent 通过 SSH 确认目标机上的 systemd unit 是否仍在，用于远端实例漂移检测。
// 仅做只读检查，不启停进程。
func (s *Service) UnitPresent(ctx context.Context, hostID, pluginID string) (bool, error) {
	if hostID == "" || pluginID == "" {
		return false, errors.New("host: 主机标识与插件标识不能为空")
	}
	h, err := s.store.Get(ctx, hostID)
	if err != nil {
		return false, err
	}
	cred, err := s.credentialFor(ctx, hostID)
	if err != nil {
		return false, err
	}
	conn, err := s.dialer.Dial(ctx, h, cred)
	if err != nil {
		return false, err
	}
	defer conn.Close()
	dep := Deployment{PluginID: pluginID, HostID: hostID, UnitDir: s.unitDir}
	out, stderr, err := conn.Run(ctx, "test -f "+shellQuote(dep.UnitPath())+" && echo yes || echo no", nil)
	if err != nil {
		return false, fmt.Errorf("检查 systemd unit 失败: %s", sanitizeRemoteError(err, stderr))
	}
	return strings.TrimSpace(string(out)) == "yes", nil
}

// Cancel 撤销下发记录。已安装的记录会尝试删除目标机目录；删除失败则保留 installed 并报错。
func (s *Service) Cancel(ctx context.Context, subj authz.Subject, id string) (Distribution, error) {
	target := authz.Target{Type: "distribution", ID: id}
	if err := s.authorize(ctx, subj, authz.ActionHostDistribute, target, "无下发撤销权限"); err != nil {
		return Distribution{}, err
	}
	d, err := s.store.GetDistribution(ctx, id)
	if err != nil {
		return Distribution{}, notFound(err, id)
	}
	switch d.State {
	case DistQueued, DistFailed, DistInterrupted:
		// 直接置撤销，不触碰目标机。
	case DistInstalled, DistStarting, DistStarted, DistStartFailed, DistCallable:
		h, err := s.store.Get(ctx, d.HostID)
		if err != nil {
			return Distribution{}, notFound(err, d.HostID)
		}
		cred, err := s.credentialFor(ctx, d.HostID)
		if err != nil {
			return Distribution{}, err
		}
		conn, err := s.dialer.Dial(ctx, h, cred)
		if err != nil {
			return Distribution{}, &Error{Code: classifyCategory(err), Message: "撤销失败：无法连接目标主机: " + err.Error()}
		}
		defer conn.Close()
		cleanup := "rm -rf " + shellQuote(d.RemotePath)
		if d.InstanceID != "" {
			// 远端 daemon：停服、删 unit/证书/配置/包目录，保留数据目录（OQ-051 基线）。
			dep := Deployment{PluginID: d.PluginID, HostID: d.HostID,
				ConfigRoot: s.configRoot, DataRoot: s.dataRoot, UnitDir: s.unitDir}
			cleanup = fmt.Sprintf("systemctl disable --now %s >/dev/null 2>&1 || true; rm -f %s; systemctl daemon-reload >/dev/null 2>&1 || true; rm -rf %s %s",
				shellQuote(dep.UnitName()), shellQuote(dep.UnitPath()), shellQuote(dep.ConfigDir()), shellQuote(d.RemotePath))
		}
		if _, stderr, err := conn.Run(ctx, cleanup, nil); err != nil {
			return Distribution{}, &Error{Code: CodeInstallFailed, Message: "撤销失败：目标机清理失败: " + sanitizeRemoteError(err, stderr)}
		}
		if d.CertSerial != "" && s.pki != nil {
			if err := s.pki.Revoke(ctx, d.CertSerial, "distribution_cancelled"); err != nil {
				return Distribution{}, fmt.Errorf("吊销实例证书失败: %w", err)
			}
		}
		if d.InstanceID != "" && s.instances != nil {
			if err := s.instances.SetEnabled(ctx, d.InstanceID, false); err != nil {
				s.logger.Warn("撤销时停用实例失败", "instance_id", d.InstanceID, "error", err)
			}
			if err := s.instances.UpdateStatus(ctx, d.InstanceID, instances.StateRevoked, "已撤销下发，证书已吊销",
				instances.DriftNone, "revoked", false, false, false); err != nil {
				return Distribution{}, err
			}
			_ = s.instances.AppendEvent(ctx, instances.RegistrationEvent{
				OccurredAt: time.Now().UTC(), Action: "instance.revoked", TypeID: d.PluginID,
				InstanceID: d.InstanceID, HostID: d.HostID, Actor: subj.ID, Result: instances.StateRevoked,
			})
		}
	default:
		return Distribution{}, &Error{Code: CodeConflict, Message: fmt.Sprintf("当前状态 %s 不可撤销", d.State)}
	}

	d.State = DistCancelled
	d.Callable = false
	d.StartedOK = false
	d.Installed = false
	d.Stage = "cancelled"
	d.ErrorCategory = CatCancelled
	d.ErrorMessage = "已撤销"
	finish := time.Now().UTC()
	d.FinishedAt = &finish
	if err := s.store.UpdateDistribution(ctx, d); err != nil {
		return Distribution{}, err
	}
	s.recordAudit(ctx, subj, authz.ActionHostDistribute, target, "success", "撤销下发")
	return d, nil
}

// RecoverInterrupted 在启动时把中间态任务标记为中断。
func (s *Service) RecoverInterrupted(ctx context.Context) (int, error) {
	return s.store.MarkInterrupted(ctx)
}

// WaitDistributions 等待在途下发结束，供优雅关闭使用。
func (s *Service) WaitDistributions(ctx context.Context) error {
	return s.distributor.Wait(ctx)
}

// LastTestStatus 返回最近一次测试状态，供概览统计使用。
func (s *Service) CountByStatus(ctx context.Context) (total int, reachable int, err error) {
	hosts, err := s.store.List(ctx)
	if err != nil {
		return 0, 0, err
	}
	for _, h := range hosts {
		if h.Status == StatusReachable {
			reachable++
		}
	}
	return len(hosts), reachable, nil
}

// InstalledDistributionCount 返回已安装完成的下发记录总数（总览 KPI）。
func (s *Service) InstalledDistributionCount(ctx context.Context) (int, error) {
	// M6 起安装完成包含已启动/可调用，统计口径不能只看 installed。
	total := 0
	for _, state := range []string{DistInstalled, DistStarted, DistCallable} {
		n, err := s.store.CountDistributionsByState(ctx, state)
		if err != nil {
			return 0, err
		}
		total += n
	}
	return total, nil
}

func classifyCategory(err error) string {
	var ce *ConnectError
	if errors.As(err, &ce) {
		return ce.Category
	}
	var he *Error
	if errors.As(err, &he) {
		return he.Code
	}
	return CatUnreachable
}

func notFound(err error, id string) error {
	if errors.Is(err, ErrNotFound) {
		return &Error{Code: CodeNotFound, Message: fmt.Sprintf("记录 %s 不存在", id)}
	}
	return err
}
