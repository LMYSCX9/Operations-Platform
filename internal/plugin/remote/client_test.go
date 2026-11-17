package remote

import (
	"context"
	"errors"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"operations-platform/internal/plugin/runtime"
)

func TestMapGRPCErrorCategories(t *testing.T) {
	cases := []struct {
		code codes.Code
		want error
	}{
		{codes.DeadlineExceeded, errTimeout},
		{codes.Unavailable, errUnavailable},
		{codes.Aborted, errUnavailable},
		{codes.NotFound, errUnavailable},
		{codes.Unimplemented, errUnavailable},
		{codes.PermissionDenied, errPermission},
		{codes.Unauthenticated, errPermission},
		{codes.InvalidArgument, errInvalid},
		{codes.FailedPrecondition, errInvalid},
	}
	for _, c := range cases {
		err := mapGRPCError(status.Error(c.code, "boom"))
		if !errors.Is(err, c.want) {
			t.Fatalf("状态码 %s 应映射为 %v，实际 %v", c.code, c.want, err)
		}
	}
}

func TestInvokeWithoutTLSFailsWithoutPlaintextFallback(t *testing.T) {
	c := New(Options{})
	res := c.Invoke(context.Background(), Target{
		HostID: "host-1", PluginID: "demo", InstanceID: "remote:host-1:demo",
		Address: "127.0.0.1", Port: 9443,
	}, "echo", nil, "u", "inv-1", time.Second, 0)
	if res.Status != "error" || res.Error == nil {
		t.Fatalf("未装配 TLS 时调用必须失败: %+v", res)
	}
	if res.Error.Code != runtime.CodeDependencyUnavailable {
		t.Fatalf("错误类别应为 dependency_unavailable，实际 %s", res.Error.Code)
	}
}

func TestProbeWithoutTLSReportsConnectStage(t *testing.T) {
	c := New(Options{})
	res := c.Probe(context.Background(), Target{
		HostID: "host-1", PluginID: "demo", Address: "127.0.0.1", Port: 9443,
	})
	if res.OK || res.Stage != "connect" {
		t.Fatalf("未装配 TLS 时探测应在 connect 阶段失败: %+v", res)
	}
}
