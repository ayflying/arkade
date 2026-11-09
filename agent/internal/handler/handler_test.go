package handler

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"arkade/agent/internal/runtime"
	"arkade/protocol/envelope"
)

// fakeRuntime 是可编程的运行时替身。
//
// 用替身而不是真实 Docker 的原因：这里要验证的是"协议层的参数校验与错误翻译"，
// 与引擎无关；真实引擎的行为已在 runtime 包的测试里覆盖。
type fakeRuntime struct {
	containers []runtime.Container
	listErr    error
	startErr   error
	stopErr    error
	restartErr error
	removeErr  error
	logsRes    runtime.LogResult
	logsErr    error
	info       runtime.Info

	lastStart    string
	lastStop     string
	lastForce    bool
	lastLogsOpts runtime.LogOptions

	// 重启 / 删除的记录（供 container_ops_test.go 使用）。
	lastRestart       string
	lastRemove        string
	lastRemoveForce   bool
	lastRemoveVolumes bool

	// 资源类方法的固定返回与记录（供 resources_test.go 使用）。
	images            []runtime.Image
	imagesErr         error
	imageRemoveErr    error
	lastImageRemove   string
	lastImageForce    bool
	networks          []runtime.Network
	networksErr       error
	networkRemoveErr  error
	lastNetworkRemove string
	volumes           []runtime.Volume
	volumesErr        error
	volumeRemoveErr   error
	lastVolumeRemove  string
}

func (f *fakeRuntime) Info(context.Context) (runtime.Info, error) {
	if f.info.Kind == "" {
		return runtime.Info{Kind: runtime.KindDocker, Version: "27.3.1"}, nil
	}
	return f.info, nil
}

func (f *fakeRuntime) List(context.Context, bool) ([]runtime.Container, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	return f.containers, nil
}

func (f *fakeRuntime) Start(_ context.Context, id string) error {
	f.lastStart = id
	return f.startErr
}

func (f *fakeRuntime) Stop(_ context.Context, id string, force bool) error {
	f.lastStop = id
	f.lastForce = force
	return f.stopErr
}

func (f *fakeRuntime) Restart(_ context.Context, id string) error {
	f.lastRestart = id
	return f.restartErr
}

func (f *fakeRuntime) RemoveContainer(_ context.Context, id string, force, removeVolumes bool) error {
	f.lastRemove = id
	f.lastRemoveForce = force
	f.lastRemoveVolumes = removeVolumes
	return f.removeErr
}

func (f *fakeRuntime) Logs(_ context.Context, _ string, opts runtime.LogOptions) (runtime.LogResult, error) {
	f.lastLogsOpts = opts
	return f.logsRes, f.logsErr
}

// 资源类方法：编排/资源的专项测试见 compose_test.go 与 resources_test.go，
// 这里给最小实现即可（fakeRuntime 被多个测试复用，补全接口才能编译）。
func (f *fakeRuntime) ListImages(context.Context) ([]runtime.Image, error) {
	if f.imagesErr != nil {
		return nil, f.imagesErr
	}
	return f.images, nil
}

func (f *fakeRuntime) RemoveImage(_ context.Context, id string, force bool) error {
	f.lastImageRemove = id
	f.lastImageForce = force
	return f.imageRemoveErr
}

func (f *fakeRuntime) ListNetworks(context.Context) ([]runtime.Network, error) {
	if f.networksErr != nil {
		return nil, f.networksErr
	}
	return f.networks, nil
}

func (f *fakeRuntime) RemoveNetwork(_ context.Context, id string) error {
	f.lastNetworkRemove = id
	return f.networkRemoveErr
}

func (f *fakeRuntime) ListVolumes(context.Context) ([]runtime.Volume, error) {
	if f.volumesErr != nil {
		return nil, f.volumesErr
	}
	return f.volumes, nil
}

func (f *fakeRuntime) RemoveVolume(_ context.Context, name string) error {
	f.lastVolumeRemove = name
	return f.volumeRemoveErr
}

// newTestRegistry 构造带默认处理器的注册表。
//
// composeMgr 传 nil：本文件的用例只测容器类方法，编排方法不注册即可
// （注册表对未注册方法返回 unsupported，不影响已有断言）。
func newTestRegistry(rt runtime.Runtime) *Registry {
	reg := NewRegistry()
	RegisterDefaults(reg, rt, nil, nil)
	return reg
}

// request 构造请求信封并执行分发，返回响应。
func request(t *testing.T, reg *Registry, method string, payload any) *envelope.Envelope {
	t.Helper()
	req, err := envelope.NewRequest("req-1", method, "agent:test", payload)
	if err != nil {
		t.Fatalf("构造请求失败: %v", err)
	}
	resp := reg.Dispatch(context.Background(), req)
	if resp == nil {
		t.Fatal("Dispatch 返回 nil，调用方会永久等待")
	}
	return resp
}

