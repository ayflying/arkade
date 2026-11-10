package handler

// container_detail_test.go：container.inspect 处理器的契约测试。
//
// 与 runtime 层的解析测试互补：
//   - runtime 测试证明"daemon 形状能解析对"；
//   - 这里证明"处理器把解析结果按协议搬对了，且没有多搬"。

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"arkade/agent/internal/runtime"
	"arkade/protocol/envelope"
)

// inspectFake 是实现了容器详情能力的假运行时。
type inspectFake struct {
	fakeRuntime
	detail runtime.InspectDetail
	err    error
}

func (f *inspectFake) InspectContainer(context.Context, string) (runtime.InspectDetail, error) {
	if f.err != nil {
		return runtime.InspectDetail{}, f.err
	}
	return f.detail, nil
}

// runInspect 通过注册表派发一次 container.inspect。
func runInspect(t *testing.T, rt runtime.Runtime, id string) *envelope.Envelope {
	t.Helper()
	reg := NewRegistry()
	reg.Register(envelope.MethodContainerInspect, newInspectHandler(rt))
	p, _ := json.Marshal(envelope.ContainerInspectPayload{ContainerID: id})
	return reg.Dispatch(context.Background(), &envelope.Envelope{
		V: envelope.Version, ID: "t", Type: envelope.TypeRequest,
		Method: envelope.MethodContainerInspect, Payload: p,
	})
}

// TestInspectHandlerMapsAllFields 验证所有字段被正确搬运。
func TestInspectHandlerMapsAllFields(t *testing.T) {
	pids := int64(64)
	f := &inspectFake{detail: runtime.InspectDetail{
		ID:           "abc",
		Name:         "demo-web-1",
		Image:        "nginx:alpine",
		State:        "running",
		Status:       "running",
		Created:      "2026-11-10T02:03:04Z",
		StartedAt:    "2026-11-10T02:03:05Z",
		FinishedAt:   "0001-01-01T00:00:00Z",
		ExitCode:     0,
		RestartCount: 2,
		NetworkMode:  "bridge",
		Command:      []string{"/entry.sh"},
		Networks:     []runtime.InspectNetwork{{Name: "bridge", IPAddress: "172.17.0.5"}},
		Mounts: []runtime.InspectMount{{
			Type: "bind", Source: "/srv/data", Destination: "/data", ReadOnly: true,
		}},
		Ports: []runtime.Port{
			{IP: "0.0.0.0", Private: 80, Public: 8080, Type: "tcp"},
			// Public=0：仅暴露未发布，不得进 PublishedPorts。
			{IP: "", Private: 443, Public: 0, Type: "tcp"},
		},
		ExposedPorts: []string{"443/tcp"},
		MemoryBytes:  536870912,
		NanoCPUs:     1500000000,
		PidsLimit:    &pids,
	}}

	resp := runInspect(t, f, "abc")
	if resp.Error != nil {
		t.Fatalf("不应失败: %+v", resp.Error)
	}
	var out envelope.ContainerInspectResult
	if err := envelope.DecodePayload(resp, &out); err != nil {
		t.Fatal(err)
	}

	if out.ID != "abc" || out.Name != "demo-web-1" || out.Image != "nginx:alpine" {
		t.Fatalf("基本字段不符: %+v", out)
	}
	if out.RestartCount != 2 || out.NetworkMode != "bridge" {
		t.Fatalf("重启/网络模式不符: %+v", out)
	}
	if len(out.Networks) != 1 || out.Networks[0].IPAddress != "172.17.0.5" {
		t.Fatalf("网络不符: %+v", out.Networks)
	}
	if len(out.Mounts) != 1 || !out.Mounts[0].ReadOnly {
		t.Fatalf("挂载不符: %+v", out.Mounts)
	}

	// 只发布真实映射的端口。
	if len(out.PublishedPorts) != 1 {
		t.Fatalf("只应有 1 条已发布端口（Public=0 的必须排除），实际 %+v", out.PublishedPorts)
	}
	pp := out.PublishedPorts[0]
	if pp.HostPort != 8080 || pp.ContainerPort != 80 || pp.Protocol != "tcp" {
		t.Fatalf("端口字段不符: %+v", pp)
	}
	if len(out.ExposedPorts) != 1 || out.ExposedPorts[0] != "443/tcp" {
		t.Fatalf("仅暴露端口不符: %v", out.ExposedPorts)
	}
	if out.ResourceLimits.MemoryBytes != 536870912 || out.ResourceLimits.NanoCPUs != 1500000000 {
		t.Fatalf("资源上限不符: %+v", out.ResourceLimits)
	}
	if out.ResourceLimits.PidsLimit == nil || *out.ResourceLimits.PidsLimit != 64 {
		t.Fatalf("pids 上限不符: %v", out.ResourceLimits.PidsLimit)
	}
}

// TestInspectHandlerNeverEmitsEnvOrLabels 是安全断言：协议载荷里没有 Env/Labels 字段。
//
// 即使将来有人往 runtime.InspectDetail 里加了敏感字段，
// 这个测试会因为序列化里出现字段名而失败——它守的是协议形状。
func TestInspectHandlerNeverEmitsEnvOrLabels(t *testing.T) {
	f := &inspectFake{detail: runtime.InspectDetail{ID: "abc", Name: "c"}}
	resp := runInspect(t, f, "abc")
	var out envelope.ContainerInspectResult
	if err := envelope.DecodePayload(resp, &out); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(out)
	s := strings.ToLower(string(raw))
	for _, banned := range []string{`"env"`, `"labels"`, `"secrets"`, `"configs"`} {
		if strings.Contains(s, banned) {
			t.Fatalf("协议载荷不应包含字段 %s: %s", banned, raw)
		}
	}
}

// TestInspectHandlerEmptyIDIsBadRequest 验证空 ID 返回 bad_request（不是 runtime 失败）。
func TestInspectHandlerEmptyIDIsBadRequest(t *testing.T) {
	resp := runInspect(t, &inspectFake{}, "   ")
	if resp.Error == nil {
		t.Fatal("空 ID 必须报错")
	}
	if resp.Error.Code != envelope.CodeBadRequest {
		t.Fatalf("期望 bad_request，实际 %s", resp.Error.Code)
	}
}

// TestInspectHandlerUnsupportedEngine 验证引擎不支持时返回 unsupported。
func TestInspectHandlerUnsupportedEngine(t *testing.T) {
	// fakeRuntime 没实现 ContainerInspectRuntime。
	resp := runInspect(t, &fakeRuntime{}, "abc")
	if resp.Error == nil || resp.Error.Code != envelope.CodeUnsupported {
		t.Fatalf("期望 unsupported，实际 %+v", resp.Error)
	}
}

// TestInspectHandlerPropagatesError 验证底层错误由统一映射处理。
func TestInspectHandlerPropagatesError(t *testing.T) {
	f := &inspectFake{err: runtime.ErrNotFound}
	resp := runInspect(t, f, "abc")
	if resp.Error == nil || resp.Error.Code != envelope.CodeNotFound {
		t.Fatalf("期望 not_found，实际 %+v", resp.Error)
	}
}
