package compose

// pulltask_lifecycle_test.go：超时、串行化与生命周期取消。
//
// 这三类是"异步任务"最容易漏测、也最容易在线上出事的部分：
//   - 超时若只覆盖执行不覆盖等锁，一个排队中的任务会把总耗时翻倍；
//   - 同项目不串行会让 pull 与 up 交错，最终"按哪份配置重建"无人说得清；
//   - Agent 退出若不取消，compose 子进程会变成孤儿继续拉镜像。
//
// 超时用注入的短预算验证（默认 10 分钟，真等不现实）。

import (
	"context"
	"fmt"
	"runtime"
	"strings"
	"testing"
	"time"
)

// TestPullTimeout 验证任务超时后进入 failed 且给出明确文案。
func TestPullTimeout(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("假 docker 是 shell 脚本，仅在 Linux 上运行")
	}
	// 命令睡很远，必然被超时打断。
	script := writeScript(t, "echo started\nsleep 30\necho never\n")
	_, p := newPullTestManager(t, script)
	p.timeout = 300 * time.Millisecond

	begin := time.Now()
	snap, err := p.StartPull("demo", "timeout-request")
	if err != nil {
		t.Fatal(err)
	}
	final := waitTerminal(t, p, snap.TaskID)
	elapsed := time.Since(begin)

	if final.Status != PullFailed {
		t.Fatalf("超时应失败，实际 %q", final.Status)
	}
	if !strings.Contains(final.Error, "超时") {
		t.Fatalf("应给出超时文案，实际 %q", final.Error)
	}
	// 必须在超时预算附近返回，而不是等命令自己睡完。
	if elapsed > 5*time.Second {
		t.Fatalf("应在超时后很快结束，实际耗时 %v", elapsed)
	}
}

// TestPullTimeoutCoversLockWait 验证超时预算**包含等待项目锁**的阶段。
//
// 若预算只覆盖执行，一个排在长任务后面的 pull 会先等满锁、再跑满命令，
// 总耗时翻倍且用户无从预期。这里让第一个任务长时间持锁，
// 第二个任务的预算很短，它必须因为"等锁"而超时失败。
func TestPullTimeoutCoversLockWait(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("假 docker 是 shell 脚本，仅在 Linux 上运行")
	}
	// 第一个任务持锁睡 3 秒。
	script := writeScript(t, "echo holding\nsleep 3\necho done\n")
	_, p := newPullTestManager(t, script)
	p.timeout = 400 * time.Millisecond

	first, err := p.StartPull("demo", "lock-holder")
	if err != nil {
		t.Fatal(err)
	}
	// 等第一个任务真正开始（已持锁）。
	waitForLine(t, p, first.TaskID, "holding")

	// 第二个任务用同一个项目：它必须排队，并因预算耗尽而失败。
	second, err := p.StartPull("demo", "lock-waiter")
	if err != nil {
		t.Fatal(err)
	}
	finalSecond := waitTerminal(t, p, second.TaskID)
	if finalSecond.Status != PullFailed {
		t.Fatalf("等锁超时的任务应失败，实际 %q", finalSecond.Status)
	}
	if finalSecond.ExitCode == nil || *finalSecond.ExitCode != -1 {
		t.Fatalf("等锁阶段失败的退出码应为 -1，实际 %v", finalSecond.ExitCode)
	}
}

// TestPullSameProjectSerialized 验证同一项目的两个任务不会并发执行。
//
// 串行化不是性能问题而是正确性问题：pull 与 up 交错会让"按哪份配置重建"
// 变得不可解释，而两个操作都可能报告成功。
func TestPullSameProjectSerialized(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("假 docker 是 shell 脚本，仅在 Linux 上运行")
	}
	// 脚本记录进入/退出时刻，用来检测区间重叠。
	script := writeScript(t,
		"echo enter-$(date +%s%N)\nsleep 0.4\necho exit-$(date +%s%N)\n")
	_, p := newPullTestManager(t, script)

	a, err := p.StartPull("demo", "serial-a-request")
	if err != nil {
		t.Fatal(err)
	}
	b, err := p.StartPull("demo", "serial-b-request")
	if err != nil {
		t.Fatal(err)
	}
	fa := waitTerminal(t, p, a.TaskID)
	fb := waitTerminal(t, p, b.TaskID)

	// 解析各自的 enter/exit 时间戳。
	aIn, aOut := enterExit(t, fa)
	bIn, bOut := enterExit(t, fb)

	// 两个区间不得重叠（允许相等边界）。
	if aIn < bOut && bIn < aOut {
		t.Fatalf("同项目任务并发执行了：A[%d,%d] B[%d,%d]", aIn, aOut, bIn, bOut)
	}
}

