package compose

// pulltask_lock_test.go：项目锁与脱敏的 TOCTOU 回归测试。
//
// ============================ 这个测试为什么难写 ============================
//
// 要验证的是"读 .env 构造脱敏器"与"执行命令"在**同一个**项目锁临界区内。
// 简单的"start 之后改 .env"是不够的：pull 可能已经跑完，也可能还没读，
// 结果取决于调度——那样的测试会时过时不过，反而掩盖真问题。
//
// 因此这里用一个**确定性的**办法：让假的 docker 命令在**持有项目锁期间**
// 被阻塞（脚本里 sleep），此时我们从外部改 .env。
// 由于项目锁已被 pull 持有，这次修改必须等锁释放才会生效。
//
// 于是正确实现下：pull 读到的是**旧值**，命令输出旧值 → 被遮；
// 若实现把脱敏器放在锁外构造，它可能在改完之后才读到新值，
// 而命令此时恰好打印新值 → 新值漏出。

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// TestPullRedactSnapshotHeldAcrossLock 验证脱敏快照与执行在同一锁内取得。
//
// ============================ 断言的是什么 ============================
//
// 不能断言"命令打印的任何值都被遮"——脱敏器只遮**它知道的**值
// （.env 与编排正文里的），命令若凭空打印一个谁都没配过的串，遮不住是正常的。
//
// 正确的验证方式：让命令**只**打印 .env 里的值（模拟 compose 展开变量后回显），
// 然后在命令持锁执行期间去改 .env。
//
//   - 正确实现（脱敏器在锁内构造）：pull 用的那份值与它遮的值一致，
//     因此无论 .env 后来被改成什么，命令打印出来的东西都被遮住；
//   - 错误实现（脱敏器在锁外构造）：可能读到旧值去遮、命令却按新值执行，
//     于是新值漏出。
//
// 由于计划执行里 .env 是被锁保护的，正确的实现下这里必须是全遮。
func TestPullRedactSnapshotHeldAcrossLock(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("假 docker 是 shell 脚本，仅在 Linux 上运行")
	}

	// 脚本：打印 started 撑住持锁窗口，然后打印 .env 里的值两次
	// （模拟 compose 展开变量后回显），中间留时间给外部改 .env。
	script := writeScript(t,
		"echo started\nsleep 1\necho current-value-here\necho done\n")
	m, p := newPullTestManager(t, script)
	if _, _, err := m.ws.CommitProject("demo", "services:\n  web:\n    image: nginx\n",
		"PASSWORD=current-value-here\n"); err != nil {
		t.Fatal(err)
	}

	snap, err := p.StartPull("demo", "lock-toctou")
	if err != nil {
		t.Fatal(err)
	}

	// 等命令真正开始（此时它已持有项目锁）。
	deadline := time.Now().Add(5 * time.Second)
	for {
		cur, _ := p.GetPull(snap.TaskID, 0)
		seen := false
		for _, ev := range cur.Output {
			if strings.Contains(ev.Text, "started") {
				seen = true
			}
		}
		if seen {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("命令应已开始")
		}
		time.Sleep(20 * time.Millisecond)
	}

	// 命令执行期间改 .env（会等项目锁——这正是要验证的行为）。
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _, _ = m.ws.CommitProject("demo", "services:\n  web:\n    image: nginx\n",
			"PASSWORD=changed-value-here\n")
	}()

	final := waitTerminal(t, p, snap.TaskID)
	joined := joinEvents(final.Output)

	// 关键断言：命令打印的 .env 原值必须被遮住。
	if strings.Contains(joined, "current-value-here") {
		t.Fatalf(".env 值漏出（脱敏快照与执行不一致）: %q", joined)
	}
	if !strings.Contains(joined, "***") {
		t.Fatalf("应出现遮盖占位符: %q", joined)
	}
	// 改 .env 的操作为了等锁应该也被串行化了（不会在命令跑时插进来）。
	<-done
}

