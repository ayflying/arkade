package handler

// resources_test.go 覆盖镜像 / 网络 / 存储卷三个代理能力的处理器。
//
// 重点：这几个接口是"代理"性质，错误必须如实透传——
// 删除失败却返回成功会让用户在界面上以为已经删掉，
// 而磁盘/卷实际还在，这类静默成功是运维事故的常见来源。

import (
	"strings"
	"testing"

	"arkade/agent/internal/runtime"
	"arkade/protocol/envelope"
)

// ---- 镜像 ----

// TestImageList 验证镜像列表转换。
func TestImageList(t *testing.T) {
	rt := &fakeRuntime{images: []runtime.Image{
		{ID: "sha256:aaa", Tags: []string{"nginx:1.27"}, Size: 100, Created: 1700000000},
		{ID: "sha256:bbb", Tags: nil, Size: 200, Dangling: true},
	}}
	resp := request(t, newTestRegistry(rt), envelope.MethodImageList, nil)
	if resp.Error != nil {
		t.Fatalf("不应报错: %+v", resp.Error)
	}
	var res envelope.ImageListResult
	if err := envelope.DecodePayload(resp, &res); err != nil {
		t.Fatal(err)
	}
	if len(res.Images) != 2 {
		t.Fatalf("应有 2 个镜像，实际 %d", len(res.Images))
	}
	// 悬空镜像的 Tags 必须是空数组而不是 null，否则前端遍历会崩。
	if res.Images[1].Tags == nil {
		t.Error("Tags 应为空数组而不是 nil")
	}
	if !res.Images[1].Dangling {
		t.Error("应标记为悬空镜像")
	}
}

// TestImageListNoRuntime 验证无运行时时的错误码。
func TestImageListNoRuntime(t *testing.T) {
	rt := &fakeRuntime{imagesErr: runtime.ErrNone}
	resp := request(t, newTestRegistry(rt), envelope.MethodImageList, nil)
	if resp.Error == nil {
		t.Fatal("应返回错误")
	}
	if resp.Error.Code != envelope.CodeUnsupported {
		t.Errorf("错误码应为 unsupported，实际 %q", resp.Error.Code)
	}
}

// TestImageRemove 验证删除参数透传（含 force）。
func TestImageRemove(t *testing.T) {
	rt := &fakeRuntime{}
	resp := request(t, newTestRegistry(rt), envelope.MethodImageRemove,
		envelope.ImageRemovePayload{ImageID: "sha256:abc", Force: true})
	if resp.Error != nil {
		t.Fatalf("不应报错: %+v", resp.Error)
	}
	if rt.lastImageRemove != "sha256:abc" {
		t.Errorf("应透传镜像 ID，实际 %q", rt.lastImageRemove)
	}
	if !rt.lastImageForce {
		t.Error("force 应透传为 true")
	}
}

// TestImageRemoveValidation 验证参数校验。
func TestImageRemoveValidation(t *testing.T) {
	reg := newTestRegistry(&fakeRuntime{})
	cases := []struct {
		name    string
		payload any
		want    string
	}{
		{"nil 载荷", nil, "缺少载荷"},
		{"空 ID", envelope.ImageRemovePayload{ImageID: ""}, "image_id 不能为空"},
		{"空白 ID", envelope.ImageRemovePayload{ImageID: "   "}, "image_id 不能为空"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			resp := request(t, reg, envelope.MethodImageRemove, c.payload)
			if resp.Error == nil {
				t.Fatal("应返回错误")
			}
			if resp.Error.Code != envelope.CodeBadRequest {
				t.Errorf("错误码应为 bad_request，实际 %q", resp.Error.Code)
			}
			if !strings.Contains(resp.Error.Message, c.want) {
				t.Errorf("文案应含 %q，实际 %q", c.want, resp.Error.Message)
			}
		})
	}
}

// TestImageRemoveErrorPropagates 验证删除失败必须报错。
func TestImageRemoveErrorPropagates(t *testing.T) {
	rt := &fakeRuntime{imageRemoveErr: runtime.ErrNotFound}
	resp := request(t, newTestRegistry(rt), envelope.MethodImageRemove,
		envelope.ImageRemovePayload{ImageID: "ghost"})
	if resp.Error == nil {
		t.Fatal("删除失败必须返回错误，不能静默成功")
	}
	if resp.Error.Code != envelope.CodeNotFound {
		t.Errorf("错误码应为 not_found，实际 %q", resp.Error.Code)
	}
}

// ---- 网络 ----

// TestNetworkList 验证网络列表转换。
func TestNetworkList(t *testing.T) {
	rt := &fakeRuntime{networks: []runtime.Network{
		{ID: "n1", Name: "bridge", Driver: "bridge", Scope: "local", Containers: 3},
		{ID: "n2", Name: "app_default", Driver: "bridge", Internal: true, Labels: nil},
	}}
	resp := request(t, newTestRegistry(rt), envelope.MethodNetworkList, nil)
	if resp.Error != nil {
		t.Fatalf("不应报错: %+v", resp.Error)
	}
	var res envelope.NetworkListResult
	if err := envelope.DecodePayload(resp, &res); err != nil {
		t.Fatal(err)
	}
	if len(res.Networks) != 2 {
		t.Fatalf("应有 2 个网络，实际 %d", len(res.Networks))
	}
	// Labels 为 nil 时必须转成空对象。
	if res.Networks[1].Labels == nil {
		t.Error("Labels 应为空对象而不是 nil")
	}
	if !res.Networks[1].Internal {
		t.Error("internal 应透传")
	}
	if res.Networks[0].Containers != 3 {
		t.Errorf("容器数应为 3，实际 %d", res.Networks[0].Containers)
	}
}