// TestPullRootContextCancelStopsCommand 验证 Agent 生命周期取消会终止子进程。
//
// ============================ 为什么这条最重要 ============================
//
// Agent 优雅退出（SIGTERM）时，若任务不跟着取消，compose 子进程会变成
// 孤儿进程继续拉镜像——此时 Agent 已经不在了，没人回收它，
// 而它还在改本机的镜像与磁盘。
func TestPullRootContextCancelStopsCommand(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("假 docker 是 shell 脚本，仅在 Linux 上运行")
	}
	script := writeScript(t, "echo started\nsleep 60\necho never\n")

	rootCtx, cancelRoot := context.WithCancel(context.Background())
	defer cancelRoot()

	m, _ := newPullTestManager(t, script)
	// 用可取消的根 ctx 重新建一个 PullManager，模拟 Agent 主循环。
	p := NewPullManager(m, rootCtx)

	snap, err := p.StartPull("demo", "cancel-request")
	if err != nil {
		t.Fatal(err)
	}
	waitForLine(t, p, snap.TaskID, "started")

	// 模拟 Agent 关闭。
	begin := time.Now()
	cancelRoot()

	final := waitTerminal(t, p, snap.TaskID)
	elapsed := time.Since(begin)

	if final.Status != PullFailed {
		t.Fatalf("取消后应失败，实际 %q", final.Status)
	}
	// 必须很快退出（不是等 sleep 60 结束）。
	if elapsed > 5*time.Second {
		t.Fatalf("取消后应在 1 秒级内退出，实际 %v", elapsed)
	}
}

// TestPullManagerCloseCancelsTasks 验证 Close 取消全部进行中的任务。
//
// 这是 Registry.Close → main defer 链条的实际效果：
// Agent 退出时会走 Close，它必须真的把子进程停掉。
func TestPullManagerCloseCancelsTasks(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("假 docker 是 shell 脚本，仅在 Linux 上运行")
	}
	script := writeScript(t, "echo started\nsleep 60\necho never\n")
	_, p := newPullTestManager(t, script)

	snap, err := p.StartPull("demo", "close-request")
	if err != nil {
		t.Fatal(err)
	}
	waitForLine(t, p, snap.TaskID, "started")

	p.Close()

	begin := time.Now()
	final := waitTerminal(t, p, snap.TaskID)
	if final.Status != PullFailed {
		t.Fatalf("Close 后任务应失败，实际 %q", final.Status)
	}
	if elapsed := time.Since(begin); elapsed > 5*time.Second {
		t.Fatalf("Close 后应快速退出，实际 %v", elapsed)
	}
}

// waitForLine 等待任务输出里出现指定内容。
func waitForLine(t *testing.T, p *PullManager, id, want string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		cur, err := p.GetPull(id, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, ev := range cur.Output {
			if strings.Contains(ev.Text, want) {
				return
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("等待输出 %q 超时", want)
}

// enterExit 从输出里解析 enter-<ns> / exit-<ns> 时间戳。
func enterExit(t *testing.T, snap PullSnapshot) (int64, int64) {
	t.Helper()
	var in, out int64
	for _, ev := range snap.Output {
		var v int64
		if _, err := fmt.Sscanf(ev.Text, "enter-%d", &v); err == nil {
			in = v
		}
		if _, err := fmt.Sscanf(ev.Text, "exit-%d", &v); err == nil {
			out = v
		}
	}
	if in == 0 || out == 0 {
		t.Fatalf("未能解析时间戳: %+v", snap.Output)
	}
	return in, out
}
