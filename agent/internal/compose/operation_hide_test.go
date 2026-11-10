package compose

// operation_hide_test.go：spec 秘密无法解析时的**粘性隐藏**。
//
// ============================ 为什么必须粘性 ============================
//
// up 执行的 .env 是**新写入**的。若它含复杂插值（如 ${OTHER}），
// 我们无从得知真实值，只能整场隐藏输出。
//
// 危险在于：precheck 或其它阶段可能读到一份**能解析**的旧配置，
// 如果此时把 hideAll 重置为 false，随后输出的新 spec 密码就漏了。
// 因此"隐藏"一旦因 spec 解析失败而开启，就必须整场保持。

import (
	"runtime"
	"strings"
	"testing"
)

// TestOperationSeedFailKeepsHidingAfterValidRefresh 验证 seed 失败后仍持续隐藏。
//
// 场景：
//   - 磁盘上的 .env 是**能解析**的简单配置（oldpass）；
//   - 而本次 up 下发的新 .env 含 ${OTHER}（无法确定真实值）。
//
// 期望：整场任务输出被隐藏，即便某一步用磁盘配置成功重建了脱敏器。
func TestOperationSeedFailKeepsHidingAfterValidRefresh(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("假 docker 是 shell 脚本，仅在 Linux 上运行")
	}
	// 命令会回显一个新秘密，用来检测是否漏出。
	script := writeScript(t, "echo new-secret-echo\nmore-line\n")
	m, p := newPullTestManager(t, script)
	// 磁盘上的旧配置可解析。
	if _, _, err := m.ws.CommitProject("demo",
		"services:\n  web:\n    image: nginx\n", "PASSWORD=oldpass\n"); err != nil {
		t.Fatal(err)
	}

	// 新 spec 的 .env 含插值：无法安全脱敏。
	snap, err := p.StartOperation(OperationSpec{
		Action: ActionUp,
		Config: "services:\n  web:\n    image: nginx\n",
		Env:    "PASSWORD=${OTHER}\n",
	}, "demo", "sticky-hide-req")
	if err != nil {
		t.Fatalf("启动不应失败（隐藏只影响输出）: %v", err)
	}
	final := waitTerminal(t, p, snap.TaskID)

	joined := joinEvents(final.Output)
	// 最关键：绝不能出现命令输出原文。
	if strings.Contains(joined, "new-secret-echo") {
		t.Fatalf("seed 失败后仍泄露了输出原文: %q", joined)
	}
	if strings.Contains(joined, "more-line") {
		t.Fatalf("seed 失败后仍泄露了后续输出: %q", joined)
	}
	// 应给出固定的隐藏说明（且不要刷屏）。
	if !strings.Contains(joined, hideNoticeText) {
		t.Fatalf("应给出隐藏说明: %q", joined)
	}
	if n := strings.Count(joined, hideNoticeText); n != 1 {
		t.Fatalf("隐藏说明应只出现一次，实际 %d 次", n)
	}
}

// TestOperationSeedFailStillSucceedsBusiness 验证隐藏不影响业务结果。
//
// 用户明确要求：脱敏不能反过来阻止本来就能做的操作。
func TestOperationSeedFailStillSucceedsBusiness(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("假 docker 是 shell 脚本，仅在 Linux 上运行")
	}
	script := writeScript(t, "echo ok\nexit 0\n")
	m, p := newPullTestManager(t, script)
	if _, _, err := m.ws.CommitProject("demo",
		"services:\n  web:\n    image: nginx\n", "A=1\n"); err != nil {
		t.Fatal(err)
	}

	snap, err := p.StartOperation(OperationSpec{
		Action: ActionUp,
		Config: "services:\n  web:\n    image: nginx\n",
		Env:    "PASSWORD=${NOPE}\n",
	}, "demo", "sticky-business-req")
	if err != nil {
		t.Fatal(err)
	}
	final := waitTerminal(t, p, snap.TaskID)

	// 业务结果如实反映命令执行情况，不因隐藏而改变。
	if final.Status == PullFailed && (final.ExitCode == nil || *final.ExitCode == 0) {
		t.Fatalf("失败时不应宣告退出码 0: %v", final.ExitCode)
	}
	// 无论成败，输出必须被隐藏。
	joined := joinEvents(final.Output)
	if strings.Contains(joined, "ok") {
		t.Fatalf("输出应被隐藏: %q", joined)
	}
}
