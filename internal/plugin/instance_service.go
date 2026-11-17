// 本文件实现 M6 的实例视角：类型/实例查询、实例调用、探测与漂移、实例配置。
//
// 设计边界（DOC-17、ADR-0023）：
//   - 调用、任务、批量操作与授权都以实例为目标；插件标识只是本机实例的别名；
//   - 目录扫描只产生类型候选与本机实例候选，不自动创建远端实例、不自动启用；
//   - 状态由底座探测决定，不采信插件自报。
package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"operations-platform/internal/plugin/authz"
	"operations-platform/internal/plugin/instances"
	"operations-platform/internal/plugin/manifest"
	"operations-platform/internal/plugin/registry"
	"operations-platform/internal/plugin/remote"
	"operations-platform/internal/plugin/runtime"
)

// HostLocator 由主机模块实现，避免 plugin 包直接依赖 host 包。
type HostLocator interface {
	HostAddress(ctx context.Context, hostID string) (string, error)
}

// UnitChecker 由主机模块可选实现，用于确认目标机 systemd unit 是否仍在。
// 未实现时不判定 unit 漂移，避免把「无法检查」误报为「已丢失」。
type UnitChecker interface {
	UnitPresent(ctx context.Context, hostID, pluginID string) (bool, error)
}

// TypeView 是类型 + 能力 + 实例数的展示视图。
type TypeView struct {
	instances.Type
	Capabilities  []instances.Capability `json:"capabilities"`
	InstanceCount int                    `json:"instance_count"`
}

// ConfigState 是单个配置项的展示状态；值永不回显（DOC-17 §7）。
type ConfigState struct {
	Key         string     `json:"key"`
	Type        string     `json:"type"`
	Required    bool       `json:"required"`
	Secret      bool       `json:"secret"`
	Enum        []string   `json:"enum,omitempty"`
	Description string     `json:"description,omitempty"`
	Configured  bool       `json:"configured"`
	UpdatedAt   *time.Time `json:"updated_at,omitempty"`
	UpdatedBy   string     `json:"updated_by,omitempty"`
}

// InstanceView 是实例详情。
type InstanceView struct {
	instances.Instance
	Type         *TypeView              `json:"type,omitempty"`
	Capabilities []instances.Capability `json:"capabilities,omitempty"`
	Config       []ConfigState          `json:"config,omitempty"`
}

// syncTypes 把注册表中的清单同步为类型与本机实例候选。
func (s *Service) syncTypes(ctx context.Context) {
	if s.instances == nil {
		return
	}
	rows, err := s.registry.List(ctx)
	if err != nil {
		s.logger.Warn("同步插件类型失败：读取注册表出错", "error", err)
		return
	}
	for _, row := range rows {
		var m manifest.Manifest
		if err := yaml.Unmarshal([]byte(row.ManifestYAML), &m); err != nil || m.Identity.ID == "" {
			s.logger.Warn("同步插件类型失败：清单无法解析", "plugin_id", row.ID, "error", err)
			continue
		}
		contractVer := ""
		if rng, err := manifest.ParseRange(m.Version.Contract); err == nil {
			if v, err := manifest.ResolveContract(rng); err == nil {
				contractVer = v
			}
		}
		cfgJSON, _ := json.Marshal(m.Config)
		// 托管方式描述类型的部署能力：命令式只能本机，daemon 支持远端（DOC-17 §4）。
		custody := instances.CustodyLocal
		if m.Form == "daemon" {
			custody = instances.CustodyRemote
		}
		port := 0
		if m.Runtime != nil && m.Runtime.Listen != nil {
			port = m.Runtime.Listen.Port
		}
		t := instances.Type{
			TypeID:          m.Identity.ID,
			DisplayName:     m.Identity.DisplayName,
			Description:     m.Identity.Description,
			Form:            m.Form,
			LatestVersion:   m.Version.Plugin,
			Custody:         custody,
			ManifestYAML:    row.ManifestYAML,
			ManifestSHA256:  row.ManifestSHA256,
			ContractVersion: contractVer,
			ContractRange:   m.Version.Contract,
			RuntimePort:     port,
			ConfigSchema:    string(cfgJSON),
		}
		caps := make([]instances.Capability, 0, len(m.Capabilities))
		for _, c := range m.Capabilities {
			caps = append(caps, instances.Capability{
				TypeID:             t.TypeID,
				CapabilityID:       c.ID,
				Description:        c.Description,
				InputSchemaRef:     c.InputSchemaRef,
				OutputSchemaRef:    c.OutputSchemaRef,
				Idempotent:         c.Idempotent,
				LongRunning:        c.LongRunning,
				SupportsCancel:     c.SupportsCancel,
				Concurrency:        c.Concurrency,
				Sensitive:          c.Sensitive,
				RiskLevel:          c.RiskLevel,
				SuggestedTimeoutMS: int64(c.SuggestedTimeoutMS),
			})
		}
		if _, err := s.instances.UpsertType(ctx, t, caps); err != nil {
			s.logger.Warn("同步插件类型失败", "plugin_id", t.TypeID, "error", err)
			continue
		}
		if _, _, err := s.instances.UpsertLocalInstance(ctx, t.TypeID, t.LatestVersion); err != nil {
			s.logger.Warn("同步本机实例失败", "plugin_id", t.TypeID, "error", err)
		}
	}
}

func (s *Service) requireInstances() error {
	if s.instances == nil {
		return &Error{Code: runtime.CodeNotReady, Message: "实例存储未装配，实例接口不可用"}
	}
	return nil
}