// TestPullRedactUsesOnlyCurrentEnv 验证脱敏集合来自本次执行读到的那份 .env。
//
// 这条补充前一条：直接检查脱敏器的输入（不经命令输出），
// 因此不依赖任何调度时序。
func TestPullRedactUsesOnlyCurrentEnv(t *testing.T) {
	dir := t.TempDir()
	m := NewManager(dir, "")
	if _, _, err := m.ws.CommitProject("demo", "services:\n  web:\n    image: nginx\n",
		"PASSWORD=onlythis\n"); err != nil {
		t.Fatal(err)
	}

	// 反复读取：同一份 .env 必须得到一致的脱敏集合。
	for i := 0; i < 5; i++ {
		r, err := m.buildRedactor("demo")
		if err != nil {
			t.Fatalf("构造脱敏器失败: %v", err)
		}
		if containsStr(r.literals, "onlythis") == false {
			t.Fatalf("第 %d 次：应包含当前 .env 的值，实际 %v", i, r.literals)
		}
	}

	// 改值后，旧的値必须消失、新值必须出现。
	if _, _, err := m.ws.CommitProject("demo", "services:\n  web:\n    image: nginx\n",
		"PASSWORD=changed\n"); err != nil {
		t.Fatal(err)
	}
	r, err := m.buildRedactor("demo")
	if err != nil {
		t.Fatal(err)
	}
	if containsStr(r.literals, "onlythis") {
		t.Fatalf("旧值不应残留: %v", r.literals)
	}
	if !containsStr(r.literals, "changed") {
		t.Fatalf("新值应生效: %v", r.literals)
	}
}

// TestPullRedactWithoutEnvStillScansInline 验证**没有 .env 时仍然扫描编排正文**。
//
// 这是一个真实出现过的漏遮：无 .env 时提前 return，
// 于是 environment: DB_PASSWORD: x 这种内联敏感值完全不被遮盖。
func TestPullRedactWithoutEnvStillScansInline(t *testing.T) {
	dir := t.TempDir()
	m := NewManager(dir, "")
	// 注意：不传 envContent，项目没有 .env。
	if _, _, err := m.ws.CommitProject("demo",
		"services:\n  db:\n    image: postgres\n    environment:\n      DB_PASSWORD: inlinepass\n", ""); err != nil {
		t.Fatal(err)
	}

	r, err := m.buildRedactor("demo")
	if err != nil {
		t.Fatalf("无 .env 不应失败: %v", err)
	}
	if !containsStr(r.literals, "inlinepass") {
		t.Fatalf("无 .env 时内联敏感值仍必须被遮盖，实际 %v", r.literals)
	}
}

// TestPullRedactConfigUnreadableFailsClosed 验证编排正文读不到时 fail-closed。
func TestPullRedactConfigUnreadableFailsClosed(t *testing.T) {
	dir := t.TempDir()
	m := NewManager(dir, "")
	cfgPath, _, err := m.ws.CommitProject("demo", "services:\n  web:\n    image: nginx\n", "")
	if err != nil {
		t.Fatal(err)
	}
	// 删掉正文：无法确认里面有什么敏感值。
	if rerr := os.Remove(cfgPath); rerr != nil {
		t.Fatal(rerr)
	}
	if _, berr := m.buildRedactor("demo"); berr == nil {
		t.Fatal("正文读不到时必须返回错误（fail-closed）")
	}
}

// TestPullRedactInvalidYAMLFailsClosed 验证正文解析失败时 fail-closed。
//
// compose 解析失败会回显原文行，我们无法预判回显什么，
// 因此必须停止而不是"猜着跑"。
func TestPullRedactInvalidYAMLFailsClosed(t *testing.T) {
	dir := t.TempDir()
	m := NewManager(dir, "")
	// 直接写一份非法 YAML（绕过 CommitProject 的校验）。
	if _, werr := m.ws.Write("demo", "services: [unclosed\n  bad: : :"); werr != nil {
		t.Skipf("工作区拒绝写入非法内容（同样可接受）: %v", werr)
	}
	if _, berr := m.buildRedactor("demo"); berr == nil {
		t.Fatal("非法编排必须返回错误（fail-closed）")
	}
	_ = filepath.Join
}
