package agentlink

// agentlink_test.go 守住请求超时按方法分档的规则。
//
// 为什么值得单独测：这个规则出错的后果非常隐蔽——Hub 超时报错、Agent 却
// 正常执行并最终成功，用户看到"失败"但服务其实起来了。日志里只有一句
// timeout，很难联想到是超时常量的问题。这里把「慢操作必须比默认值长」
// 钉死，任何人想改回去都会先看到测试失败。

import (
	"testing"
	"time"

	"arkade/protocol/envelope"
)

// TestSlowMethodTimeoutsExceedDefault 确认所有慢操作都拿到了更长的超时。
func TestSlowMethodTimeoutsExceedDefault(t *testing.T) {
	slow := []string{
		envelope.MethodComposeUp,
		envelope.MethodComposePull,
		envelope.MethodComposeRebuild,
		envelope.MethodComposeDown,
		envelope.MethodComposeRemove,
		envelope.MethodComposeBackup,
	}
	for _, m := range slow {
		got := requestTimeoutFor(m)
		if got <= DefaultRequestTimeout {
			t.Errorf("%s 的超时 %v 不大于默认值 %v，拉镜像时必然误报超时", m, got, DefaultRequestTimeout)
		}
	}
}

// TestSnapshotAndQuickMethodsUseDefault 确认快操作没被误放宽。
//
// 若有人图省事把 map 改成「所有方法都给 10 分钟」，真正的卡死会拖很久才报错，
// 这个断言就是为了拦住那种偷懒改法。
func TestSnapshotAndQuickMethodsUseDefault(t *testing.T) {
	quick := []string{
		envelope.MethodContainerList,
		envelope.MethodContainerStart,
		envelope.MethodContainerStop,
		envelope.MethodContainerLogs,
		envelope.MethodComposePS,
		envelope.MethodComposeList,
		envelope.MethodImageList,
		envelope.MethodNetworkList,
		envelope.MethodVolumeList,
	}
	for _, m := range quick {
		if got := requestTimeoutFor(m); got != DefaultRequestTimeout {
			t.Errorf("%s 应使用默认超时 %v，实际 %v", m, DefaultRequestTimeout, got)
		}
	}
}

// TestComposeTimeoutsCoverAgentSide 确认 Hub 的等待上限大于 Agent 侧命令超时。
//
// 顺序很重要：必须让 Agent 先把明确的错误（YAML 写错、端口冲突）报回来，
// 而不是 Hub 先超时。反过来的话用户永远只能看到"超时"，看不到真正原因。
func TestComposeTimeoutsCoverAgentSide(t *testing.T) {
	// Agent 侧 pull/up 的命令超时是 10 分钟（见 agent/internal/compose）。
	const agentCommandTimeout = 10 * time.Minute
	if requestTimeoutFor(envelope.MethodComposeUp) <= agentCommandTimeout {
		t.Errorf("Hub 的 compose.up 超时 %v 必须大于 Agent 侧命令超时 %v",
			requestTimeoutFor(envelope.MethodComposeUp), agentCommandTimeout)
	}
}
