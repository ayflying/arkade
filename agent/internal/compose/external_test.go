package compose

// external_test.go 覆盖从引擎标签发现外部编排项目。
//
// 背景是真实故障：用户反馈「容器编排怎么是空的，这台设备应该有不少编排」。
// 根因是列表只枚举 Arkade 工作区目录，完全没看引擎里已有的
// 5 个生产项目（act-runner / adinsight-db-lab / arkade-demo / lanet / portainer-agent）。
//
// 测试重点：
//   - 外部项目能被发现，且状态数量正确；
//   - 已停止的项目也必须出现（否则用户以为项目消失了）；
//   - compose run 的一次性容器不算服务；
//   - **引擎出错时必须返回错误，不能假装"没有项目"**。

import (
	"context"
	"errors"
	"sort"
	"testing"
)

// fakeLabels 是可控的容器标签源。
type fakeLabels struct {
	items []ContainerLabels
	err   error
}

func (f *fakeLabels) ListContainerLabels(context.Context) ([]ContainerLabels, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.items, nil
}

// composeC 构造一个 compose 容器的标签集合。
func composeC(project, service, state string) ContainerLabels {
	return ContainerLabels{
		State: state,
		Labels: map[string]string{
			labelComposeProject: project,
			labelComposeService: service,
		},
	}
}

// TestDiscoverExternalProjects 验证多个项目被发现且统计正确。
func TestDiscoverExternalProjects(t *testing.T) {
	src := &fakeLabels{items: []ContainerLabels{
		composeC("act-runner", "runner", stateRunning),
		composeC("act-runner", "db", stateRunning),
		composeC("lanet", "web", stateRunning),
		composeC("lanet", "cache", "exited"),
		composeC("portainer-agent", "agent", stateRunning),
	}}

	got, err := DiscoverExternal(context.Background(), src)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("应发现 3 个项目，实际 %d", len(got))
	}

	// 按名称排序应稳定（Agent 侧已排过序）。
	names := make([]string, 0, len(got))
	for _, p := range got {
		names = append(names, p.Name)
	}
	if !sort.StringsAreSorted(names) {
		t.Errorf("项目应按名称排序，实际 %v", names)
	}

	byName := map[string]ExternalProject{}
	for _, p := range got {
		byName[p.Name] = p
	}

	// 全运行 → running，2/2。
	if p := byName["act-runner"]; p.Status() != "running" || p.Running != 2 || p.Total != 2 {
		t.Errorf("act-runner 状态错误: %+v（status=%s）", p, p.Status())
	}
	// 部分运行 → partial，1/2。这个状态最容易被忽略：
	// 用户需要知道"有个服务挂了"，而不是笼统的"项目存在"。
	if p := byName["lanet"]; p.Status() != "partial" || p.Running != 1 || p.Total != 2 {
		t.Errorf("lanet 状态错误: %+v（status=%s）", p, p.Status())
	}
	// 服务名去重且排序。
	if p := byName["act-runner"]; len(p.Services) != 2 || p.Services[0] != "db" || p.Services[1] != "runner" {
		t.Errorf("act-runner 服务名错误: %v", p.Services)
	}
}

// TestDiscoverExternalStoppedProject 验证全停的项目仍被发现。
//
// 这是最容易做错的一条：若只查运行中的容器，
// 一个刚停掉的项目就会从列表里消失，用户会以为配置丢了。
// 项目仍然存在于机器上，只是没在跑——这正是需要展示的信息。
func TestDiscoverExternalStoppedProject(t *testing.T) {
	src := &fakeLabels{items: []ContainerLabels{
		composeC("adinsight-db-lab", "mysql", "exited"),
		composeC("adinsight-db-lab", "redis", "created"),
	}}

	got, err := DiscoverExternal(context.Background(), src)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("已停止的项目也应被发现，实际 %d 个", len(got))
	}
	p := got[0]
	if p.Name != "adinsight-db-lab" {
		t.Errorf("项目名错误: %q", p.Name)
	}
	if p.Status() != "stopped" {
		t.Errorf("全停时状态应为 stopped，实际 %q", p.Status())
	}
	if p.Running != 0 || p.Total != 2 {
		t.Errorf("应为 0/2，实际 %d/%d", p.Running, p.Total)
	}
}

