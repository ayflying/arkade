package httpx

// volume_cancel_test.go：验证「浏览器断开 → Hub 取消 Agent 调用」这条链路真的通。
//
// ## 为什么要从 HTTP 层测，而不是只测 Registry.Call
//
// 这条修复依赖三段的接缝：
//   1. GoFrame 把**请求上下文**交给控制器（而不是一个永不结束的 ctx）；
//   2. 控制器把它透传给 Ops.VolumeFiles；
//   3. Registry.Call 在放弃等待时通知 Agent。
// 只测第 3 段会漏掉最致命的一种情况：如果第 1 段传下来的是背景 ctx，
// 浏览器断开后 Hub 会一直等到自己 75 秒超时，槽位照样被占。
//
// 这里用**本机执行器**当 Agent 替身：它的入参 ctx 正是整条链路传下来的，
// 因此"执行器看到 ctx 结束"就等于"客户端断开传到了 Agent 侧"。

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"arkade/protocol/envelope"
)

// localCancelledExecutor 是"一直在干活、只听 ctx 取消"的执行器替身。
type localCancelledExecutor func(ctx context.Context) ([]byte, *envelope.Error)

func (fn localCancelledExecutor) Call(ctx context.Context, _ string, _ []byte) ([]byte, *envelope.Error) {
	return fn(ctx)
}

func TestVolumeFilesClientDisconnectCancelsAgentCall(t *testing.T) {
	e := newTestEnv(t)
	const agentID = "vol-cancel-agent"
	e.seedAgent(t, agentID)

	var once sync.Once
	entered := make(chan struct{})
	cancelled := make(chan struct{})
	if err := e.registry.RegisterLocal(agentID, localCancelledExecutor(func(ctx context.Context) ([]byte, *envelope.Error) {
		once.Do(func() { close(entered) })
		<-ctx.Done()
		close(cancelled)
		return nil, envelope.NewError(envelope.CodeRuntimeFailed, "已取消")
	})); err != nil {
		t.Fatalf("注册本机执行器失败: %v", err)
	}

	cookies := e.login(t)
	ctx, cancel := context.WithCancel(newTestContext())
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		e.wsURL+"/api/agents/"+agentID+"/volume-files",
		strings.NewReader(`{"volume":"test-volume","operation":"list","path":""}`))
	if err != nil {
		t.Fatalf("构造请求失败: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	for _, c := range cookies {
		req.AddCookie(c)
	}

	go func() {
		resp, err := http.DefaultClient.Do(req)
		if err == nil {
			_ = resp.Body.Close()
		}
	}()

	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("请求没有进入 Agent 调用")
	}

	// 模拟用户关掉面板/切走页面：浏览器断开，客户端 ctx 结束
	cancel()
	select {
	case <-cancelled:
	case <-time.After(5 * time.Second):
		t.Fatal("客户端断开后 Hub 没有取消 Agent 调用（会继续占满并发槽位）")
	}
}