// ListTypes 返回全部类型及能力。
func (s *Service) ListTypes(ctx context.Context, subj authz.Subject) ([]TypeView, error) {
	if d := s.authorizer.Authorize(ctx, subj, authz.ActionPluginRead, authz.Target{Type: "plugin_type"}); !d.Allowed {
		s.recordAudit(ctx, subj, authz.ActionPluginRead, authz.Target{Type: "plugin_type"}, "denied", d.Reason)
		return nil, &Error{Code: runtime.CodePermissionDenied, Message: "无插件查看权限"}
	}
	if err := s.requireInstances(); err != nil {
		return nil, err
	}
	types, err := s.instances.ListTypes(ctx)
	if err != nil {
		return nil, err
	}
	all, err := s.instances.ListInstances(ctx, instances.InstanceFilter{})
	if err != nil {
		return nil, err
	}
	counts := make(map[string]int, len(types))
	for _, inst := range all {
		counts[inst.TypeID]++
	}
	out := make([]TypeView, 0, len(types))
	for _, t := range types {
		caps, err := s.instances.ListCapabilities(ctx, t.TypeID)
		if err != nil {
			return nil, err
		}
		if caps == nil {
			caps = []instances.Capability{}
		}
		out = append(out, TypeView{Type: t, Capabilities: caps, InstanceCount: counts[t.TypeID]})
	}
	return out, nil
}

// GetType 返回单个类型详情。
func (s *Service) GetType(ctx context.Context, subj authz.Subject, typeID string) (TypeView, error) {
	if d := s.authorizer.Authorize(ctx, subj, authz.ActionPluginRead, authz.Target{Type: "plugin_type", ID: typeID}); !d.Allowed {
		s.recordAudit(ctx, subj, authz.ActionPluginRead, authz.Target{Type: "plugin_type", ID: typeID}, "denied", d.Reason)
		return TypeView{}, &Error{Code: runtime.CodePermissionDenied, Message: "无插件查看权限"}
	}
	if err := s.requireInstances(); err != nil {
		return TypeView{}, err
	}
	t, err := s.instances.GetType(ctx, typeID)
	if errors.Is(err, instances.ErrNotFound) {
		return TypeView{}, &Error{Code: "not_found", Message: fmt.Sprintf("插件类型 %s 未登记", typeID)}
	}
	if err != nil {
		return TypeView{}, err
	}
	caps, err := s.instances.ListCapabilities(ctx, t.TypeID)
	if err != nil {
		return TypeView{}, err
	}
	if caps == nil {
		caps = []instances.Capability{}
	}
	all, err := s.instances.ListInstances(ctx, instances.InstanceFilter{TypeID: typeID})
	if err != nil {
		return TypeView{}, err
	}
	return TypeView{Type: t, Capabilities: caps, InstanceCount: len(all)}, nil
}

// ListInstances 返回实例列表。
func (s *Service) ListInstances(ctx context.Context, subj authz.Subject, f instances.InstanceFilter) ([]InstanceView, error) {
	if d := s.authorizer.Authorize(ctx, subj, authz.ActionPluginRead, authz.Target{Type: "plugin_instance"}); !d.Allowed {
		s.recordAudit(ctx, subj, authz.ActionPluginRead, authz.Target{Type: "plugin_instance"}, "denied", d.Reason)
		return nil, &Error{Code: runtime.CodePermissionDenied, Message: "无插件查看权限"}
	}
	if err := s.requireInstances(); err != nil {
		return nil, err
	}
	rows, err := s.instances.ListInstances(ctx, f)
	if err != nil {
		return nil, err
	}
	out := make([]InstanceView, 0, len(rows))
	for _, inst := range rows {
		out = append(out, InstanceView{Instance: inst})
	}
	return out, nil
}

// GetInstance 返回实例详情（含类型、能力与配置状态）。
func (s *Service) GetInstance(ctx context.Context, subj authz.Subject, instanceID string) (InstanceView, error) {
	instanceID = normalizeInstanceID(instanceID)
	if d := s.authorizer.Authorize(ctx, subj, authz.ActionPluginRead, authz.Target{Type: "plugin_instance", ID: instanceID}); !d.Allowed {
		s.recordAudit(ctx, subj, authz.ActionPluginRead, authz.Target{Type: "plugin_instance", ID: instanceID}, "denied", d.Reason)
		return InstanceView{}, &Error{Code: runtime.CodePermissionDenied, Message: "无插件查看权限"}
	}
	if err := s.requireInstances(); err != nil {
		return InstanceView{}, err
	}
	inst, err := s.instances.GetInstance(ctx, instanceID)
	if errors.Is(err, instances.ErrNotFound) {
		return InstanceView{}, &Error{Code: "not_found", Message: fmt.Sprintf("实例 %s 未登记", instanceID)}
	}
	if err != nil {
		return InstanceView{}, err
	}
	view := InstanceView{Instance: inst}
	if t, err := s.instances.GetType(ctx, inst.TypeID); err == nil {
		caps, err := s.instances.ListCapabilities(ctx, inst.TypeID)
		if err != nil {
			return InstanceView{}, err
		}
		if caps == nil {
			caps = []instances.Capability{}
		}
		tv := TypeView{Type: t, Capabilities: caps}
		out, err := s.instances.ListInstances(ctx, instances.InstanceFilter{TypeID: inst.TypeID})
		if err != nil {
			return InstanceView{}, err
		}
		tv.InstanceCount = len(out)
		view.Type = &tv
		view.Capabilities = caps
	}
	if cfg, err := s.ListInstanceConfig(ctx, subj, instanceID); err == nil {
		view.Config = cfg
	}
	return view, nil
}