// TestDiscoverExternalSkipsNonCompose 验证非 compose 容器被跳过。
//
// 手工 docker run 起的容器没有 compose 标签，
// 把它当成"有 1 个服务的项目"会让列表出现一堆假项目。
func TestDiscoverExternalSkipsNonCompose(t *testing.T) {
	src := &fakeLabels{items: []ContainerLabels{
		{State: stateRunning, Labels: map[string]string{"some.other": "label"}},
		{State: stateRunning, Labels: nil},
		composeC("real-project", "app", stateRunning),
	}}

	got, err := DiscoverExternal(context.Background(), src)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Name != "real-project" {
		t.Errorf("应只发现 real-project，实际 %+v", got)
	}
}

// TestDiscoverExternalSkipsOneoff 验证 compose run 的一次性容器不计入服务数。
//
// docker compose run 产生的容器带着项目标签，但它不是"服务"。
// 不排除的话，跑过几次 run 的项目会显示成十几个服务，
// 用户据此会以为配置被改坏了。
func TestDiscoverExternalSkipsOneoff(t *testing.T) {
	oneoff := ContainerLabels{
		State: stateRunning,
		Labels: map[string]string{
			labelComposeProject: "arkade-demo",
			labelComposeService: "web",
			labelComposeOneoff:  "True",
		},
	}
	src := &fakeLabels{items: []ContainerLabels{
		composeC("arkade-demo", "web", stateRunning),
		oneoff,
	}}

	got, err := DiscoverExternal(context.Background(), src)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("应有 1 个项目，实际 %d", len(got))
	}
	if got[0].Total != 1 {
		t.Errorf("一次性容器不应计入总数，实际 total=%d", got[0].Total)
	}
	if got[0].Services[0] != "web" || len(got[0].Services) != 1 {
		t.Errorf("服务名应只有 web，实际 %v", got[0].Services)
	}
}

// TestDiscoverExternalMissingServiceLabel 验证服务名缺失时不占名额。
func TestDiscoverExternalMissingServiceLabel(t *testing.T) {
	src := &fakeLabels{items: []ContainerLabels{{
		State:  stateRunning,
		Labels: map[string]string{labelComposeProject: "half-labeled"},
	}}}

	got, err := DiscoverExternal(context.Background(), src)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("应发现 1 个项目，实际 %d", len(got))
	}
	if len(got[0].Services) != 0 {
		t.Errorf("服务名缺失时不应编造，实际 %v", got[0].Services)
	}
	if got[0].Total != 1 {
		t.Errorf("容器本身仍应计入，实际 total=%d", got[0].Total)
	}
}

// TestDiscoverExternalEngineError 是本组最重要的一条。
//
// **引擎出错时必须返回错误，绝不能返回空列表假装"没有项目"。**
//
// 理由正是本次 bug 的同类错误：用户看到空列表，以为机器上没有编排，
// 而真相是探测链路出了问题。空列表是一个**断言**（"确实没有"），
// 我们没资格在不知道的时候做出这个断言。
//
// 上层（handler）会把这个错误降级为"只返回已纳管项目"并记日志，
// 那是有意的取舍；但发现函数本身必须如实报告失败。
func TestDiscoverExternalEngineError(t *testing.T) {
	src := &fakeLabels{err: errors.New("连接 docker.sock 失败")}

	got, err := DiscoverExternal(context.Background(), src)
	if err == nil {
		t.Fatal("引擎出错时应返回错误，不能假装没有项目")
	}
	if got != nil {
		t.Errorf("出错时不应返回任何项目，实际 %+v", got)
	}
}

// TestDiscoverExternalEmptyIsNotError 验证"真的没有项目"与"出错"是两回事。
func TestDiscoverExternalEmptyIsNotError(t *testing.T) {
	src := &fakeLabels{items: nil}

	got, err := DiscoverExternal(context.Background(), src)
	if err != nil {
		t.Fatalf("没有外部项目不是错误: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("应为空，实际 %d", len(got))
	}
}
