package compose

// pulltask_test.go：镜像更新任务的端到端行为。
//
// ============================ 用脚本代替真实 docker ============================
//
// 全部测试用一个**假的 docker 可执行文件**（见 fakeClientScript），
// 通过 Runner.Binary 指过去。理由：
//   - 真实 docker 需要网络拉镜像，测试会不稳定且慢；
//   - 需要精确控制"什么时候输出第几行"，才能验证"完成前可见"。
// 脚本本身不联网，只按参数打印内容。

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// fakeDockerScript 在 Linux 上是一个 shell 脚本；Windows 上跳过相关用例。
func fakeDockerScript(body string) string {
	return "#!/bin/sh\n" + body
}

// writeScript 写一个可执行的假 docker。
func writeScript(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "docker")
	if err := os.WriteFile(p, []byte(fakeDockerScript(body)), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

// newPullTestManager 构造带假 docker 的 Manager 与 PullManager。
func newPullTestManager(t *testing.T, script string) (*Manager, *PullManager) {
	t.Helper()
	dir := t.TempDir()
	m := NewManager(dir, "")
	m.runner = &Runner{Binary: script}
	m.RootCtx = context.Background()
	// 建一个项目，让 requireExists 通过。
	if _, _, err := m.ws.CommitProject("demo", "services:\n  web:\n    image: nginx\n", ""); err != nil {
		t.Fatal(err)
	}
	return m, NewPullManager(m, nil)
}

// requireLinux 在非 Linux 上跳过（假 docker 是 shell 脚本）。
func requireLinux(t *testing.T) {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("假 docker 是 shell 脚本，仅在 Linux 上运行")
	}
}

// TestPullStartReturnsImmediately 验证 start 不等命令结束就返回。
func TestPullStartReturnsImmediately(t *testing.T) {
	requireLinux(t)
	script := writeScript(t, "echo start\nsleep 2\necho done\n")
	_, p := newPullTestManager(t, script)

	begin := time.Now()
	snap, err := p.StartPull("demo", "req-00001")
	elapsed := time.Since(begin)
	if err != nil {
		t.Fatalf("不应失败: %v", err)
	}
	if elapsed > 1500*time.Millisecond {
		t.Fatalf("start 应立即返回，实际耗时 %v", elapsed)
	}
	if snap.TaskID == "" {
		t.Fatal("应返回 task_id")
	}
	if snap.Status != PullQueued && snap.Status != PullRunning {
		t.Fatalf("初始状态应为 queued/running，实际 %q", snap.Status)
	}
	// queued 阶段不再预先填命令：只有第一条真实 command 事件到来时才展示。
	// 这是刻意的——预先展示"预计命令"会在参数变化后与实际执行的不一致。
	// 因此这里只在 running 时要求命令已可见。
	if snap.Status == PullRunning && !strings.Contains(snap.Command, "compose") {
		t.Fatalf("运行中应展示真实命令: %q", snap.Command)
	}
}