// SetInstanceEnabled 启用/停用实例；启用前必须已安装且未漂移。
func (s *Service) SetInstanceEnabled(ctx context.Context, subj authz.Subject, instanceID string, enabled bool) (InstanceView, error) {
	instanceID = normalizeInstanceID(instanceID)
	action := authz.ActionPluginDisable
	label := "停用"
	if enabled {
		action = authz.ActionPluginEnable
		label = "启用"
	}
	target := authz.Target{Type: "plugin_instance", ID: instanceID}
	if d := s.authorizer.Authorize(ctx, subj, action, target); !d.Allowed {
		s.recordAudit(ctx, subj, action, target, "denied", d.Reason)
		return InstanceView{}, &Error{Code: runtime.CodePermissionDenied, Message: "无插件" + label + "权限", Details: map[string]any{"reason": d.Reason}}
	}
	if err := s.requireInstances(); err != nil {
		return InstanceView{}, err
	}
	inst, err := s.instances.GetInstance(ctx, instanceID)
	if errors.Is(err, instances.ErrNotFound) {
		return InstanceView{}, &Error{Code: "not_found", Message: fmt.Sprintf("实例 %s 未登记", instanceID)}
	}
	if err != nil {
		return InstanceView{}, err
	}
	if enabled {
		if !inst.Installed {
			return InstanceView{}, &Error{Code: runtime.CodeNotReady, Message: "实例尚未安装，不能启用"}
		}
		if inst.Drift != instances.DriftNone {
			return InstanceView{}, &Error{Code: runtime.CodeNotReady, Message: "实例存在漂移，先修复后再启用", Details: map[string]any{"drift": inst.Drift}}
		}
	}
	if err := s.instances.SetEnabled(ctx, instanceID, enabled); err != nil {
		return InstanceView{}, err
	}
	// 本机实例与注册表状态保持一致，保证 /api/plugins 别名不出现第二套语义。
	if inst.Custody == instances.CustodyLocal {
		state := registry.StateDisabled
		if enabled {
			state = registry.StateEnabled
		}
		if err := s.registry.SetState(ctx, inst.TypeID, state); err != nil && !errors.Is(err, registry.ErrNotFound) {
			return InstanceView{}, err
		}
	}
	res := "success"
	if err := s.instances.AppendEvent(ctx, instances.RegistrationEvent{
		OccurredAt: time.Now().UTC(), Action: "instance." + string(action), TypeID: inst.TypeID,
		InstanceID: instanceID, HostID: inst.HostID, Actor: subj.ID, Result: res,
	}); err != nil {
		s.logger.Warn("写入注册事件失败", "instance_id", instanceID, "error", err)
	}
	s.recordAudit(ctx, subj, action, target, res, "")
	return s.GetInstance(ctx, subj, instanceID)
}

// ProbeInstance 手动触发一次探测。
func (s *Service) ProbeInstance(ctx context.Context, subj authz.Subject, instanceID string) (InstanceView, error) {
	instanceID = normalizeInstanceID(instanceID)
	target := authz.Target{Type: "plugin_instance", ID: instanceID}
	if d := s.authorizer.Authorize(ctx, subj, authz.ActionPluginInstanceProbe, target); !d.Allowed {
		s.recordAudit(ctx, subj, authz.ActionPluginInstanceProbe, target, "denied", d.Reason)
		return InstanceView{}, &Error{Code: runtime.CodePermissionDenied, Message: "无实例探测权限", Details: map[string]any{"reason": d.Reason}}
	}
	inst, err := s.probe(ctx, instanceID)
	if err != nil {
		return InstanceView{}, err
	}
	s.recordAudit(ctx, subj, authz.ActionPluginInstanceProbe, target, "success", "")
	return s.GetInstance(ctx, subj, inst.InstanceID)
}

// ProbeSystem 是系统调度与下发使用的探测入口，不经过用户授权。
func (s *Service) ProbeSystem(ctx context.Context, instanceID string) (bool, string) {
	inst, err := s.probe(ctx, normalizeInstanceID(instanceID))
	if err != nil {
		return false, err.Error()
	}
	return inst.Callable, inst.StateReason
}

// ListProbes 返回实例探测历史。
func (s *Service) ListProbes(ctx context.Context, subj authz.Subject, instanceID string, limit int) ([]instances.Probe, error) {
	instanceID = normalizeInstanceID(instanceID)
	if d := s.authorizer.Authorize(ctx, subj, authz.ActionPluginRead, authz.Target{Type: "plugin_instance", ID: instanceID}); !d.Allowed {
		s.recordAudit(ctx, subj, authz.ActionPluginRead, authz.Target{Type: "plugin_instance", ID: instanceID}, "denied", d.Reason)
		return nil, &Error{Code: runtime.CodePermissionDenied, Message: "无插件查看权限"}
	}
	if err := s.requireInstances(); err != nil {
		return nil, err
	}
	return s.instances.ListProbes(ctx, instanceID, limit)
}

// ListEvents 返回注册/状态事件。
func (s *Service) ListEvents(ctx context.Context, subj authz.Subject, limit int) ([]instances.RegistrationEvent, error) {
	if d := s.authorizer.Authorize(ctx, subj, authz.ActionPluginEventRead, authz.Target{Type: "plugin_event"}); !d.Allowed {
		s.recordAudit(ctx, subj, authz.ActionPluginEventRead, authz.Target{Type: "plugin_event"}, "denied", d.Reason)
		return nil, &Error{Code: runtime.CodePermissionDenied, Message: "无注册事件查看权限"}
	}
	if err := s.requireInstances(); err != nil {
		return nil, err
	}
	return s.instances.ListEvents(ctx, limit)
}

// probe 执行一次探测并落库；本地与远端走不同阶段。
func (s *Service) probe(ctx context.Context, instanceID string) (instances.Instance, error) {
	if err := s.requireInstances(); err != nil {
		return instances.Instance{}, err
	}
	inst, err := s.instances.GetInstance(ctx, instanceID)
	if errors.Is(err, instances.ErrNotFound) {
		return instances.Instance{}, &Error{Code: "not_found", Message: fmt.Sprintf("实例 %s 未登记", instanceID)}
	}
	if err != nil {
		return instances.Instance{}, err
	}
	started := time.Now()

	var (
		ok        bool
		stage     string
		detail    string
		drift     = instances.DriftNone
		state     = inst.State
		prevState = inst.State
		installed = inst.Installed
		isStarted = inst.Started
		callable  = false
		lastErr   string
	)
	if inst.Custody == instances.CustodyLocal {
		ok, stage, detail, drift, installed = s.probeLocal(ctx, inst)
		if !ok {
			state = instances.StateDrifted
			if drift == instances.DriftNone {
				state = instances.StateUnknown
			}
		} else {
			// 技术可用性（callable）与业务开关（enabled）是两层，不得互相推断。
			isStarted, callable = true, true
			if !inst.Enabled {
				state = instances.StateDisabled
			} else {
				state = instances.StateCallable
			}
		}
		if !ok {
			lastErr = detail
		}
	} else {
		ok, stage, detail, drift, isStarted = s.probeRemote(ctx, inst)
		if ok {
			isStarted, callable = true, true
			if inst.Enabled {
				state = instances.StateCallable
			} else {
				state = instances.StateDisabled
			}
		} else {
			lastErr = detail
			callable = false
			switch stage {
			case "connect":
				state = instances.StateUnknown
			default:
				state = instances.StateStartFailed
			}
		}
	}

	probeStatus := instances.ProbeOK
	if !ok {
		probeStatus = instances.ProbeFailed
	}
	if err := s.instances.RecordProbe(ctx, instances.Probe{
		InstanceID: inst.InstanceID, ProbeAt: started, Stage: stage,
		OK: ok, LatencyMS: time.Since(started).Milliseconds(), Detail: truncate(detail, 512),
	}); err != nil {
		s.logger.Warn("写入探测记录失败", "instance_id", instanceID, "error", err)
	}
	if err := s.instances.UpdateStatus(ctx, inst.InstanceID, state, truncate(detail, 512), drift, lastErr, installed, isStarted, callable); err != nil {
		return instances.Instance{}, err
	}
	if state != prevState {
		if err := s.instances.AppendEvent(ctx, instances.RegistrationEvent{
			OccurredAt: time.Now().UTC(), Action: "instance." + probeStatus, TypeID: inst.TypeID,
			InstanceID: inst.InstanceID, HostID: inst.HostID, Actor: "system", Result: state,
			Reason: truncate(detail, 512),
		}); err != nil {
			s.logger.Warn("写入注册事件失败", "instance_id", instanceID, "error", err)
		}
	}
	return s.instances.GetInstance(ctx, inst.InstanceID)
}