// TestNetworkRemove 验证删除透传与校验。
func TestNetworkRemove(t *testing.T) {
	rt := &fakeRuntime{}
	reg := newTestRegistry(rt)

	resp := request(t, reg, envelope.MethodNetworkRemove,
		envelope.NetworkRemovePayload{NetworkID: "net-1"})
	if resp.Error != nil {
		t.Fatalf("不应报错: %+v", resp.Error)
	}
	if rt.lastNetworkRemove != "net-1" {
		t.Errorf("应透传网络 ID，实际 %q", rt.lastNetworkRemove)
	}

	resp = request(t, reg, envelope.MethodNetworkRemove,
		envelope.NetworkRemovePayload{NetworkID: ""})
	if resp.Error == nil || resp.Error.Code != envelope.CodeBadRequest {
		t.Errorf("空 ID 应返回 bad_request，实际 %+v", resp.Error)
	}
}

// ---- 存储卷 ----

// TestVolumeList 验证卷列表转换（含使用状态）。
func TestVolumeList(t *testing.T) {
	rt := &fakeRuntime{volumes: []runtime.Volume{
		{Name: "app_pgdata", Driver: "local", Mountpoint: "/var/lib/docker/volumes/app_pgdata/_data", InUse: true},
		{Name: "app_cache", Driver: "local"},
	}}
	resp := request(t, newTestRegistry(rt), envelope.MethodVolumeList, nil)
	if resp.Error != nil {
		t.Fatalf("不应报错: %+v", resp.Error)
	}
	var res envelope.VolumeListResult
	if err := envelope.DecodePayload(resp, &res); err != nil {
		t.Fatal(err)
	}
	if len(res.Volumes) != 2 {
		t.Fatalf("应有 2 个卷，实际 %d", len(res.Volumes))
	}
	if !res.Volumes[0].InUse {
		t.Error("第一个卷应标记为使用中")
	}
	if res.Volumes[1].InUse {
		t.Error("第二个卷不应标记为使用中")
	}
	if res.Volumes[1].Labels == nil {
		t.Error("Labels 应为空对象而不是 nil")
	}
}

// TestVolumeRemove 验证删除透传与参数校验。
//
// 这是全项目最危险的接口（删卷 = 永久丢数据），
// 因此参数校验必须严：空名字会让运行时按空前缀匹配，行为不可预测。
func TestVolumeRemove(t *testing.T) {
	rt := &fakeRuntime{}
	reg := newTestRegistry(rt)

	resp := request(t, reg, envelope.MethodVolumeRemove,
		envelope.VolumeRemovePayload{Name: "app_pgdata"})
	if resp.Error != nil {
		t.Fatalf("不应报错: %+v", resp.Error)
	}
	if rt.lastVolumeRemove != "app_pgdata" {
		t.Errorf("应透传卷名，实际 %q", rt.lastVolumeRemove)
	}

	for _, c := range []struct {
		name    string
		payload any
	}{
		{"缺载荷", nil},
		{"空名", envelope.VolumeRemovePayload{Name: ""}},
		{"空白名", envelope.VolumeRemovePayload{Name: "  "}},
	} {
		t.Run(c.name, func(t *testing.T) {
			resp := request(t, reg, envelope.MethodVolumeRemove, c.payload)
			if resp.Error == nil {
				t.Fatal("应返回错误")
			}
			if resp.Error.Code != envelope.CodeBadRequest {
				t.Errorf("错误码应为 bad_request，实际 %q", resp.Error.Code)
			}
		})
	}
}

// TestVolumeRemoveError 验证删除失败必须报错。
//
// 静默成功是最危险的 bug：用户以为卷删了，实际磁盘还在被占。
func TestVolumeRemoveError(t *testing.T) {
	rt := &fakeRuntime{volumeRemoveErr: runtime.ErrNotFound}
	resp := request(t, newTestRegistry(rt), envelope.MethodVolumeRemove,
		envelope.VolumeRemovePayload{Name: "ghost"})
	if resp.Error == nil {
		t.Fatal("删除失败必须返回错误，不能静默成功")
	}
	if resp.Error.Code != envelope.CodeNotFound {
		t.Errorf("错误码应为 not_found，实际 %q", resp.Error.Code)
	}
}

// TestResourceMethodsRegistered 验证资源方法确实注册进了注册表。
//
// 防止"写了处理器却忘了注册"——那种情况下 Hub 会收到 unsupported
// 并提示升级 Agent，而实际只是漏了一行注册，排查会非常费劲。
func TestResourceMethodsRegistered(t *testing.T) {
	reg := newTestRegistry(&fakeRuntime{})
	methods := []string{
		envelope.MethodImageList, envelope.MethodImageRemove,
		envelope.MethodNetworkList, envelope.MethodNetworkRemove,
		envelope.MethodVolumeList, envelope.MethodVolumeRemove,
	}
	for _, m := range methods {
		if !reg.Has(m) {
			t.Errorf("方法 %s 未注册", m)
		}
	}
}
