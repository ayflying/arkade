package embedded

// helper_reaper_wiring_test.go：验证内嵌执行器会启动残留 helper 回收器。
//
// 为什么单独钉这一条：泄漏现场恰恰是"Hub 内嵌本机执行器"这种部署，
// 回收器如果只在独立 Agent 的启动路径里跑，Hub 自己产生的残留就永远
// 没人收。这类"少接一根线"的问题编译能过、单测别处也全绿，
// 只能靠显式断言"它被调了"来兜住。

import (
	"context"
	"sync"
	"testing"
	"time"

	"arkade/agent/internal/runtime"
)

// reapSpy 只关心 RunHelperReaper 是否被调用。
// 其余方法全部委托给 None 运行时，避免为了一个接线测试去实现整个接口。
type reapSpy struct {
	runtime.Runtime
	called chan struct{}
	once   sync.Once
}

func (s *reapSpy) RunHelperReaper(ctx context.Context, _ func(int, error)) {
	s.once.Do(func() { close(s.called) })
	// 与真实实现一样阻塞到根上下文取消：Close 会等这个 goroutine 退出，
	// 提前返回会让 Close 的等待变得没有意义（也就测不出那个等待）。
	<-ctx.Done()
}

func TestEmbeddedExecutorStartsHelperReaper(t *testing.T) {
	dir := t.TempDir()
	spy := &reapSpy{Runtime: runtime.NewNone(""), called: make(chan struct{})}

	e, err := newWithRuntime(context.Background(), Options{DataDir: dir}, spy, dir, nil)
	if err != nil {
		t.Fatalf("装配内嵌执行器失败: %v", err)
	}
	closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	defer func() { _ = e.Close(closeCtx) }()

	select {
	case <-spy.called:
	case <-time.After(3 * time.Second):
		t.Fatal("内嵌执行器没有启动残留 helper 回收器：Hub 自己产生的残留将无人回收")
	}
}

// TestEmbeddedCloseWaitsReaper 确认 Close 会等回收 goroutine 退出。
//
// 不等就会留下"Close 返回后仍在访问运行时"的 goroutine，
// 在进程退出/热重启时表现为偶发的删除失败或日志乱序。
func TestEmbeddedCloseWaitsReaper(t *testing.T) {
	dir := t.TempDir()
	spy := &reapSpy{Runtime: runtime.NewNone(""), called: make(chan struct{})}

	e, err := newWithRuntime(context.Background(), Options{DataDir: dir}, spy, dir, nil)
	if err != nil {
		t.Fatalf("装配内嵌执行器失败: %v", err)
	}
	select {
	case <-spy.called:
	case <-time.After(3 * time.Second):
		t.Fatal("回收器未启动，无法验证 Close 等待行为")
	}

	done := make(chan error, 1)
	go func() { done <- e.Close(context.Background()) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Close 返回错误: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Close 被回收 goroutine 卡住：应该随根上下文取消立即退出")
	}
}