// probeLocal 只校验底座掌握的安装事实：包目录、入口文件与版本。
func (s *Service) probeLocal(ctx context.Context, inst instances.Instance) (ok bool, stage, detail, drift string, installed bool) {
	row, err := s.registry.Get(ctx, inst.TypeID)
	if errors.Is(err, registry.ErrNotFound) {
		return false, "package", fmt.Sprintf("插件 %s 未登记", inst.TypeID), instances.DriftPackageMissing, false
	}
	if err != nil {
		return false, "package", err.Error(), instances.DriftNone, inst.Installed
	}
	if _, err := os.Stat(row.InstallPath); err != nil {
		return false, "package", "安装目录不可用: " + err.Error(), instances.DriftPackageMissing, false
	}
	var m manifest.Manifest
	if err := yaml.Unmarshal([]byte(row.ManifestYAML), &m); err != nil {
		return false, "manifest", "已登记清单无法解析: " + err.Error(), instances.DriftNone, inst.Installed
	}
	if m.Entry.Executable != "" {
		if _, err := os.Stat(resolveLocalEntry(row.InstallPath, m.Entry.Executable)); err != nil {
			return false, "package", "入口文件缺失: " + m.Entry.Executable, instances.DriftPackageMissing, false
		}
	}
	if inst.PackageVersion != "" && row.Version != "" && inst.PackageVersion != row.Version {
		// 目录扫描已把包版本写回实例；仍不一致说明注册事实与实机出现分叉。
		return false, "version", fmt.Sprintf("实例版本 %s 与登记版本 %s 不一致", inst.PackageVersion, row.Version),
			instances.DriftVersionMismatch, inst.Installed
	}
	return true, "version", "本机安装事实校验通过", instances.DriftNone, true
}

// resolveLocalEntry 解析本机实例清单入口的真实路径。
// Windows 基线（OQ-039）：清单按契约写不带后缀的可执行文件名，实机文件是同名 .exe；
// 回退规则与 manifest/validate.go、runtime/stdio.go 保持一致，避免把正常安装误报为包缺失。
func resolveLocalEntry(installPath, entry string) string {
	full := filepath.Join(installPath, entry)
	if _, err := os.Stat(full); err == nil {
		return full
	}
	if goruntime.GOOS == "windows" {
		if alt, altErr := os.Stat(full + ".exe"); altErr == nil && !alt.IsDir() {
			return full + ".exe"
		}
	}
	return full
}

// probeRemote 依次做连接、TLS、Ready、GetVersion；失败阶段可定位。
func (s *Service) probeRemote(ctx context.Context, inst instances.Instance) (ok bool, stage, detail, drift string, started bool) {
	if s.remote == nil || s.hosts == nil {
		return false, "connect", "远端调用组件未装配", instances.DriftNone, inst.Started
	}
	t, err := s.remoteTarget(ctx, inst)
	if err != nil {
		return false, "connect", err.Error(), instances.DriftNone, inst.Started
	}
	res := s.remote.Probe(ctx, t)
	if !res.OK {
		// 网络不可达与「unit 被删」表现相同；仅当能通过 SSH 确认 unit 缺失时才判漂移。
		if checker, ok := s.hosts.(UnitChecker); ok {
			if present, err := checker.UnitPresent(ctx, inst.HostID, inst.TypeID); err == nil && !present {
				return false, "unit", "目标机 systemd unit 缺失", instances.DriftUnitMissing, false
			}
		}
		return false, res.Stage, res.Detail, instances.DriftNone, inst.Started
	}
	drift, stage, detail = detectRemoteDrift(inst, res)
	if drift != instances.DriftNone {
		return false, stage, detail, drift, true
	}
	return true, res.Stage, res.Detail, instances.DriftNone, true
}

// detectRemoteDrift 只做「探测成功后的静态比对」，便于单测覆盖证书与版本漂移。
func detectRemoteDrift(inst instances.Instance, res remote.ProbeResult) (drift, stage, detail string) {
	if inst.CertFingerprint != "" && res.Fingerprint != "" &&
		!strings.EqualFold(inst.CertFingerprint, res.Fingerprint) {
		return instances.DriftCertMismatch, "tls", "远端出示的证书指纹与下发绑定不一致"
	}
	if inst.PackageVersion != "" && res.Version != "" && inst.PackageVersion != res.Version {
		return instances.DriftVersionMismatch, "version",
			fmt.Sprintf("远端插件版本 %s 与登记版本 %s 不一致", res.Version, inst.PackageVersion)
	}
	return instances.DriftNone, res.Stage, res.Detail
}

