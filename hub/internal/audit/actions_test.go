package audit

// actions_test.go 覆盖动作常量与中文名映射的一致性。
//
// 最关键的一条：每个导出的动作常量都必须在 labels 表里。
// 加常量忘加映射是这类改动最常见的疏漏，后果是审计列表上
// 直接显示 `container.remove` 这样的原始标识——而且不会有任何报错。

import (
	"strconv"
	"strings"
	"testing"
)

// TestAllActionsHaveLabels 确保每个动作都有中文名。
func TestAllActionsHaveLabels(t *testing.T) {
	// 显式列出全部常量而不是反射扫描：
	// 反射拿不到 map 之外的"应该存在"清单，
	// 而这里的用意正是"新增常量时必须来加一行"，漏了会编译不过或此测试失败。
	all := []string{
		ActionContainerStart, ActionContainerStop, ActionContainerRestart, ActionContainerRemove,
		ActionStackCreate, ActionStackUpdate, ActionStackDelete, ActionStackAction,
		ActionImageRemove, ActionNetworkRemove, ActionVolumeRemove,
		ActionAgentUpdate, ActionAgentDelete, ActionAgentDisable, ActionAgentEnable,
		ActionUserCreate, ActionUserUpdate, ActionUserDelete, ActionUserLogin, ActionUserLogout,
		ActionGroupCreate, ActionGroupUpdate, ActionGroupDelete,
	}
	for _, a := range all {
		if !Known(a) {
			t.Errorf("动作 %q 缺少中文名映射", a)
		}
		if label := ActionLabel(a); label == "" {
			t.Errorf("动作 %q 的中文名为空", a)
		}
	}
}

// TestLabelsMatchAll 确保 labels 表里没有多余的键。
//
// 反向检查：如果 labels 里有某个动作但常量已删除，
// 说明映射表成了孤儿数据，会误导后来人以为那个动作还在用。
func TestLabelsMatchAll(t *testing.T) {
	if got, want := len(All()), len(labels); got != want {
		t.Errorf("All() 返回 %d 条，labels 有 %d 条", got, want)
	}
}

// TestActionNamingConvention 验证动作标识遵循 `域.动作` 格式。
//
// 格式不统一会让前端做筛选分组、让报表按域聚合都变得困难，
// 而且这种问题一旦写进历史数据就改不回来了。
func TestActionNamingConvention(t *testing.T) {
	for _, a := range All() {
		if !strings.Contains(a, ".") {
			t.Errorf("动作 %q 不符合 `域.动作` 格式", a)
		}
		parts := strings.Split(a, ".")
		if len(parts) != 2 {
			t.Errorf("动作 %q 应只有一段点号", a)
		}
		for _, p := range parts {
			if p == "" {
				t.Errorf("动作 %q 有空的域名或动作名", a)
			}
			if p != strings.ToLower(p) {
				t.Errorf("动作 %q 应全小写", a)
			}
		}
	}
}

// TestActionLabelUnknown 验证未登记动作原样返回。
//
// 这是刻意的：审计列表上出现 unknown.POST 这样的原始值是
// "这里有新接口还没补映射"的信号，一眼可见。
// 若统一显示成「未知操作」，反而把这个信号藏起来了。
func TestActionLabelUnknown(t *testing.T) {
	got := ActionLabel("unknown.POST")
	if got != "unknown.POST" {
		t.Errorf("未登记动作应原样返回，实际 %q", got)
	}
}

// TestUnknown 验证 unknown 动作的生成格式。
//
// 保留方法名便于分辨（同一个漏映射路径的 POST 和 DELETE 是两件事）。
func TestUnknown(t *testing.T) {
	if got := Unknown("POST"); got != "unknown.POST" {
		t.Errorf("应为 unknown.POST，实际 %q", got)
	}
	if got := Unknown("DELETE"); got != "unknown.DELETE" {
		t.Errorf("应为 unknown.DELETE，实际 %q", got)
	}
}

// TestResultConstants 验证结果常量取值稳定。
//
// 这两个字符串会写进数据库，改动等于让历史记录无法被新代码识别。
func TestResultConstants(t *testing.T) {
	if ResultSuccess != "success" {
		t.Errorf("ResultSuccess 应为 success，实际 %q", ResultSuccess)
	}
	if ResultFailed != "failed" {
		t.Errorf("ResultFailed 应为 failed，实际 %q", ResultFailed)
	}
}

// TestNewID 验证 ID 生成。
func TestNewID(t *testing.T) {
	a := NewID()
	b := NewID()
	if a == b {
		t.Error("两次生成的 ID 不应相同")
	}
	if len(a) != IDBytes*2 {
		t.Errorf("ID 长度应为 %d，实际 %d", IDBytes*2, len(a))
	}
	// 不应是纯数字：那说明退化成了某种计数器，并发时会撞号。
	if _, err := strconv.ParseInt(a, 10, 64); err == nil {
		t.Error("ID 不应是纯数字")
	}
}
