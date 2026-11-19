//go:build integration

package runtime

// helper_reaper_integration_test.go：在**真实** Docker 守护进程上验证回收链路。
//
// 默认不参与构建（integration 构建标签），因为会删真实容器，且需要一个
// 可用的套接字。单元测试用的是手工构造的 Container 结构，证明不了两件真事：
//
//  1. 真实引擎的容器列表里到底有没有 Labels 与 Created 字段
//     （回收判据 100% 依赖这两个，字段缺失就会永远不收）；
//  2. 按完整 ID 删除在真实守护进程上是否成功。
//
// 手动跑法（先准备好"已超过 10 分钟"的残留容器）：
//
//	ARKADE_TEST_DOCKER_SOCKET=/var/run/docker.sock \
//	ARKADE_TEST_REAP_EXPECT_GONE=arkade-helper-x,arkade-vfh-y \
//	ARKADE_TEST_REAP_EXPECT_KEEP=arkade-helper-decoy \
//	go test -tags integration ./internal/runtime/ -run TestReapStaleHelpersRealDaemon -v

import (
	"context"
	"os"
	"strings"
	"testing"
)

func splitList(v string) []string {
	var out []string
	for _, part := range strings.Split(v, ",") {
		if s := strings.TrimSpace(part); s != "" {
			out = append(out, s)
		}
	}
	return out
}

// hasContainerName 判断列表里是否还有名字等于 name 的容器。
// 引擎返回的 Name 不带前导斜杠（由 toContainer 统一裁剪），这里兼容两种情况。
func hasContainerName(list []Container, name string) bool {
	for _, c := range list {
		if strings.TrimPrefix(c.Name, "/") == name {
			return true
		}
	}
	return false
}

func TestReapStaleHelpersRealDaemon(t *testing.T) {
	sock := strings.TrimSpace(os.Getenv("ARKADE_TEST_DOCKER_SOCKET"))
	expectGone := splitList(os.Getenv("ARKADE_TEST_REAP_EXPECT_GONE"))
	if sock == "" || len(expectGone) == 0 {
		t.Skip("未提供 ARKADE_TEST_DOCKER_SOCKET / ARKADE_TEST_REAP_EXPECT_GONE，跳过真实守护进程验证")
	}
	expectKeep := splitList(os.Getenv("ARKADE_TEST_REAP_EXPECT_KEEP"))

	ctx := context.Background()
	rt, err := probe(ctx, "unix://"+sock, false)
	if err != nil {
		t.Fatalf("探测真实运行时失败: %v", err)
	}
	e, ok := rt.(*engine)
	if !ok {
		t.Fatalf("端点未解析为 Docker 引擎: %T", rt)
	}

	before, err := e.List(ctx, true)
	if err != nil {
		t.Fatalf("列出容器失败: %v", err)
	}
	// 前提校验：要删的容器必须真的在，否则后面的"已消失"毫无意义。
	for _, name := range expectGone {
		if !hasContainerName(before, name) {
			t.Fatalf("前置条件不满足：%s 不在容器列表里，无法验证删除", name)
		}
	}

	removed, err := e.ReapStaleHelpers(ctx)
	if err != nil {
		t.Fatalf("回收过程报错: %v", err)
	}
	t.Logf("本轮回收到 %d 个残留 helper 容器（期望至少 %d 个）", removed, len(expectGone))
	if removed < len(expectGone) {
		t.Fatalf("回收数量不足：%d < %d", removed, len(expectGone))
	}

	after, err := e.List(ctx, true)
	if err != nil {
		t.Fatalf("回收后列出容器失败: %v", err)
	}
	for _, name := range expectGone {
		if hasContainerName(after, name) {
			t.Errorf("%s 应已被回收，但仍在列表里", name)
		}
	}
	// 安全性：名字像 helper 但没有 io.arkade.role 标签的容器必须原样保留。
	// 这是"不能靠名字乱删别人容器"的底线。
	for _, name := range expectKeep {
		if !hasContainerName(after, name) {
			t.Errorf("%s 不带 Arkade 标签，绝不应被回收", name)
		}
	}

	// 幂等：再跑一轮不应重复删除，也不应报错。
	again, err := e.ReapStaleHelpers(ctx)
	if err != nil {
		t.Fatalf("第二轮回收报错: %v", err)
	}
	if again != 0 {
		t.Errorf("残留已清空，第二轮不应再删除任何容器，实际 %d", again)
	}
}