// remoteTarget 组装远端拨号目标：地址来自主机清单，端口来自实例（缺省回落类型声明）。
func (s *Service) remoteTarget(ctx context.Context, inst instances.Instance) (remote.Target, error) {
	if inst.HostID == "" {
		return remote.Target{}, errors.New("远端实例缺少主机标识")
	}
	addr, err := s.hosts.HostAddress(ctx, inst.HostID)
	if err != nil {
		return remote.Target{}, fmt.Errorf("主机 %s 地址不可用: %w", inst.HostID, err)
	}
	port := inst.ListenPort
	if port == 0 {
		if t, err := s.instances.GetType(ctx, inst.TypeID); err == nil {
			port = t.RuntimePort
		}
	}
	if port <= 0 || port > 65535 {
		return remote.Target{}, fmt.Errorf("实例 %s 没有生效端口（清单未声明 runtime.listen.port 且下发未覆盖）", inst.InstanceID)
	}
	contractVer := ""
	if t, err := s.instances.GetType(ctx, inst.TypeID); err == nil {
		contractVer = t.ContractVersion
	}
	return remote.Target{
		HostID: inst.HostID, PluginID: inst.TypeID, InstanceID: inst.InstanceID,
		Address: addr, Port: port, ContractVer: contractVer,
	}, nil
}

// Sweep 处理状态过期与漂移：远端超过 TTL 未探测降为 unknown；本机重新核对安装事实。
func (s *Service) Sweep(ctx context.Context) {
	if s.instances == nil {
		return
	}
	rows, err := s.instances.ListInstances(ctx, instances.InstanceFilter{})
	if err != nil {
		s.logger.Warn("状态巡检失败：读取实例出错", "error", err)
		return
	}
	now := time.Now().UTC()
	for _, inst := range rows {
		if inst.Custody == instances.CustodyLocal {
			// 本机实例的漂移只能靠重新核对包目录与版本发现；探测本身是本地 stat，成本可忽略。
			if !inst.LastProbeAt.IsZero() && now.Sub(inst.LastProbeAt) <= s.probeTTL {
				continue
			}
			if _, err := s.probe(ctx, inst.InstanceID); err != nil {
				s.logger.Warn("本机实例巡检失败", "instance_id", inst.InstanceID, "error", err)
			}
			continue
		}
		if !inst.Callable && !inst.Started {
			continue
		}
		if inst.LastProbeAt.IsZero() || now.Sub(inst.LastProbeAt) > s.probeTTL {
			if err := s.instances.UpdateStatus(ctx, inst.InstanceID, instances.StateUnknown,
				"探测结果已过期，需重新探测", inst.Drift, inst.LastError, inst.Installed, inst.Started, false); err != nil {
				s.logger.Warn("状态过期处理失败", "instance_id", inst.InstanceID, "error", err)
				continue
			}
			_ = s.instances.AppendEvent(ctx, instances.RegistrationEvent{
				OccurredAt: now, Action: "instance.probe_expired", TypeID: inst.TypeID,
				InstanceID: inst.InstanceID, HostID: inst.HostID, Actor: "system", Result: instances.StateUnknown,
			})
		}
	}
}

// ProbeEnabled 探测全部启用且已安装的实例；供周期调度调用。
func (s *Service) ProbeEnabled(ctx context.Context) {
	if s.instances == nil {
		return
	}
	rows, err := s.instances.ListInstances(ctx, instances.InstanceFilter{})
	if err != nil {
		s.logger.Warn("周期探测失败：读取实例出错", "error", err)
		return
	}
	for _, inst := range rows {
		if !inst.Enabled || !inst.Installed {
			continue
		}
		if _, err := s.probe(ctx, inst.InstanceID); err != nil {
			s.logger.Warn("周期探测失败", "instance_id", inst.InstanceID, "error", err)
		}
	}
}

// CapabilityInstances 按能力反查提供该能力的实例。
func (s *Service) CapabilityInstances(ctx context.Context, subj authz.Subject, capabilityID string) ([]InstanceView, error) {
	if d := s.authorizer.Authorize(ctx, subj, authz.ActionPluginRead, authz.Target{Type: "capability", ID: capabilityID}); !d.Allowed {
		s.recordAudit(ctx, subj, authz.ActionPluginRead, authz.Target{Type: "capability", ID: capabilityID}, "denied", d.Reason)
		return nil, &Error{Code: runtime.CodePermissionDenied, Message: "无插件查看权限"}
	}
	if err := s.requireInstances(); err != nil {
		return nil, err
	}
	types, err := s.instances.ListTypes(ctx)
	if err != nil {
		return nil, err
	}
	var out []InstanceView
	for _, t := range types {
		caps, err := s.instances.ListCapabilities(ctx, t.TypeID)
		if err != nil {
			return nil, err
		}
		found := false
		for _, c := range caps {
			if c.CapabilityID == capabilityID {
				found = true
				break
			}
		}
		if !found {
			continue
		}
		rows, err := s.instances.ListInstances(ctx, instances.InstanceFilter{TypeID: t.TypeID})
		if err != nil {
			return nil, err
		}
		for _, inst := range rows {
			out = append(out, InstanceView{Instance: inst})
		}
	}
	if out == nil {
		out = []InstanceView{}
	}
	return out, nil
}

// ListInstanceConfig 返回配置项状态（含未设置的声明项），永不返回值。
func (s *Service) ListInstanceConfig(ctx context.Context, subj authz.Subject, instanceID string) ([]ConfigState, error) {
	instanceID = normalizeInstanceID(instanceID)
	if d := s.authorizer.Authorize(ctx, subj, authz.ActionPluginRead, authz.Target{Type: "plugin_instance", ID: instanceID}); !d.Allowed {
		return nil, &Error{Code: runtime.CodePermissionDenied, Message: "无插件查看权限"}
	}
	if err := s.requireInstances(); err != nil {
		return nil, err
	}
	inst, err := s.instances.GetInstance(ctx, instanceID)
	if errors.Is(err, instances.ErrNotFound) {
		return nil, &Error{Code: "not_found", Message: fmt.Sprintf("实例 %s 未登记", instanceID)}
	}
	if err != nil {
		return nil, err
	}
	schema, err := s.configSchema(ctx, inst.TypeID)
	if err != nil {
		return nil, err
	}
	entries, err := s.instances.ListConfig(ctx, instanceID)
	if err != nil {
		return nil, err
	}
	byKey := make(map[string]instances.ConfigEntry, len(entries))
	for _, e := range entries {
		byKey[e.Key] = e
	}
	out := make([]ConfigState, 0, len(schema))
	for _, f := range schema {
		st := ConfigState{Key: f.Key, Type: f.Type, Required: f.Required, Secret: f.Secret,
			Enum: f.Enum, Description: f.Description}
		if e, ok := byKey[f.Key]; ok {
			st.Configured = true
			at := e.UpdatedAt
			st.UpdatedAt = &at
			st.UpdatedBy = e.UpdatedBy
		}
		out = append(out, st)
	}
	return out, nil
}