func TestDispatchUnknownMethod(t *testing.T) {
	reg := newTestRegistry(&fakeRuntime{})
	resp := request(t, reg, "no.such.method", nil)
	if resp.Error == nil {
		t.Fatal("未知方法应返回错误")
	}
	if resp.Error.Code != envelope.CodeUnsupported {
		t.Fatalf("错误码 = %s，期望 unsupported（用于提示升级 Agent）", resp.Error.Code)
	}
	// 响应必须回填请求 ID，否则 Hub 无法配对。
	if resp.ReplyTo != "req-1" {
		t.Fatalf("ReplyTo = %q，期望 req-1", resp.ReplyTo)
	}
}

func TestDispatchInvalidEnvelope(t *testing.T) {
	reg := newTestRegistry(&fakeRuntime{})
	// 缺少 ID 的信封必须被拒绝。
	resp := reg.Dispatch(context.Background(), &envelope.Envelope{
		V: envelope.Version, Type: envelope.TypeRequest, Method: envelope.MethodRuntimeInfo,
	})
	if resp.Error == nil || resp.Error.Code != envelope.CodeBadRequest {
		t.Fatalf("期望 bad_request，实际: %+v", resp.Error)
	}
	// event 类型送进调度也应被拒绝（调度只处理 request）。
	resp = reg.Dispatch(context.Background(), &envelope.Envelope{
		V: envelope.Version, ID: "x", Type: envelope.TypeEvent, Method: envelope.MethodRuntimeInfo,
	})
	if resp.Error == nil || resp.Error.Code != envelope.CodeBadRequest {
		t.Fatalf("非 request 类型应返回 bad_request，实际: %+v", resp.Error)
	}
	// nil 请求不能 panic。
	resp = reg.Dispatch(context.Background(), nil)
	if resp == nil || resp.Error == nil {
		t.Fatal("nil 请求应返回错误响应而不是 nil/panic")
	}
}

func TestListNormalizesSummary(t *testing.T) {
	rt := &fakeRuntime{containers: []runtime.Container{
		{
			ID: "0123456789abcdef0123456789abcdef", Name: "web", Image: "nginx:1.27",
			State: "running", Status: "Up 1h", CreatedUnix: 1700000000,
			Ports: []runtime.Port{{IP: "0.0.0.0", Private: 80, Public: 8080, Type: "tcp"}},
		},
		{
			ID: "short", Name: "db", Image: "postgres:16", State: "exited",
			Ports: []runtime.Port{{Private: 5432, Type: "tcp"}},
		},
	}}
	reg := newTestRegistry(rt)
	resp := request(t, reg, envelope.LocalMethodContainerList, nil)
	if resp.Error != nil {
		t.Fatalf("列表失败: %s", resp.Error.Message)
	}

	var res envelope.ContainerListResult
	if err := envelope.DecodePayload(resp, &res); err != nil {
		t.Fatal(err)
	}
	if len(res.Containers) != 2 {
		t.Fatalf("容器数 = %d", len(res.Containers))
	}
	// 短 ID 统一为 12 位，与 docker CLI 习惯一致。
	if res.Containers[0].ID != "0123456789ab" {
		t.Fatalf("短 ID = %q", res.Containers[0].ID)
	}
	if res.Containers[0].Ports != "0.0.0.0:8080->80/tcp" {
		t.Fatalf("端口摘要 = %q", res.Containers[0].Ports)
	}
	// 未映射端口只显示容器内端口。
	if res.Containers[1].Ports != "5432/tcp" {
		t.Fatalf("未映射端口摘要 = %q", res.Containers[1].Ports)
	}
	// 长度不足的 ID 原样返回，不 panic。
	if res.Containers[1].ID != "short" {
		t.Fatalf("短 ID 原样返回失败: %q", res.Containers[1].ID)
	}
	// 首期未登记归属的容器一律按 discovered 处理。
	if res.Containers[0].Origin != originDiscovered {
		t.Fatalf("Origin = %q，期望 discovered", res.Containers[0].Origin)
	}
}

