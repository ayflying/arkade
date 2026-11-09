package handler

// container_ops_test.go 覆盖 container.restart 与 container.remove。
//
// 重点不是"能调到运行时"，而是三件容易出事的语义：
//  1. force 必须真的透传——不透传则运行中的容器根本删不掉，功能等于废的；
//  2. remove_volumes 默认必须是 false——它是数据丢失开关；
//  3. 载荷形态受到约束，防止误调用把"删除"静默升级成"强制删除"。

import (
	"strings"
	"testing"

	"arkade/agent/internal/runtime"
	"arkade/protocol/envelope"
)

// TestContainerRestart 验证重启成功路径与 ID 透传。
func TestContainerRestart(t *testing.T) {
	rt := &fakeRuntime{containers: []runtime.Container{{ID: "abc123456789", Name: "web", State: "running"}}}
	resp := request(t, newTestRegistry(rt), envelope.MethodContainerRestart,
		envelope.ContainerOpPayload{ContainerID: "abc123456789"})
	if resp.Error != nil {
		t.Fatalf("重启不应报错: %+v", resp.Error)
	}
	if rt.lastRestart != "abc123456789" {
		t.Errorf("应透传容器 ID，实际 %q", rt.lastRestart)
	}
	// 回读状态：与 start/stop 一致，前端不用再发一次列表请求。
	var res envelope.ContainerOpResult
	if err := envelope.DecodePayload(resp, &res); err != nil {
		t.Fatal(err)
	}
	if res.State != "running" {
		t.Errorf("应回读最新状态，实际 %q", res.State)
	}
}

// TestContainerRestartValidation 验证参数校验。
func TestContainerRestartValidation(t *testing.T) {
	reg := newTestRegistry(&fakeRuntime{})
	for _, c := range []struct {
		name    string
		payload any
	}{
		{"nil", nil},
		{"空 ID", envelope.ContainerOpPayload{ContainerID: ""}},
		{"空白 ID", envelope.ContainerOpPayload{ContainerID: "   "}},
	} {
		t.Run(c.name, func(t *testing.T) {
			resp := request(t, reg, envelope.MethodContainerRestart, c.payload)
			if resp.Error == nil {
				t.Fatal("应返回错误")
			}
			if resp.Error.Code != envelope.CodeBadRequest {
				t.Errorf("错误码应为 bad_request，实际 %q", resp.Error.Code)
			}
		})
	}
}

// TestContainerRestartErrorPropagates 验证重启失败必须报错。
func TestContainerRestartErrorPropagates(t *testing.T) {
	rt := &fakeRuntime{restartErr: runtime.ErrNotFound}
	resp := request(t, newTestRegistry(rt), envelope.MethodContainerRestart,
		envelope.ContainerOpPayload{ContainerID: "ghost"})
	if resp.Error == nil {
		t.Fatal("重启失败必须报错，不能静默成功")
	}
	if resp.Error.Code != envelope.CodeNotFound {
		t.Errorf("错误码应为 not_found，实际 %q", resp.Error.Code)
	}
}

// TestContainerRemoveDefaults 验证默认值：
// 既不强制、也不删卷。这是本接口最重要的安全断言——
// 一次"普通删除"绝不能悄悄带走数据卷。
func TestContainerRemoveDefaults(t *testing.T) {
	rt := &fakeRuntime{}
	resp := request(t, newTestRegistry(rt), envelope.MethodContainerRemove,
		envelope.ContainerOpPayload{ContainerID: "abc"})
	if resp.Error != nil {
		t.Fatalf("删除不应报错: %+v", resp.Error)
	}
	if rt.lastRemove != "abc" {
		t.Errorf("应透传容器 ID，实际 %q", rt.lastRemove)
	}
	if rt.lastRemoveForce {
		t.Error("未传 force 时不应强制删除")
	}
	if rt.lastRemoveVolumes {
		t.Error("未传 remove_volumes 时绝不能删数据卷")
	}
}

// TestContainerRemoveForce 验证 force 透传。
//
// 这条是任务点名要求的：不支持 force，运行中的容器就删不掉。
func TestContainerRemoveForce(t *testing.T) {
	rt := &fakeRuntime{}
	resp := request(t, newTestRegistry(rt), envelope.MethodContainerRemove,
		envelope.ContainerOpPayload{ContainerID: "abc", Force: true})
	if resp.Error != nil {
		t.Fatalf("删除不应报错: %+v", resp.Error)
	}
	if !rt.lastRemoveForce {
		t.Error("force=true 必须透传到运行时")
	}
	if rt.lastRemoveVolumes {
		t.Error("force 不应连带开启删卷")
	}
}