// SetInstanceConfig 写入实例配置；返回值永不回显。
func (s *Service) SetInstanceConfig(ctx context.Context, subj authz.Subject, instanceID string, values map[string]any) ([]ConfigState, error) {
	instanceID = normalizeInstanceID(instanceID)
	target := authz.Target{Type: "plugin_instance", ID: instanceID}
	if d := s.authorizer.Authorize(ctx, subj, authz.ActionPluginInstanceConfig, target); !d.Allowed {
		s.recordAudit(ctx, subj, authz.ActionPluginInstanceConfig, target, "denied", d.Reason)
		return nil, &Error{Code: runtime.CodePermissionDenied, Message: "无实例配置权限", Details: map[string]any{"reason": d.Reason}}
	}
	if err := s.requireInstances(); err != nil {
		return nil, err
	}
	if s.box == nil {
		return nil, &Error{Code: runtime.CodeInternalError, Message: "配置加密组件未装配"}
	}
	inst, err := s.instances.GetInstance(ctx, instanceID)
	if errors.Is(err, instances.ErrNotFound) {
		return nil, &Error{Code: "not_found", Message: fmt.Sprintf("实例 %s 未登记", instanceID)}
	}
	if err != nil {
		return nil, err
	}
	if inst.Custody == instances.CustodyLocal && inst.TypeID != "" {
		// 命令式插件的配置下发通道在契约中尚未定义（OQ-051），显式拒绝而不是静默丢弃。
		if t, err := s.instances.GetType(ctx, inst.TypeID); err == nil && t.Form == "command" {
			return nil, &Error{Code: runtime.CodeNotReady, Message: "命令式插件暂不支持实例配置（待 OQ-051 决定下发方式）"}
		}
	}
	schema, err := s.configSchema(ctx, inst.TypeID)
	if err != nil {
		return nil, err
	}
	if len(schema) == 0 && len(values) > 0 {
		return nil, &Error{Code: runtime.CodeInputInvalid, Message: fmt.Sprintf("插件类型 %s 未声明 config[]", inst.TypeID)}
	}
	declared := make(map[string]manifest.ConfigField, len(schema))
	for _, f := range schema {
		declared[f.Key] = f
	}
	existing, err := s.instances.ListConfig(ctx, instanceID)
	if err != nil {
		return nil, err
	}
	present := make(map[string]bool, len(existing))
	for _, e := range existing {
		present[e.Key] = true
	}
	var changed []string
	for key, raw := range values {
		f, ok := declared[key]
		if !ok {
			// 未声明的键忽略（plugin-spec/11 §3），不得静默落到数据库中。
			continue
		}
		if raw == nil {
			if err := s.instances.DeleteConfig(ctx, instanceID, key); err != nil {
				return nil, err
			}
			delete(present, key)
			changed = append(changed, key)
			continue
		}
		if err := validateConfigValue(f, raw); err != nil {
			return nil, &Error{Code: runtime.CodeInputInvalid, Message: fmt.Sprintf("配置项 %s 非法: %v", key, err)}
		}
		payload, err := json.Marshal(raw)
		if err != nil {
			return nil, &Error{Code: runtime.CodeInputInvalid, Message: fmt.Sprintf("配置项 %s 无法序列化", key)}
		}
		ciphertext, err := s.box.Encrypt(payload)
		if err != nil {
			return nil, fmt.Errorf("加密配置项 %s 失败: %w", key, err)
		}
		if err := s.instances.SetConfig(ctx, instanceID, key, ciphertext, subj.ID); err != nil {
			return nil, err
		}
		present[key] = true
		changed = append(changed, key)
	}
	for _, f := range schema {
		if f.Required && !present[f.Key] {
			return nil, &Error{Code: runtime.CodeInputInvalid, Message: fmt.Sprintf("缺少必填配置项 %s", f.Key)}
		}
	}
	if err := s.instances.AppendEvent(ctx, instances.RegistrationEvent{
		OccurredAt: time.Now().UTC(), Action: "instance.config_updated", TypeID: inst.TypeID,
		InstanceID: instanceID, HostID: inst.HostID, Actor: subj.ID, Result: "success",
		Details: strings.Join(changed, ","),
	}); err != nil {
		s.logger.Warn("写入注册事件失败", "instance_id", instanceID, "error", err)
	}
	s.recordAudit(ctx, subj, authz.ActionPluginInstanceConfig, target, "success", "")
	return s.ListInstanceConfig(ctx, subj, instanceID)
}