func TestListRuntimeNoneIsUnsupported(t *testing.T) {
	reg := newTestRegistry(runtime.NewNone(""))
	resp := request(t, reg, envelope.LocalMethodContainerList, nil)
	// 无运行时下列表返回空（不是错误），这是有意的：Hub 应该看到"0 个容器"。
	if resp.Error != nil {
		t.Fatalf("无运行时下列表不应报错: %s", resp.Error.Message)
	}
	var res envelope.ContainerListResult
	if err := envelope.DecodePayload(resp, &res); err != nil {
		t.Fatal(err)
	}
	if len(res.Containers) != 0 {
		t.Fatalf("期望空列表，实际 %d 条", len(res.Containers))
	}

	// 但操作类方法必须回 unsupported，而不是 runtime_failed。
	resp = request(t, reg, envelope.LocalMethodContainerStart, envelope.ContainerOpPayload{ContainerID: "x"})
	if resp.Error == nil || resp.Error.Code != envelope.CodeUnsupported {
		t.Fatalf("无运行时启动容器应返回 unsupported，实际: %+v", resp.Error)
	}
}

func TestStartStopPayloadValidation(t *testing.T) {
	rt := &fakeRuntime{}
	reg := newTestRegistry(rt)

	// 缺载荷。
	resp := request(t, reg, envelope.LocalMethodContainerStart, nil)
	if resp.Error == nil || resp.Error.Code != envelope.CodeBadRequest {
		t.Fatalf("缺载荷应返回 bad_request，实际: %+v", resp.Error)
	}
	// 空容器 ID。
	resp = request(t, reg, envelope.LocalMethodContainerStart, envelope.ContainerOpPayload{ContainerID: "  "})
	if resp.Error == nil || resp.Error.Code != envelope.CodeBadRequest {
		t.Fatalf("空容器 ID 应返回 bad_request，实际: %+v", resp.Error)
	}
}

func TestStartStopSuccess(t *testing.T) {
	rt := &fakeRuntime{containers: []runtime.Container{{ID: "0123456789abcdef", Name: "web", State: "running"}}}
	reg := newTestRegistry(rt)

	resp := request(t, reg, envelope.LocalMethodContainerStart, envelope.ContainerOpPayload{ContainerID: "web"})
	if resp.Error != nil {
		t.Fatalf("启动失败: %s", resp.Error.Message)
	}
	if rt.lastStart != "web" {
		t.Fatalf("Start 收到引用 = %q", rt.lastStart)
	}
	var res envelope.ContainerOpResult
	if err := envelope.DecodePayload(resp, &res); err != nil {
		t.Fatal(err)
	}
	// 操作后应回读最新状态，前端无需再发列表请求。
	if res.State != "running" || res.ContainerID != "0123456789ab" {
		t.Fatalf("操作结果 = %+v", res)
	}

	// force 必须透传到运行时（决定走 stop 还是 kill）。
	resp = request(t, reg, envelope.LocalMethodContainerStop, envelope.ContainerOpPayload{ContainerID: "web", Force: true})
	if resp.Error != nil {
		t.Fatalf("停止失败: %s", resp.Error.Message)
	}
	if !rt.lastForce {
		t.Fatal("force 未透传到运行时")
	}
}

func TestNotFoundTranslation(t *testing.T) {
	rt := &fakeRuntime{startErr: errors.Join(runtime.ErrNotFound, errors.New("no such container"))}
	reg := newTestRegistry(rt)
	resp := request(t, reg, envelope.LocalMethodContainerStart, envelope.ContainerOpPayload{ContainerID: "ghost"})
	if resp.Error == nil || resp.Error.Code != envelope.CodeNotFound {
		t.Fatalf("ErrNotFound 应映射为 not_found，实际: %+v", resp.Error)
	}
}

func TestRuntimeFailureTranslation(t *testing.T) {
	rt := &fakeRuntime{listErr: errors.New("连接运行时失败: permission denied")}
	reg := newTestRegistry(rt)
	resp := request(t, reg, envelope.LocalMethodContainerList, nil)
	if resp.Error == nil || resp.Error.Code != envelope.CodeRuntimeFailed {
		t.Fatalf("引擎报错应映射为 runtime_failed，实际: %+v", resp.Error)
	}
}

func TestLogsClampTailLines(t *testing.T) {
	rt := &fakeRuntime{logsRes: runtime.LogResult{Lines: []string{"a", "b"}}}
	reg := newTestRegistry(rt)

	// 负值应回落到默认 200，而不是"全量拉取"。
	resp := request(t, reg, envelope.LocalMethodContainerLogs, envelope.ContainerLogsPayload{ContainerID: "web", TailLines: -5})
	if resp.Error != nil {
		t.Fatalf("日志失败: %s", resp.Error.Message)
	}
	if rt.lastLogsOpts.TailLines != defaultLogLines {
		t.Fatalf("TailLines = %d，期望默认 %d", rt.lastLogsOpts.TailLines, defaultLogLines)
	}
	if !rt.lastLogsOpts.Stdout || !rt.lastLogsOpts.Stderr {
		t.Fatal("默认应同时拉取 stdout 与 stderr")
	}

	// 超过上限必须被裁剪，并在结果里标记 Truncated。
	resp = request(t, reg, envelope.LocalMethodContainerLogs, envelope.ContainerLogsPayload{ContainerID: "web", TailLines: 999999})
	if rt.lastLogsOpts.TailLines != maxLogLines {
		t.Fatalf("TailLines = %d，期望被夹到 %d", rt.lastLogsOpts.TailLines, maxLogLines)
	}
	var res envelope.ContainerLogsResult
	if err := envelope.DecodePayload(resp, &res); err != nil {
		t.Fatal(err)
	}
	if !res.Truncated {
		t.Fatal("裁剪到上限时应标记 Truncated")
	}
}