// TestContainerRemoveVolumes 验证 remove_volumes 透传，且与 force 可独立组合。
func TestContainerRemoveVolumes(t *testing.T) {
	cases := []struct {
		force, volumes      bool
		wantForce, wantVols bool
	}{
		{false, false, false, false},
		{true, false, true, false},
		{false, true, false, true},
		{true, true, true, true},
	}
	for _, c := range cases {
		rt := &fakeRuntime{}
		resp := request(t, newTestRegistry(rt), envelope.MethodContainerRemove,
			envelope.ContainerOpPayload{ContainerID: "abc", Force: c.force, RemoveVolumes: c.volumes})
		if resp.Error != nil {
			t.Fatalf("force=%v volumes=%v 不应报错: %+v", c.force, c.volumes, resp.Error)
		}
		if rt.lastRemoveForce != c.wantForce {
			t.Errorf("force=%v 时应透传 %v，实际 %v", c.force, c.wantForce, rt.lastRemoveForce)
		}
		if rt.lastRemoveVolumes != c.wantVols {
			t.Errorf("volumes=%v 时应透传 %v，实际 %v", c.volumes, c.wantVols, rt.lastRemoveVolumes)
		}
	}
}

// TestContainerRemoveRejectsScalarPayload 验证标量载荷被拒。
//
// 这是刻意的安全边界：如果载荷是 `true`，宽松解码会把它解成
// Force=true 的零值结构体，语义从"删除"静默变成"强制删除"——
// 破坏力被无声放大。要求 JSON 对象能直接拒掉这种误调用。
func TestContainerRemoveRejectsScalarPayload(t *testing.T) {
	for _, payload := range []any{true, "abc", 123, []string{"a"}} {
		rt := &fakeRuntime{}
		resp := request(t, newTestRegistry(rt), envelope.MethodContainerRemove, payload)
		if resp.Error == nil {
			t.Errorf("载荷 %v 应被拒绝", payload)
			continue
		}
		if resp.Error.Code != envelope.CodeBadRequest {
			t.Errorf("载荷 %v 错误码应为 bad_request，实际 %q", payload, resp.Error.Code)
		}
		// 关键：被拒的请求不能已经调到运行时。
		if rt.lastRemove != "" {
			t.Errorf("载荷 %v 被拒时不应调用运行时，实际删了 %q", payload, rt.lastRemove)
		}
	}
}

// TestContainerRemoveValidation 验证空 ID 被拒。
func TestContainerRemoveValidation(t *testing.T) {
	reg := newTestRegistry(&fakeRuntime{})
	for _, c := range []struct {
		name    string
		payload any
	}{
		{"nil", nil},
		{"空 ID", envelope.ContainerOpPayload{ContainerID: ""}},
		{"空白 ID", envelope.ContainerOpPayload{ContainerID: "  "}},
	} {
		t.Run(c.name, func(t *testing.T) {
			resp := request(t, reg, envelope.MethodContainerRemove, c.payload)
			if resp.Error == nil {
				t.Fatal("应返回错误")
			}
			if resp.Error.Code != envelope.CodeBadRequest {
				t.Errorf("错误码应为 bad_request，实际 %q", resp.Error.Code)
			}
		})
	}
}

// TestContainerRemoveConflictMessage 验证"运行中不能删"被翻译成可读中文。
//
// 这是用户最常撞上的错误。Docker 原文是英文长句，
// 必须变成能照做的建议：先停止或强制删除。
func TestContainerRemoveConflictMessage(t *testing.T) {
	rt := &fakeRuntime{removeErr: runtime.ErrContainerConflict}
	resp := request(t, newTestRegistry(rt), envelope.MethodContainerRemove,
		envelope.ContainerOpPayload{ContainerID: "running"})
	if resp.Error == nil {
		t.Fatal("应返回错误")
	}
	if !strings.Contains(resp.Error.Message, "正在运行") &&
		!strings.Contains(resp.Error.Message, "状态") {
		t.Errorf("应给出中文说明，实际 %q", resp.Error.Message)
	}
}

// TestContainerOpsRegistered 验证两个方法都注册成功。
func TestContainerOpsRegistered(t *testing.T) {
	reg := newTestRegistry(&fakeRuntime{})
	for _, m := range []string{
		envelope.MethodContainerRestart,
		envelope.MethodContainerRemove,
	} {
		if !reg.Has(m) {
			t.Errorf("方法 %s 未注册", m)
		}
	}
}