func (s *Service) configSchema(ctx context.Context, typeID string) ([]manifest.ConfigField, error) {
	t, err := s.instances.GetType(ctx, typeID)
	if errors.Is(err, instances.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(t.ConfigSchema) == "" || t.ConfigSchema == "null" {
		return nil, nil
	}
	var fields []manifest.ConfigField
	if err := json.Unmarshal([]byte(t.ConfigSchema), &fields); err != nil {
		return nil, fmt.Errorf("解析类型 %s 的 config[] 失败: %w", typeID, err)
	}
	return fields, nil
}

func validateConfigValue(f manifest.ConfigField, v any) error {
	switch f.Type {
	case "string":
		if _, ok := v.(string); !ok {
			return errors.New("应为字符串")
		}
	case "number":
		switch v.(type) {
		case float64, float32, int, int64, json.Number:
		default:
			return errors.New("应为数字")
		}
	case "integer":
		switch n := v.(type) {
		case float64:
			if n != float64(int64(n)) {
				return errors.New("应为整数")
			}
		case int, int64, json.Number:
		default:
			return errors.New("应为整数")
		}
	case "boolean":
		if _, ok := v.(bool); !ok {
			return errors.New("应为布尔值")
		}
	case "enum":
		sv, ok := v.(string)
		if !ok {
			return errors.New("应为字符串")
		}
		valid := false
		for _, e := range f.Enum {
			if e == sv {
				valid = true
				break
			}
		}
		if !valid {
			return fmt.Errorf("取值必须属于 %v", f.Enum)
		}
	default:
		return fmt.Errorf("未知配置类型 %q", f.Type)
	}
	return nil
}

// InvokeInstance 执行一次实例调用（HTTP 层入口）。
func (s *Service) InvokeInstance(ctx context.Context, subj authz.Subject, instanceID, capabilityID string,
	input json.RawMessage, source string) (InvokeResult, error) {
	return s.InvokeInstanceWithTask(ctx, subj, instanceID, capabilityID, input, source, "")
}

// InvokeInstanceWithTask 携带定时任务标识。
func (s *Service) InvokeInstanceWithTask(ctx context.Context, subj authz.Subject, instanceID, capabilityID string,
	input json.RawMessage, source, taskID string) (InvokeResult, error) {
	if err := s.requireInstances(); err != nil {
		return InvokeResult{}, err
	}
	return s.invokeInstanceWithTask(ctx, subj, instanceID, capabilityID, input, source, taskID)
}

// invokeInstanceWithTask 以实例为目标执行调用，无论成败都写执行记录。
func (s *Service) invokeInstanceWithTask(ctx context.Context, subj authz.Subject, instanceID, capabilityID string,
	input json.RawMessage, source, taskID string) (InvokeResult, error) {
	started := time.Now().UTC()
	invocationID := newInvocationID()
	instanceID = normalizeInstanceID(instanceID)
	target := authz.Target{Type: "plugin_instance", ID: instanceID}
	result := InvokeResult{InvocationID: invocationID, InstanceID: instanceID, CapabilityID: capabilityID}
	if source == "" {
		source = registry.SourceAPI
	}

	finish := func(pluginID, status, category string) {
		result.PluginID = pluginID
		result.Status = status
		result.DurationMS = time.Since(started).Milliseconds()
		s.enqueueExecution(registry.Execution{
			InvocationID:  invocationID,
			InstanceID:    instanceID,
			PluginID:      pluginID,
			CapabilityID:  capabilityID,
			Source:        source,
			Subject:       subj.ID,
			StartedAt:     started,
			FinishedAt:    time.Now().UTC(),
			DurationMS:    result.DurationMS,
			Status:        status,
			ErrorCategory: category,
			TaskID:        taskID,
			InputSummary:  summarizeJSON(input),
			OutputSummary: summarizeJSON(result.Output),
		})
	}

	if d := s.authorizer.Authorize(ctx, subj, authz.ActionPluginInvoke, target); !d.Allowed {
		s.recordAudit(ctx, subj, authz.ActionPluginInvoke, target, "denied", d.Reason)
		result.Error = &runtime.HostError{Code: runtime.CodePermissionDenied, Message: "无插件调用权限"}
		finish("", registry.StatusError, runtime.CodePermissionDenied)
		return result, nil
	}

	inst, err := s.instances.GetInstance(ctx, instanceID)
	if errors.Is(err, instances.ErrNotFound) {
		result.Error = &runtime.HostError{Code: runtime.CodeCapabilityUnknown, Message: fmt.Sprintf("实例 %s 未登记", instanceID)}
		finish("", registry.StatusError, runtime.CodeCapabilityUnknown)
		return result, nil
	}
	if err != nil {
		return result, err
	}
	result.PluginID = inst.TypeID
	typeRow, err := s.instances.GetType(ctx, inst.TypeID)
	if err != nil {
		result.Error = &runtime.HostError{Code: runtime.CodeCapabilityUnknown, Message: fmt.Sprintf("插件类型 %s 未登记", inst.TypeID)}
		finish(inst.TypeID, registry.StatusError, runtime.CodeCapabilityUnknown)
		return result, nil
	}
	caps, err := s.instances.ListCapabilities(ctx, inst.TypeID)
	if err != nil {
		return result, err
	}
	var capability instances.Capability
	found := false
	for _, c := range caps {
		if c.CapabilityID == capabilityID {
			capability, found = c, true
			break
		}
	}
	if !found {
		result.Error = &runtime.HostError{Code: runtime.CodeCapabilityUnknown,
			Message: fmt.Sprintf("插件类型 %s 未声明能力 %s", inst.TypeID, capabilityID)}
		finish(inst.TypeID, registry.StatusError, runtime.CodeCapabilityUnknown)
		return result, nil
	}

	notReady := func(msg string) (InvokeResult, error) {
		result.Error = &runtime.HostError{Code: runtime.CodeNotReady, Message: msg}
		finish(inst.TypeID, registry.StatusError, runtime.CodeNotReady)
		s.recordAudit(ctx, subj, authz.ActionPluginInvoke, target, "failure", runtime.CodeNotReady)
		return result, nil
	}
	if !inst.Installed {
		return notReady(fmt.Sprintf("实例 %s 尚未安装", instanceID))
	}
	if inst.Drift != instances.DriftNone {
		return notReady(fmt.Sprintf("实例 %s 存在漂移（%s），拒绝调用", instanceID, inst.Drift))
	}
	if !inst.Enabled {
		return notReady(fmt.Sprintf("实例 %s 未启用", instanceID))
	}
	if !inst.Callable {
		reason := inst.StateReason
		if reason == "" {
			reason = inst.State
		}
		return notReady(fmt.Sprintf("实例 %s 当前不可调用（%s）", instanceID, reason))
	}

	select {
	case s.sem <- struct{}{}:
		defer func() { <-s.sem }()
	case <-ctx.Done():
		result.Error = &runtime.HostError{Code: runtime.CodeCancelled, Message: "调用方已取消"}
		finish(inst.TypeID, registry.StatusError, runtime.CodeCancelled)
		return result, ctx.Err()
	}

	unlock := func() {}
	if capability.Concurrency == "serial" {
		mu := s.serialMutex(instanceID + "|" + capabilityID)
		mu.Lock()
		unlock = mu.Unlock
	}
	defer unlock()

	var res runtime.Result
	if inst.Custody == instances.CustodyLocal {
		res = s.invokeLocal(ctx, typeRow, inst, capabilityID, input, subj.ID, invocationID)
	} else {
		if s.remote == nil || s.hosts == nil {
			result.Error = &runtime.HostError{Code: runtime.CodeDependencyUnavailable, Message: "远端调用组件未装配"}
			finish(inst.TypeID, registry.StatusError, runtime.CodeDependencyUnavailable)
			return result, nil
		}
		rt, err := s.remoteTarget(ctx, inst)
		if err != nil {
			result.Error = &runtime.HostError{Code: runtime.CodeDependencyUnavailable, Message: err.Error()}
			finish(inst.TypeID, registry.StatusError, runtime.CodeDependencyUnavailable)
			return result, nil
		}
		res = s.remote.Invoke(ctx, rt, capabilityID, input, subj.ID, invocationID, s.timeout, s.maxBytes)
	}

	result.Output = res.Output
	result.PluginVersion = res.PluginVersion
	if res.Status == registry.StatusOK {
		finish(inst.TypeID, registry.StatusOK, "")
		s.recordAudit(ctx, subj, authz.ActionPluginInvoke, target, "success", "")
		return result, nil
	}

	status := registry.StatusError
	category := ""
	if res.Error != nil {
		category = res.Error.Code
		if category == runtime.CodeTimeout {
			status = registry.StatusTimeout
		}
	}
	result.Error = res.Error
	finish(inst.TypeID, status, category)
	s.recordAudit(ctx, subj, authz.ActionPluginInvoke, target, "failure", category)
	if inst.Custody == instances.CustodyRemote && (category == runtime.CodeDependencyUnavailable || category == runtime.CodeTimeout) {
		// 调用失败立即降级，避免界面继续展示可调用（DOC-17 §5.2）。
		if err := s.instances.UpdateStatus(ctx, inst.InstanceID, instances.StateUnknown,
			truncate("调用失败: "+category, 512), inst.Drift, truncate(res.Error.Message, 512), inst.Installed, inst.Started, false); err != nil {
			s.logger.Warn("调用失败降级写入失败", "instance_id", inst.InstanceID, "error", err)
		}
	}
	if len(res.StderrTail) > 0 {
		s.logger.Warn("插件 stderr", "plugin_id", inst.TypeID, "instance_id", instanceID,
			"capability_id", capabilityID, "invocation_id", invocationID, "stderr", truncate(res.StderrTail, 4096))
	}
	return result, nil
}

func (s *Service) invokeLocal(ctx context.Context, t instances.Type, inst instances.Instance,
	capabilityID string, input json.RawMessage, subject, invocationID string) runtime.Result {
	var m manifest.Manifest
	if err := yaml.Unmarshal([]byte(t.ManifestYAML), &m); err != nil {
		return runtime.Result{Status: registry.StatusError, Error: &runtime.HostError{
			Code: runtime.CodeInternalError, Message: "已登记清单无法解析: " + err.Error()}}
	}
	row, err := s.registry.Get(ctx, inst.TypeID)
	if err != nil {
		return runtime.Result{Status: registry.StatusError, Error: &runtime.HostError{
			Code: runtime.CodeInternalError, Message: "读取插件登记失败: " + err.Error()}}
	}
	spec := runtime.InvokeSpec{
		Root:            row.InstallPath,
		Executable:      m.Entry.Executable,
		Capability:      capabilityID,
		Input:           input,
		Subject:         subject,
		InvocationID:    invocationID,
		ContractVersion: t.ContractVersion,
		Deadline:        s.timeout,
		MaxResultBytes:  s.maxBytes,
	}
	// daemon 形态以 --ops-config 作为配置的唯一入口（plugin-spec/11 §4）。
	if m.Form == "daemon" {
		if path, err := s.writeLocalConfigFile(ctx, inst); err == nil && path != "" {
			spec.Args = []string{"--ops-config", path}
		} else if err != nil {
			return runtime.Result{Status: registry.StatusError, Error: &runtime.HostError{
				Code: runtime.CodeInternalError, Message: "生成实例配置失败: " + err.Error()}}
		}
	}
	return runtime.Invoke(ctx, spec)
}

// writeLocalConfigFile 生成本机实例配置；文件权限 0600，目录 0700（ADR-0022 §6）。
func (s *Service) writeLocalConfigFile(ctx context.Context, inst instances.Instance) (string, error) {
	if s.box == nil {
		return "", errors.New("配置加密组件未装配")
	}
	entries, err := s.instances.ListConfig(ctx, inst.InstanceID)
	if err != nil {
		return "", err
	}
	if len(entries) == 0 {
		return "", nil
	}
	values := make(map[string]any, len(entries))
	for _, e := range entries {
		plaintext, err := s.box.Decrypt(e.Ciphertext)
		if err != nil {
			return "", fmt.Errorf("解密配置项 %s 失败: %w", e.Key, err)
		}
		var v any
		if err := json.Unmarshal(plaintext, &v); err != nil {
			return "", fmt.Errorf("解析配置项 %s 失败: %w", e.Key, err)
		}
		values[e.Key] = v
	}
	dir := filepath.Join(s.dataDir, "instances", sanitizeInstanceID(inst.InstanceID))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	body := map[string]any{
		"deployment":  instances.CustodyLocal,
		"plugin_id":   inst.TypeID,
		"instance_id": inst.InstanceID,
		"data_dir":    filepath.Join(dir, "data"),
		"config":      values,
	}
	data, err := yaml.Marshal(body)
	if err != nil {
		return "", err
	}
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return "", err
	}
	_ = os.Chmod(path, 0o600)
	return path, nil
}

// normalizeInstanceID 把插件标识别名解析为本机实例标识。
func normalizeInstanceID(id string) string {
	id = strings.TrimSpace(id)
	if id == "" || strings.HasPrefix(id, "local:") || strings.HasPrefix(id, "remote:") {
		return id
	}
	return instances.LocalInstanceID(id)
}

func sanitizeInstanceID(id string) string {
	replacer := strings.NewReplacer(":", "_", "/", "_", "\\", "_", "..", "_")
	return replacer.Replace(id)
}