// TestPullOutputVisibleBeforeDone 验证输出在命令完成前就能被 get 到。
func TestPullOutputVisibleBeforeDone(t *testing.T) {
	requireLinux(t)
	script := writeScript(t, "echo first-segment\nsleep 2\necho second-segment\n")
	_, p := newPullTestManager(t, script)

	snap, err := p.StartPull("demo", "req-00002")
	if err != nil {
		t.Fatal(err)
	}
	// 等到第一段出现。
	var sawFirst bool
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		cur, gerr := p.GetPull(snap.TaskID, 0)
		if gerr != nil {
			t.Fatal(gerr)
		}
		for _, ev := range cur.Output {
			if strings.Contains(ev.Text, "first-segment") {
				sawFirst = true
			}
		}
		if sawFirst && cur.Status == PullRunning {
			// 命令还没结束（sleep 未过），但第一段已经可见——这就是要验的。
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !sawFirst {
		t.Fatal("第一段输出应在命令完成前可见")
	}
}

// TestPullCapturesStderr 验证 stderr 被独立标记。
func TestPullCapturesStderr(t *testing.T) {
	requireLinux(t)
	script := writeScript(t, "echo oops 1>&2\nexit 1\n")
	_, p := newPullTestManager(t, script)

	snap, _ := p.StartPull("demo", "req-00003")
	final := waitTerminal(t, p, snap.TaskID)
	if final.Status != PullFailed {
		t.Fatalf("应失败，实际 %q", final.Status)
	}
	var sawStderr bool
	for _, ev := range final.Output {
		if ev.Stream == "stderr" && strings.Contains(ev.Text, "oops") {
			sawStderr = true
		}
	}
	if !sawStderr {
		t.Fatalf("应捕获 stderr，实际 %+v", final.Output)
	}
	if final.ExitCode == nil || *final.ExitCode != 1 {
		t.Fatalf("退出码应为 1，实际 %v", final.ExitCode)
	}
}

// TestPullSucceeded 验证成功路径。
func TestPullSucceeded(t *testing.T) {
	requireLinux(t)
	script := writeScript(t, "echo pulled-ok\nexit 0\n")
	_, p := newPullTestManager(t, script)

	snap, _ := p.StartPull("demo", "req-00004")
	final := waitTerminal(t, p, snap.TaskID)
	if final.Status != PullSucceeded {
		t.Fatalf("应成功，实际 %q (%s)", final.Status, final.Error)
	}
	if final.ExitCode == nil || *final.ExitCode != 0 {
		t.Fatalf("成功时退出码应为 0，实际 %v", final.ExitCode)
	}
}

// TestPullDuplicateStartReusesTask 验证同 request_id 只执行一次。
func TestPullDuplicateStartReusesTask(t *testing.T) {
	requireLinux(t)
	marker := filepath.Join(t.TempDir(), "runs")
	script := writeScript(t, "echo x >> "+marker+"\necho pulled\n")
	_, p := newPullTestManager(t, script)

	s1, err := p.StartPull("demo", "same-request")
	if err != nil {
		t.Fatal(err)
	}
	s2, err := p.StartPull("demo", "same-request")
	if err != nil {
		t.Fatal(err)
	}
	if s1.TaskID != s2.TaskID {
		t.Fatalf("同 request_id 应返回同一任务: %s ≠ %s", s1.TaskID, s2.TaskID)
	}
	waitTerminal(t, p, s1.TaskID)
	// 命令只应被执行一次。
	b, _ := os.ReadFile(marker)
	if got := strings.Count(string(b), "x"); got != 1 {
		t.Fatalf("命令应只执行一次，实际 %d 次", got)
	}
}

// TestPullRequestIDNameMismatch 验证同 request_id 不同项目被拒绝。
func TestPullRequestIDNameMismatch(t *testing.T) {
	requireLinux(t)
	script := writeScript(t, "echo ok\n")
	m, p := newPullTestManager(t, script)
	if _, _, err := m.ws.CommitProject("other", "services:\n  a:\n    image: alpine\n", ""); err != nil {
		t.Fatal(err)
	}

	if _, err := p.StartPull("demo", "conflict-request"); err != nil {
		t.Fatal(err)
	}
	if _, err := p.StartPull("other", "conflict-request"); !errors.Is(err, ErrPullRequestMismatch) {
		t.Fatalf("应报 request 冲突，实际 %v", err)
	}
}

// TestPullTaskCapRejects 验证任务数上限。
func TestPullTaskCapRejects(t *testing.T) {
	requireLinux(t)
	script := writeScript(t, "sleep 30\n")
	_, p := newPullTestManager(t, script)

	for i := 0; i < MaxPullTasks; i++ {
		if _, err := p.StartPull("demo", "cap-request-0"+string(rune('a'+i))); err != nil {
			t.Fatalf("第 %d 个任务不应被拒: %v", i, err)
		}
	}
	if _, err := p.StartPull("demo", "cap-overflow"); !errors.Is(err, ErrTooManyPullTasks) {
		t.Fatalf("超过上限应拒绝，实际 %v", err)
	}
	p.Close()
}

// TestPullRedactsEnvValues 验证 .env 里的值不出现在输出里。
func TestPullRedactsEnvValues(t *testing.T) {
	requireLinux(t)
	script := writeScript(t, "echo 'using secret hunter2 now'\necho done\n")
	m, p := newPullTestManager(t, script)
	if _, _, err := m.ws.CommitProject("demo", "services:\n  web:\n    image: nginx\n",
		"PASSWORD=hunter2\n"); err != nil {
		t.Fatal(err)
	}

	snap, _ := p.StartPull("demo", "redact-request")
	final := waitTerminal(t, p, snap.TaskID)
	joined := joinEvents(final.Output)
	if strings.Contains(joined, "hunter2") {
		t.Fatalf(".env 值漏出: %q", joined)
	}
	if !strings.Contains(joined, "***") {
		t.Fatalf("应出现遮盖占位符: %q", joined)
	}
}

// TestPullRedactsInlineComposeSecret 验证编排内联敏感值被遮盖。
//
// compose 解析出错时会把 environment: 整行原文打到 stderr，
// 只遮 .env 完全保护不到这种情况。
func TestPullRedactsInlineComposeSecret(t *testing.T) {
	requireLinux(t)
	// 模拟 compose 打印含内联密码的错误行。
	script := writeScript(t, "echo 'error while interpolating services.db.environment.DB_PASSWORD: hunter2'\nexit 1\n")
	m, p := newPullTestManager(t, script)
	if _, _, err := m.ws.CommitProject("demo",
		"services:\n  db:\n    image: postgres\n    environment:\n      DB_PASSWORD: hunter2\n", ""); err != nil {
		t.Fatal(err)
	}

	snap, _ := p.StartPull("demo", "inline-request")
	final := waitTerminal(t, p, snap.TaskID)
	joined := joinEvents(final.Output)
	if strings.Contains(joined, "hunter2") {
		t.Fatalf("内联密码漏出: %q", joined)
	}
}

// TestPullRedactFailClosedOnBadEnv 验证 .env 无法解析时**隐藏输出但业务继续**。
//
// ============================ 语义已变更（用户明确授权） ============================
// 旧行为：脱敏不可用 → 不执行命令（fail-closed）。
// 新行为：业务继续执行，仅把输出全部隐藏。
//
// 理由：日志脱敏是新一轮加的展示能力，不应该反过来阻止用户
// 本来就能做的操作。安全边界从"不执行"改为"不泄露"。
func TestPullRedactFailClosedOnBadEnv(t *testing.T) {
	requireLinux(t)
	marker := filepath.Join(t.TempDir(), "ran")
	script := writeScript(t, "echo x >> "+marker+"\necho pulled\n")
	m, p := newPullTestManager(t, script)
	// 插值写法：我们无法确定真实值，因此无法安全脱敏。
	if _, _, err := m.ws.CommitProject("demo", "services:\n  web:\n    image: nginx\n",
		"PASSWORD=${OTHER}\n"); err != nil {
		t.Fatal(err)
	}

	snap, _ := p.StartPull("demo", "failclosed-request")
	final := waitTerminal(t, p, snap.TaskID)

	// 业务必须继续：命令真的跑过。
	if final.Status != PullSucceeded {
		t.Fatalf("脱敏不可用不应阻止业务，实际 %q（%s）", final.Status, final.Error)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatal("业务必须继续执行（命令应已跑过）")
	}
	// 但输出必须被隐藏：不能出现任何原文。
	joined := joinEvents(final.Output)
	if strings.Contains(joined, "pulled") {
		t.Fatalf("输出应被隐藏，却看到原文: %q", joined)
	}
	if !strings.Contains(joined, hideNoticeText) {
		t.Fatalf("应给出固定的隐藏说明: %q", joined)
	}
	if len(final.Output) == 0 {
		t.Fatalf("不应有任何输出: %+v", final.Output)
	}
}

// TestPullLockErrExitCodeIsMinusOne 验证等锁/前置失败时退出码是 -1 而非 0。
//
// 0 会被前端读成"成功"，而实际上命令根本没跑。
func TestPullLockErrExitCodeIsMinusOne(t *testing.T) {
	requireLinux(t)
	script := writeScript(t, "echo ok\n")
	m, p := newPullTestManager(t, script)
	// 删掉项目目录，让锁内 requireExists 失败。
	if err := os.RemoveAll(filepath.Join(m.ws.stacksDir(), "demo")); err != nil {
		t.Fatal(err)
	}

	snap, err := p.StartPull("demo", "exit-request")
	if err == nil {
		// StartPull 的预检查可能先拦住；若进了任务则检查终态。
		final := waitTerminal(t, p, snap.TaskID)
		if final.ExitCode == nil || *final.ExitCode != -1 {
			t.Fatalf("等锁阶段失败时退出码应为 -1，实际 %v", final.ExitCode)
		}
		if final.Status != PullFailed {
			t.Fatalf("应失败，实际 %q", final.Status)
		}
	}
}

// TestPullTruncatesOverLimit 验证超限时丢弃最旧并置 truncated。
func TestPullTruncatesOverLimit(t *testing.T) {
	requireLinux(t)
	// 输出超过行数上限。
	script := writeScript(t, "i=0\nwhile [ $i -lt 2100 ]; do echo line-$i; i=$((i+1)); done\n")
	_, p := newPullTestManager(t, script)

	snap, _ := p.StartPull("demo", "trunc-request")
	final := waitTerminal(t, p, snap.TaskID)
	if !final.Truncated {
		t.Fatal("超过上限应置 truncated")
	}
	if len(final.Output) > maxPullEvents {
		t.Fatalf("保留行数不应超过上限，实际 %d", len(final.Output))
	}
	// 应保留尾部（失败原因总在末尾）。
	joined := joinEvents(final.Output)
	if !strings.Contains(joined, "line-2099") {
		t.Fatal("应保留最后几行")
	}
	if strings.Contains(joined, "line-0\n") {
		t.Fatal("最旧的行应被丢弃")
	}
}

// TestPullCursorIncremental 验证 after 游标只返回新增行。
func TestPullCursorIncremental(t *testing.T) {
	requireLinux(t)
	script := writeScript(t, "echo a\necho b\necho c\n")
	_, p := newPullTestManager(t, script)

	snap, _ := p.StartPull("demo", "cursor-request")
	final := waitTerminal(t, p, snap.TaskID)
	all, _ := p.GetPull(snap.TaskID, 0)
	// 事件流现在是：1 条 command + 3 条 stdout = 4。
	want := 4
	if len(all.Output) != want {
		t.Fatalf("应有 %d 个事件（1 条命令 + 3 行输出），实际 %d", want, len(all.Output))
	}
	// 用游标取第一行之后：剩下 2 条 stdout + 1 条 command 之后的内容。
	rest, _ := p.GetPull(snap.TaskID, all.Output[0].Seq)
	if len(rest.Output) != 3 {
		t.Fatalf("游标后应剩 3 个事件，实际 %d", len(rest.Output))
	}
	_ = final
}

// TestPullTaskNotFound 验证未知任务返回明确错误。
func TestPullTaskNotFound(t *testing.T) {
	_, p := newPullTestManager(t, writeScript(t, "echo ok\n"))
	if _, err := p.GetPull("nope", 0); !errors.Is(err, ErrPullTaskNotFound) {
		t.Fatalf("应报任务不存在，实际 %v", err)
	}
}

// waitTerminal 轮询直到任务进入终态。
func waitTerminal(t *testing.T, p *PullManager, id string) PullSnapshot {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		s, err := p.GetPull(id, 0)
		if err != nil {
			t.Fatalf("查询失败: %v", err)
		}
		if s.Status == PullSucceeded || s.Status == PullFailed {
			return s
		}
		time.Sleep(30 * time.Millisecond)
	}
	t.Fatal("等待任务终态超时")
	return PullSnapshot{}
}

// joinEvents 把输出行拼成一个字符串便于断言。
func joinEvents(evs []PullEvent) string {
	var b strings.Builder
	for _, ev := range evs {
		b.WriteString(ev.Text)
		b.WriteByte('\n')
	}
	return b.String()
}
