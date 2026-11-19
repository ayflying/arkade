package handler

// container_recreate_handler_test.go：container.recreate 的协议边界。
//
// 只验参数校验、能力探测与返回值语义；"重建真的按顺序执行"由
// runtime 包的 container_recreate_test.go 覆盖（那里能断言真实 HTTP 顺序）。

import (
	"context"
	"errors"
	"strings"
	"testing"

	"arkade/agent/internal/runtime"
	"arkade/protocol/envelope"
)

// recreateRuntime 在 fakeRuntime 之上补出重建能力。
//
// 用组合而不是改 fakeRuntime：那个替身被几十个用例共用，
// 给它加一个"只有本文件关心"的字段会让其它用例的构造意图变模糊。
type recreateRuntime struct {
	*fakeRuntime
	newID    string
	err      error
	lastPull bool
	lastRef  string
}

func (r *recreateRuntime) RecreateContainer(_ context.Context, idOrName string, pull bool) (string, error) {
	r.lastRef = idOrName
	r.lastPull = pull
	return r.newID, r.err
}

// TestRecreateHandlerRequiresObjectPayload 验证非对象载荷被拒绝。
//
// 宽松解码下 `true` 会被当成结构体零值，重建就会从"照原配置"变成
// "用默认配置新建"，配置被改掉且没有任何提示——必须挡在入口。
func TestRecreateHandlerRequiresObjectPayload(t *testing.T) {
	rt := &recreateRuntime{fakeRuntime: &fakeRuntime{}, newID: "newid"}
	reg := newTestRegistry(rt)

	resp := reg.Dispatch(context.Background(), mustRawRequest(t, envelope.LocalMethodContainerRecreate, true))
	if resp.Error == nil || resp.Error.Code != envelope.CodeBadRequest {
		t.Fatalf("非对象载荷应返回 bad_request，实际 %+v", resp.Error)
	}
}

// mustRawRequest 用一个非结构体载荷构造请求，专门喂给"载荷必须是对象"的校验。
func mustRawRequest(t *testing.T, method string, payload any) *envelope.Envelope {
	t.Helper()
	req, err := envelope.NewRequest("req-raw", method, "agent:test", payload)
	if err != nil {
		t.Fatalf("构造请求失败: %v", err)
	}
	return req
}

// TestRecreateHandlerRequiresContainerID 验证空 ID 被拒绝。
func TestRecreateHandlerRequiresContainerID(t *testing.T) {
	rt := &recreateRuntime{fakeRuntime: &fakeRuntime{}}
	reg := newTestRegistry(rt)

	resp := request(t, reg, envelope.LocalMethodContainerRecreate, envelope.ContainerRecreatePayload{ContainerID: "  "})
	if resp.Error == nil || resp.Error.Code != envelope.CodeBadRequest {
		t.Fatalf("空 ID 应返回 bad_request，实际 %+v", resp.Error)
	}
}

// TestRecreateHandlerUnsupportedWithoutCapability 验证引擎不支持时返回 unsupported。
//
// 为什么要单独测：Hub 依赖这个错误码提示"请升级 Agent"。
// 若它退化成 runtime_failed（503），用户会以为"稍后重试就好"，
// 但重试永远不会成功。
func TestRecreateHandlerUnsupportedWithoutCapability(t *testing.T) {
	reg := newTestRegistry(&fakeRuntime{}) // 没有 RecreateContainer 方法

	resp := request(t, reg, envelope.LocalMethodContainerRecreate,
		envelope.ContainerRecreatePayload{ContainerID: "web"})
	if resp.Error == nil || resp.Error.Code != envelope.CodeUnsupported {
		t.Fatalf("不支持时应返回 unsupported，实际 %+v", resp.Error)
	}
}

// TestRecreateHandlerReturnsNewID 验证返回的是新容器 ID 与最新状态。
func TestRecreateHandlerReturnsNewID(t *testing.T) {
	rt := &recreateRuntime{
		fakeRuntime: &fakeRuntime{containers: []runtime.Container{
			{ID: "newid0123456789", Name: "web", State: "running"},
		}},
		newID: "newid0123456789",
	}
	reg := newTestRegistry(rt)

	resp := request(t, reg, envelope.LocalMethodContainerRecreate,
		envelope.ContainerRecreatePayload{ContainerID: "oldid", Pull: true})
	if resp.Error != nil {
		t.Fatalf("重建应成功，实际错误 %+v", resp.Error)
	}
	out := envelope.ContainerOpResult{}
	if err := envelope.DecodePayload(resp, &out); err != nil {
		t.Fatalf("应答无法解析: %v", err)
	}
	// 返回旧 ID 会让前端继续操作一个已经不存在的容器。
	// 这里拿到的是列表投影后的短 ID（与 start/stop 的既有行为一致），
	// 所以断言前缀而不是完整 ID。
	if !strings.HasPrefix("newid0123456789", out.ContainerID) || out.ContainerID == "" {
		t.Errorf("应返回新容器 ID，实际 %q", out.ContainerID)
	}
	if strings.HasPrefix("oldid", out.ContainerID) {
		t.Errorf("不能返回旧容器 ID，实际 %q", out.ContainerID)
	}
	if out.State != "running" {
		t.Errorf("应回读最新状态，实际 %q", out.State)
	}
	if !rt.lastPull || rt.lastRef != "oldid" {
		t.Errorf("pull 与容器 ID 应透传给运行时，实际 pull=%v ref=%q", rt.lastPull, rt.lastRef)
	}
}

// TestRecreateHandlerTranslatesError 验证运行时错误被映射成协议错误码。
func TestRecreateHandlerTranslatesError(t *testing.T) {
	rt := &recreateRuntime{
		fakeRuntime: &fakeRuntime{},
		err:         errors.Join(runtime.ErrNotFound, errors.New("容器不存在或已被删除")),
	}
	reg := newTestRegistry(rt)

	resp := request(t, reg, envelope.LocalMethodContainerRecreate,
		envelope.ContainerRecreatePayload{ContainerID: "ghost"})
	if resp.Error == nil || resp.Error.Code != envelope.CodeNotFound {
		t.Fatalf("容器不存在应映射成 not_found，实际 %+v", resp.Error)
	}
}
