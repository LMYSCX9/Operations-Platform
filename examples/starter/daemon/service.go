// PluginService 实现：GetVersion / Ready / Health / Invoke / Shutdown。
// 规则（plugin-spec/03 §4）：只暴露通用 Invoke；一律串行调度；Ready 与 Health 分离；
// 错误只用 contract.go 已登记的类别；deadline_ms 由 gRPC 上下文承载，handler 必须可被超时终止。
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"gopkg.in/yaml.v3"

	pluginv1 "example.com/starter-daemon/gen/pluginv1"
)

// yamlUnmarshal 独立封装便于统一错误信息。
func yamlUnmarshal(data []byte, out any) error {
	return yaml.Unmarshal(data, out)
}

// waitForTerm 阻塞等待 SIGTERM/SIGINT，收到后触发优雅停止（systemctl stop 发送 SIGTERM）。
func waitForTerm(stop func()) {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGTERM, syscall.SIGINT)
	<-ch
	stop()
}

type service struct {
	pluginv1.UnimplementedPluginServiceServer

	cfg      *remoteConfig
	contract string // GetVersion 上报的实际契约版本，必须落在清单声明范围内

	// invokeMu 保证同一插件串行处理调用（跨能力亦串行）；清单 concurrency 只是承受能力声明。
	invokeMu sync.Mutex
	srv      grpcServer
}

// grpcServer 是 shutdown 需要的最小接口，避免 service 直接依赖 grpc.Server 类型。
type grpcServer interface {
	GracefulStop()
}

func newService(cfg *remoteConfig, srv grpcServer) *service {
	return &service{cfg: cfg, contract: pluginv1.ContractVersion, srv: srv}
}

func (s *service) shutdown() {
	// 优雅停止：停止接受新调用 → drain 在途调用（GracefulStop）→ Serve 返回后进程退出。
	s.srv.GracefulStop()
}

func (s *service) GetVersion(context.Context, *pluginv1.GetVersionRequest) (*pluginv1.GetVersionResponse, error) {
	return &pluginv1.GetVersionResponse{
		PluginVersion:   pluginVersion,
		ContractVersion: s.contract,
	}, nil
}

// Ready 远端形态没有一次性凭据文件；配置与证书在 main 启动期已校验，能监听即视为就绪。
func (s *service) Ready(context.Context, *pluginv1.ReadyRequest) (*pluginv1.ReadyResponse, error) {
	return &pluginv1.ReadyResponse{Ready: true}, nil
}

func (s *service) Health(context.Context, *pluginv1.HealthRequest) (*pluginv1.HealthResponse, error) {
	return &pluginv1.HealthResponse{Healthy: true}, nil
}

func (s *service) Shutdown(_ context.Context, req *pluginv1.ShutdownRequest) (*pluginv1.ShutdownResponse, error) {
	logf("收到 Shutdown（grace_ms=%d），开始优雅停止", req.GetGraceMs())
	go s.shutdown()
	return &pluginv1.ShutdownResponse{Accepted: true}, nil
}

func (s *service) Invoke(ctx context.Context, req *pluginv1.InvokeRequest) (*pluginv1.InvokeResponse, error) {
	s.invokeMu.Lock()
	defer s.invokeMu.Unlock()

	resp := func(status string, output []byte, perr *pluginv1.PluginError) *pluginv1.InvokeResponse {
		return &pluginv1.InvokeResponse{
			Status:        status,
			OutputJson:    output,
			Error:         perr,
			PluginVersion: pluginVersion,
		}
	}
	errResp := func(code, message string, retryable bool) *pluginv1.InvokeResponse {
		return resp(pluginv1.StatusError, nil, &pluginv1.PluginError{
			Code: code, Message: message, Retryable: retryable,
		})
	}

	// 契约主版本不符：拒绝调用（plugin-spec/03 §8）。MINOR 差异由能力内部自证兼容。
	if major(req.GetContractVersion()) != major(s.contract) {
		return errResp(pluginv1.CodeVersionIncompatible,
			fmt.Sprintf("底座契约版本 %s 与插件契约主版本 %s 不兼容", req.GetContractVersion(), s.contract), false), nil
	}

	// context.subject 可能敏感，禁止写日志；context.credential 远端可省略，只存内存。

	switch req.GetCapability() {
	case "starter-daemon.now":
		return s.now(ctx, req), nil
	default:
		return errResp(pluginv1.CodeCapabilityUnknown, "不支持的能力 "+req.GetCapability(), false), nil
	}
}

func (s *service) now(ctx context.Context, req *pluginv1.InvokeRequest) *pluginv1.InvokeResponse {
	var in struct {
		Format string `json:"format"`
	}
	if len(req.GetInputJson()) > 0 {
		if err := json.Unmarshal(req.GetInputJson(), &in); err != nil {
			return s.errInvoke(pluginv1.CodeInputInvalid, "input 解析失败: "+err.Error(), false)
		}
	}
	switch in.Format {
	case "", "rfc3339":
	case "unix":
	default:
		return s.errInvoke(pluginv1.CodeInputInvalid,
			fmt.Sprintf("字段 format 取值 %q 不在允许集合 [rfc3339, unix] 内", in.Format), false)
	}

	// 实例配置作用域（plugin-spec/11）：同一插件的不同实例可有不同 config 值；
	// 值来自按主机配置文件的 config 段，由底座按实例写入。
	label, _ := s.cfg.Config["label"].(string)
	if label == "" {
		label = "starter-daemon"
	}

	now := time.Now()
	output := map[string]string{"label": label, "now": now.Format(time.RFC3339Nano)}
	if in.Format == "unix" {
		output["now"] = fmt.Sprintf("%d", now.Unix())
	}
	raw, err := json.Marshal(output)
	if err != nil {
		return s.errInvoke(pluginv1.CodeInternalError, "构造输出失败", false)
	}
	return &pluginv1.InvokeResponse{
		Status:        pluginv1.StatusOK,
		OutputJson:    raw,
		PluginVersion: pluginVersion,
	}
}

func (s *service) errInvoke(code, message string, retryable bool) *pluginv1.InvokeResponse {
	return &pluginv1.InvokeResponse{
		Status: pluginv1.StatusError,
		Error:  &pluginv1.PluginError{Code: code, Message: message, Retryable: retryable},
	}
}

func major(v string) string {
	for i := 0; i < len(v); i++ {
		if v[i] == '.' {
			return v[:i]
		}
	}
	return v
}
