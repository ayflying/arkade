package compose

// guard_test.go 覆盖「不许按同名接管外部项目」这道安全闸门。
//
// 这是本任务里最危险的一处，值得单独一组测试。
//
// 危险路径：外部生产项目（act-runner 等）已在引擎里跑着，但 Arkade
// 工作区里没有它的目录。用户在界面上新建一个同名编排，`docker compose up -d`
// 会按项目名匹配到**已有的那些容器**，用新配置重建它们。
// 也就是说：只要名字撞上，一次"新建"就能悄悄改掉生产负载。
//
// 列表只读拦不住这件事——列表只读挡不住用户手工输入相同名字。
// 判定必须落在真正执行 up 的那一层。

import (
	"context"
	"strings"
	"testing"
)

// newTestManager 构造指向临时目录的 Manager。
//
// 返回的 Runner 指向一个不存在的 docker 地址：测试只关心
// **闸门判定与落盘顺序**，不真的执行 compose 命令。
// 这样测试不依赖机器上是否装了 docker compose。
func newTestManager(t *testing.T) (*Manager, string) {
	t.Helper()
	dir := t.TempDir()
	return NewManager(dir, "unix:///nonexistent-test.sock"), dir
}

// TestApplyRejectsExternalNameCollision 是核心安全测试。
//
// 工作区无目录 + 引擎有同名项目 → 必须拒绝，且错误信息要说清怎么办。
func TestApplyRejectsExternalNameCollision(t *testing.T) {
	m, _ := newTestManager(t)
	// 引擎里已有 act-runner（外部项目），工作区里没有它的目录。
	m.SetLabelSource(&fakeLabels{items: []ContainerLabels{
		composeC("act-runner", "runner", stateRunning),
	}})

	_, err := m.ApplyProject(context.Background(), ProjectSpec{Name: "act-runner", Content: "services:\n  web:\n    image: nginx\n"})
	if err == nil {
		t.Fatal("与外部项目同名时必须拒绝，否则会重建生产容器")
	}
	msg := err.Error()
	// 错误文案必须让用户知道发生了什么、该怎么办。
	if !strings.Contains(msg, "act-runner") {
		t.Errorf("错误应指出具体项目名，实际 %q", msg)
	}
	if !strings.Contains(msg, "不是由 Arkade 创建") {
		t.Errorf("错误应说明该项目不属于 Arkade，实际 %q", msg)
	}
	// 给出可执行的出路：改名，或走导入（当前不支持）。
	if !strings.Contains(msg, "换一个编排名称") {
		t.Errorf("错误应给出改名的建议，实际 %q", msg)
	}
}

// TestApplyAllowsWhenWorkspaceExists 验证自己的项目不会被误拦。
//
// 工作区里已有目录 → 这是 Arkade 自己管理的项目，正常更新。
// 若这条失败，说明闸门判定条件写错了（只看引擎有没有同名），
// 那会导致所有已存在项目都无法更新——功能性坏死。
func TestApplyAllowsWhenWorkspaceExists(t *testing.T) {
	m, _ := newTestManager(t)
	// 先正常创建（此时引擎里还没有，或探测源缺失）。
	if _, err := m.ws.Write("mine", "services:\n  web:\n    image: nginx\n"); err != nil {
		t.Fatal(err)
	}
	// 引擎里现在也有 mine（就是我们自己起的）。
	m.SetLabelSource(&fakeLabels{items: []ContainerLabels{
		composeC("mine", "web", stateRunning),
	}})

	// 这次不该被闸门拒绝。它可能在后续 run 阶段失败
	// （测试环境里没有 compose 二进制），但那不是闸门的问题——
	// 所以只断言错误里不含"同名"提示。
	_, err := m.ApplyProject(context.Background(), ProjectSpec{Name: "mine", Content: "services:\n  web:\n    image: nginx\n"})
	if err != nil && strings.Contains(err.Error(), "不是由 Arkade 创建") {
		t.Errorf("自己管理的项目被安全闸门误拦了: %v", err)
	}
}

// TestApplyRejectsWhenProbeFails 验证探测失败时保守拒绝。
//
// 无法确认名字是否安全时，宁可拒绝一次新建，也不要冒险改动生产容器。
// 前者用户重试即可；后者不可逆。
func TestApplyRejectsWhenProbeFails(t *testing.T) {
	m, _ := newTestManager(t)
	m.SetLabelSource(&fakeLabels{err: context.DeadlineExceeded})

	_, err := m.ApplyProject(context.Background(), ProjectSpec{Name: "whatever", Content: "services: {}\n"})
	if err == nil {
		t.Fatal("无法确认安全性时应保守拒绝，实际放行了")
	}
}

// TestApplyNoProbeSourceKeepsOldBehavior 验证未注入探测源时不改变既有行为。
//
// 单元测试里构造的裸 Manager 没有引擎，让它保持"不检查"，
// 否则每个既有测试都要构造一个假的引擎才能跑。
func TestApplyNoProbeSourceKeepsOldBehavior(t *testing.T) {
	m, _ := newTestManager(t)
	// 不注入 labelSource。
	_, err := m.ApplyProject(context.Background(), ProjectSpec{Name: "fresh", Content: "services:\n  web:\n    image: nginx\n"})
	if err != nil && strings.Contains(err.Error(), "不是由 Arkade 创建") {
		t.Errorf("未注入探测源时不应触发安全闸门: %v", err)
	}
}

// TestApplyRejectsBeforeWritingFile 验证拒绝发生在落盘之前。
//
// 顺序很重要：如果先写了文件再报错，工作区里会留下一个
// 与外部项目同名的目录。下次用户再试同名时，"工作区已存在"这个条件
// 会放行——闸门就被自己刚才的失败操作绕过了。
func TestApplyRejectsBeforeWritingFile(t *testing.T) {
	m, dir := newTestManager(t)
	m.SetLabelSource(&fakeLabels{items: []ContainerLabels{
		composeC("prod-db", "mysql", stateRunning),
	}})

	if _, err := m.ApplyProject(context.Background(), ProjectSpec{Name: "prod-db", Content: "services: {}\n"}); err == nil {
		t.Fatal("应拒绝")
	}
	// 工作区里不应留下同名目录/文件。
	if m.ws.Exists("prod-db") {
		t.Error("被拒绝的操作不应在工作区留下文件，否则下次会被安全闸门放行")
	}
	_ = dir
}