func TestLogsEmptyReturnsArray(t *testing.T) {
	rt := &fakeRuntime{logsRes: runtime.LogResult{}}
	reg := newTestRegistry(rt)
	resp := request(t, reg, envelope.LocalMethodContainerLogs, envelope.ContainerLogsPayload{ContainerID: "web"})
	if string(resp.Payload) == "null" {
		t.Fatal("空日志应返回空数组而不是 null")
	}
	var res envelope.ContainerLogsResult
	if err := envelope.DecodePayload(resp, &res); err != nil {
		t.Fatal(err)
	}
	if res.Lines == nil {
		t.Fatal("Lines 应为非 nil 空切片")
	}
}

func TestRuntimeInfoPayload(t *testing.T) {
	rt := &fakeRuntime{info: runtime.Info{
		Kind: runtime.KindPodman, Version: "5.2.0", APIVer: "1.41",
		Endpoint: "unix:///run/user/1000/podman/podman.sock", Rootless: true,
	}}
	reg := newTestRegistry(rt)
	resp := request(t, reg, envelope.LocalMethodRuntimeInfo, nil)
	if resp.Error != nil {
		t.Fatalf("runtime.info 失败: %s", resp.Error.Message)
	}
	var info envelope.RuntimeInfo
	if err := envelope.DecodePayload(resp, &info); err != nil {
		t.Fatal(err)
	}
	if info.Kind != envelope.RuntimePodman || !info.Rootless {
		t.Fatalf("运行时信息 = %+v", info)
	}
}

func TestRegistryMethodsSorted(t *testing.T) {
	reg := newTestRegistry(&fakeRuntime{})
	methods := reg.Methods()
	// 本测试的注册表不挂编排（composeMgr 为 nil），因此只应包含
	// 容器 7 个 + 资源 6 个 = 13 个方法。
	// 编排方法数由 compose 专项测试验证，这里固定数字是为了让
	// "悄悄多注册/漏注册一个方法"能被立刻发现。
	const wantCount = 13
	if len(methods) != wantCount {
		t.Fatalf("注册方法数 = %d，期望 %d: %v", len(methods), wantCount, methods)
	}
	for i := 1; i < len(methods); i++ {
		if methods[i-1] > methods[i] {
			t.Fatalf("方法名未排序: %v", methods)
		}
	}
	for _, want := range []string{
		envelope.MethodRuntimeInfo,
		envelope.LocalMethodContainerList,
		envelope.LocalMethodContainerStart,
		envelope.LocalMethodContainerStop,
		envelope.LocalMethodContainerRestart,
		envelope.LocalMethodContainerRemove,
		envelope.LocalMethodContainerLogs,
		envelope.MethodImageList,
		envelope.MethodImageRemove,
		envelope.MethodNetworkList,
		envelope.MethodNetworkRemove,
		envelope.MethodVolumeList,
		envelope.MethodVolumeRemove,
	} {
		if !reg.Has(want) {
			t.Fatalf("缺少方法 %s", want)
		}
	}
}

// TestLocalAndRemoteShareHandlers 验证"本地 CLI 与在线通道共用一套处理器"。
//
// 做法：同一个注册表，一次用本地风格的方法名常量调用，一次用在线方法名调用，
// 响应载荷必须完全一致——这正是决策 D-05 要求的"不存在两套实现"。
func TestLocalAndRemoteShareHandlers(t *testing.T) {
	rt := &fakeRuntime{containers: []runtime.Container{{ID: "aaaaaaaaaaaa", Name: "web", State: "running"}}}
	reg := newTestRegistry(rt)

	localResp := request(t, reg, envelope.LocalMethodContainerList, nil)
	// 直接使用协议里的在线方法名，模拟 Hub 下发。
	remoteResp := request(t, reg, envelope.MethodContainerList, nil)

	lb, _ := json.Marshal(localResp.Payload)
	rb, _ := json.Marshal(remoteResp.Payload)
	if string(lb) != string(rb) {
		t.Fatalf("本地与在线响应不一致:\n本地: %s\n在线: %s", lb, rb)
	}
}
