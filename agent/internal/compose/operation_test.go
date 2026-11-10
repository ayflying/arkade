package compose

// operation_test.go：八个编排动作的 argv、分段、失败短路与冲突。
//
// ============================ 断言策略 ============================
//
// 这些测试用假 docker（shell 脚本）真实执行，逐条核对
// "任务输出里的 command 事件"——也就是用户实际看到的命令文本。
// 不断言内部函数调用，因为用户看到的才是契约。

import (
	"context"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// cmdEvents 取出快照里所有 command 事件文本。
func cmdEvents(snap PullSnapshot) []string {
	var out []string
	for _, ev := range snap.Output {
		if ev.Stream == "command" {
			out = append(out, ev.Text)
		}
	}
	return out
}

// streamEvents 取出指定 stream 的所有事件文本。
func streamEvents(snap PullSnapshot, stream string) []string {
	var out []string
	for _, ev := range snap.Output {
		if ev.Stream == stream {
			out = append(out, ev.Text)
		}
	}
	return out
}

// TestOperationEachActionArgv 验证每个动作真实执行的 argv。
func TestOperationEachActionArgv(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("假 docker 是 shell 脚本，仅在 Linux 上运行")
	}

	cases := []struct {
		name string
		spec OperationSpec
		// wantCmds 是**按执行顺序**的期望命令：每一项对应一条真实命令，
		// 每项内部的多个片段必须全部出现在同一条命令里。
		//
		// 这样 rebuild 的两步（pull、up -d --force-recreate --remove-orphans）
		// 就能被分别验证，而不是把两个片段塞进一条命令里去匹配。
		wantCmds [][]string
	}{
		{"down 不带卷", OperationSpec{Action: ActionDown}, [][]string{{"down"}}},
		{"down 带卷", OperationSpec{Action: ActionDown, RemoveVolumes: true},
			[][]string{{"down", "-v"}}},
		{"stop 全部", OperationSpec{Action: ActionStop}, [][]string{{"stop"}}},
		{"stop 指定服务", OperationSpec{Action: ActionStop, Services: []string{"web"}},
			[][]string{{"stop", "web"}}},
		{"restart 指定服务", OperationSpec{Action: ActionRestart, Services: []string{"web"}},
			[][]string{{"restart", "web"}}},
		{"pull", OperationSpec{Action: ActionPull}, [][]string{{"pull"}}},
		// rebuild 的真实语义是两步：先 pull 再 up -d --force-recreate。
		// 绝不能写成 "rebuild"——那是一条**不存在**的命令，属于造假。
		{"rebuild", OperationSpec{Action: ActionRebuild}, [][]string{
			{"pull"},
			{"up", "-d", "--force-recreate", "--remove-orphans"},
		}},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			script := writeScript(t, "echo ok\n")
			_, p := newPullTestManager(t, script)
			// stop/restart/down 需要项目存在。
			if _, _, err := p.mgr.ws.CommitProject("demo",
				"services:\n  web:\n    image: nginx\n", ""); err != nil {
				t.Fatal(err)
			}

			reqID := "argv-" + strings.ReplaceAll(c.name, " ", "-")
			if len(reqID) < 8 {
				reqID = reqID + "-request"
			}
			snap, err := p.StartOperation(c.spec, "demo", reqID)
			if err != nil {
				t.Fatalf("启动失败: %v", err)
			}
			final := waitTerminal(t, p, snap.TaskID)
			cmds := cmdEvents(final)
			if len(cmds) == 0 {
				t.Fatalf("应有 command 事件，实际输出 %+v", final.Output)
			}

			// ============================ 为什么必须剥掉路径 ============================
			// 假 docker 脚本与 compose.yml 都在 t.TempDir() 下，
			// 而 Go 把**测试名**编进该路径（.../TestX_rebuild123/003/docker）。
			// 直接对整条命令做子串匹配，会让 "rebuild" 因为路径里恰好
			// 含同名子测试而"通过"——这是假阳性，曾被它骗过一次。
			// 因此先滤掉一切含 "/" 的 token，只比较命令语义部分。
			tails := make([]string, 0, len(cmds))
			for _, cmd := range cmds {
				tails = append(tails, composeTail(cmd))
			}
			if len(tails) != len(c.wantCmds) {
				t.Fatalf("应执行 %d 条命令，实际 %d 条: %q", len(c.wantCmds), len(tails), tails)
			}
			for i, want := range c.wantCmds {
				for _, frag := range want {
					if !strings.Contains(tails[i], frag) {
						t.Fatalf("第 %d 条命令应含 %q，实际 %q", i+1, frag, tails[i])
					}
				}
			}
		})
	}
}

// composeTail 去掉命令里的路径成分，只留下可判定的语义 token。
//
// 路径里可能包含测试名、用户名等与命令语义无关的字符串，
// 用它们做断言会得到假阳性。
func composeTail(display string) string {
	fields := strings.Fields(display)
	kept := make([]string, 0, len(fields))
	for _, f := range fields {
		if strings.Contains(f, "/") || strings.Contains(f, "\\") {
			continue
		}
		kept = append(kept, f)
	}
	return strings.Join(kept, " ")
}

// TestOperationRemoveVolumesExplicit 验证 remove 的删卷由参数决定。
//
// ============================ 这是安全断言 ============================
//
// 默认 false 意味着"删除编排"不会顺手删掉数据卷。
// 若实现回退成固定 -v，用户点一次删除就永久丢数据。
func TestOperationRemoveVolumesExplicit(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("假 docker 是 shell 脚本，仅在 Linux 上运行")
	}

	// 不带卷。
	script := writeScript(t, "echo ok\n")
	_, p := newPullTestManager(t, script)
	if _, _, err := p.mgr.ws.CommitProject("demo",
		"services:\n  web:\n    image: nginx\n", ""); err != nil {
		t.Fatal(err)
	}
	snap, err := p.StartOperation(OperationSpec{Action: ActionRemove}, "demo", "remove-novol")
	if err != nil {
		t.Fatal(err)
	}
	final := waitTerminal(t, p, snap.TaskID)
	cmds := cmdEvents(final)
	if len(cmds) == 0 {
		t.Fatalf("应有 command 事件: %+v", final.Output)
	}
	if strings.Contains(cmds[0], "-v") {
		t.Fatalf("未要求删卷时不应出现 -v: %q", cmds[0])
	}

	// 带卷。
	script2 := writeScript(t, "echo ok\n")
	_, p2 := newPullTestManager(t, script2)
	if _, _, err := p2.mgr.ws.CommitProject("demo2",
		"services:\n  web:\n    image: nginx\n", ""); err != nil {
		t.Fatal(err)
	}
	snap2, err := p2.StartOperation(
		OperationSpec{Action: ActionRemove, RemoveVolumes: true}, "demo2", "remove-withvol")
	if err != nil {
		t.Fatal(err)
	}
	final2 := waitTerminal(t, p2, snap2.TaskID)
	cmds2 := cmdEvents(final2)
	if len(cmds2) == 0 || !strings.Contains(cmds2[0], "-v") {
		t.Fatalf("要求删卷时应出现 -v: %+v", cmds2)
	}
}

// TestOperationStreamIsRealTime 验证输出在命令**执行期间**就可见。
//
// ============================ 这是本功能的立身之本 ============================
//
// 若实现是"命令结束后一次性补发"，长操作会长时间空白，
// 用户以为卡死——那正是这个功能要解决的问题。
func TestOperationStreamIsRealTime(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("假 docker 是 shell 脚本，仅在 Linux 上运行")
	}
	// 先打印 started，再睡足够久，最后打印 done。
	script := writeScript(t, "echo started\nsleep 2\necho done\n")
	_, p := newPullTestManager(t, script)
	if _, _, err := p.mgr.ws.CommitProject("demo",
		"services:\n  web:\n    image: nginx\n", ""); err != nil {
		t.Fatal(err)
	}

	snap, err := p.StartOperation(OperationSpec{Action: ActionPull}, "demo", "realtime-request")
	if err != nil {
		t.Fatal(err)
	}

	// 在任务仍处于 running 时就必须能看到 started。
	deadline := time.Now().Add(1500 * time.Millisecond)
	sawStartedWhileRunning := false
	for time.Now().Before(deadline) {
		cur, err := p.GetPull(snap.TaskID, 0)
		if err != nil {
			t.Fatal(err)
		}
		if cur.Status == PullRunning {
			for _, ev := range cur.Output {
				if strings.Contains(ev.Text, "started") {
					sawStartedWhileRunning = true
				}
			}
		}
		if sawStartedWhileRunning {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !sawStartedWhileRunning {
		t.Fatal("任务运行期间就应看到 started（说明输出是结束后才补发的）")
	}
	waitTerminal(t, p, snap.TaskID)
}

// TestOperationShortCircuitOnFailure 验证 down 失败后不再删除本机编排文件。
//
// ============================ 这是安全断言 ============================
//
// 异步任务的 failed 语义是"可以重试"。若 down 失败仍把配置删掉，
// 重试就会变成"项目不存在"（Agent 侧的 workspace 没了，而 Hub 还记着
// 这个项目），用户彻底失去入口，只能重新填一遍配置。
//
// 执行失败时数据必须保留——加日志能力不能让安全性变差。
func TestOperationShortCircuitOnFailure(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("假 docker 是 shell 脚本，仅在 Linux 上运行")
	}
	// 命令以非零退出。
	script := writeScript(t, "echo failing >&2\nexit 3\n")
	_, p := newPullTestManager(t, script)
	if _, _, err := p.mgr.ws.CommitProject("demo",
		"services:\n  web:\n    image: nginx\n", ""); err != nil {
		t.Fatal(err)
	}

	snap, err := p.StartOperation(OperationSpec{Action: ActionRemove}, "demo", "shortcircuit-req")
	if err != nil {
		t.Fatal(err)
	}
	final := waitTerminal(t, p, snap.TaskID)

	if final.Status != PullFailed {
		t.Fatalf("应失败，实际 %q", final.Status)
	}
	// 失败那一步的真实退出码必须保留（不能被抹成 0）。
	if final.ExitCode == nil || *final.ExitCode != 3 {
		t.Fatalf("退出码应为失败那一步的 3，实际 %v", final.ExitCode)
	}
	// 关键：编排文件必须仍在，用户才能重试。
	if _, err := p.mgr.ws.Read("demo"); err != nil {
		t.Fatal("down 失败时必须保留本机编排文件（否则任务无法重试）")
	}
}

// TestOperationErrorWithoutExitCodeReportsMinusOne 验证"命令之外的失败"退出码为 -1。
//
// ============================ 为什么必须 -1 ============================
//
// ApplyProject 的哈希校验在**执行任何命令之前**失败，没有子进程退出码。
// 若上报零值 0，前端会显示"失败（退出码 0）"——自相矛盾，
// 用户无法判断命令到底跑没跑。
func TestOperationErrorWithoutExitCodeReportsMinusOne(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("假 docker 是 shell 脚本，仅在 Linux 上运行")
	}
	script := writeScript(t, "echo ok\n")
	_, p := newPullTestManager(t, script)
	content := "services:\n  web:\n    image: nginx\n"
	if _, _, err := p.mgr.ws.CommitProject("demo", content, ""); err != nil {
		t.Fatal(err)
	}

	// 期望哈希故意写错：ApplyProject 会在落盘/执行前拒绝。
	snap, err := p.StartOperation(OperationSpec{
		Action:     ActionUp,
		Config:     content,
		ExpectHash: "0000000000000000000000000000000000000000000000000000000000000000",
	}, "demo", "hash-mismatch-req")
	if err != nil {
		return // 启动阶段就拒绝也算正确
	}
	final := waitTerminal(t, p, snap.TaskID)
	if final.Status != PullFailed {
		t.Fatalf("哈希不匹配应失败，实际 %q", final.Status)
	}
	if final.ExitCode == nil || *final.ExitCode != -1 {
		t.Fatalf("命令之外的失败退出码应为 -1，实际 %v", final.ExitCode)
	}
}

// TestOperationSameRequestIDDifferentParamsConflicts 验证同 id 不同参数返回冲突。
func TestOperationSameRequestIDDifferentParamsConflicts(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("假 docker 是 shell 脚本，仅在 Linux 上运行")
	}
	script := writeScript(t, "echo ok\n")
	_, p := newPullTestManager(t, script)
	if _, _, err := p.mgr.ws.CommitProject("demo",
		"services:\n  web:\n    image: nginx\n", ""); err != nil {
		t.Fatal(err)
	}

	// 第一次：stop 不带服务。
	if _, err := p.StartOperation(OperationSpec{Action: ActionStop}, "demo", "same-request-id"); err != nil {
		t.Fatal(err)
	}
	// 第二次：同 id 但动作不同 → 冲突。
	if _, err := p.StartOperation(OperationSpec{Action: ActionRestart}, "demo", "same-request-id"); err == nil {
		t.Fatal("同 request_id 不同参数应返回冲突")
	} else if !strings.Contains(err.Error(), "请求") {
		t.Logf("冲突错误文案: %v", err)
	}
}

// TestOperationSameParamsReusesTask 验证同 id 同参数返回同一任务。
func TestOperationSameParamsReusesTask(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("假 docker 是 shell 脚本，仅在 Linux 上运行")
	}
	script := writeScript(t, "echo ok\n")
	_, p := newPullTestManager(t, script)
	if _, _, err := p.mgr.ws.CommitProject("demo",
		"services:\n  web:\n    image: nginx\n", ""); err != nil {
		t.Fatal(err)
	}

	a, err := p.StartOperation(OperationSpec{Action: ActionPull}, "demo", "idempotent-request")
	if err != nil {
		t.Fatal(err)
	}
	b, err := p.StartOperation(OperationSpec{Action: ActionPull}, "demo", "idempotent-request")
	if err != nil {
		t.Fatal(err)
	}
	if a.TaskID != b.TaskID {
		t.Fatalf("同参数应复用任务: %s != %s", a.TaskID, b.TaskID)
	}
	waitTerminal(t, p, a.TaskID)
}

// TestOperationServicesOrderIndependent 验证服务顺序不影响指纹。
func TestOperationServicesOrderIndependent(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("假 docker 是 shell 脚本，仅在 Linux 上运行")
	}
	script := writeScript(t, "echo ok\n")
	_, p := newPullTestManager(t, script)
	if _, _, err := p.mgr.ws.CommitProject("demo",
		"services:\n  web:\n    image: nginx\n", ""); err != nil {
		t.Fatal(err)
	}

	a, err := p.StartOperation(
		OperationSpec{Action: ActionStop, Services: []string{"web", "db"}}, "demo", "order-request")
	if err != nil {
		t.Fatal(err)
	}
	// 顺序反过来 + 重复项。
	b, err := p.StartOperation(
		OperationSpec{Action: ActionStop, Services: []string{"db", "web", "web"}}, "demo", "order-request")
	if err != nil {
		t.Fatalf("顺序不同不应冲突: %v", err)
	}
	if a.TaskID != b.TaskID {
		t.Fatalf("顺序无关的参数应复用任务: %s != %s", a.TaskID, b.TaskID)
	}
	waitTerminal(t, p, a.TaskID)
}

// TestOperationUnknownActionRejected 验证未知动作被拒绝。
func TestOperationUnknownActionRejected(t *testing.T) {
	script := writeScript(t, "echo ok\n")
	_, p := newPullTestManager(t, script)
	if _, err := p.StartOperation(OperationSpec{Action: "explode"}, "demo", "bad-action-req"); err == nil {
		t.Fatal("未知动作应被拒绝")
	}
}

// TestOperationUpWithoutConfigRejected 验证 up 缺配置被拒绝。
func TestOperationUpWithoutConfigRejected(t *testing.T) {
	script := writeScript(t, "echo ok\n")
	_, p := newPullTestManager(t, script)
	if _, err := p.StartOperation(OperationSpec{Action: ActionUp}, "demo", "up-nospec-req"); err == nil {
		t.Fatal("up 缺配置应被拒绝")
	}
}

// TestObserverCapturesNestedRun 验证 Observer 能捕获底层 Runner 的命令。
//
// 这条是"观察点必须放在最底层"的回归：若只挂在 Manager.run，
// ApplyProject 内部的 precheck 就会漏掉。
func TestObserverCapturesNestedRun(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("假 docker 是 shell 脚本，仅在 Linux 上运行")
	}
	script := writeScript(t, "echo ok\n")
	m, _ := newPullTestManager(t, script)

	var mu struct {
		sync.Mutex
		cmds []string
	}
	ob := &executionObserver{
		onCommand: func(argv []string, _ *Runner) {
			mu.Lock()
			mu.cmds = append(mu.cmds, strings.Join(argv, " "))
			mu.Unlock()
		},
	}
	ctx := withObserver(context.Background(), ob)

	// 直接用 Runner.Run（模拟 precheck 这类底层调用）。
	r := m.runnerFor("demo")
	r.Dir = m.pullWorkDir("demo")
	r.Run(ctx, "demo", TimeoutShort, "version")

	mu.Lock()
	defer mu.Unlock()
	if len(mu.cmds) == 0 {
		t.Fatal("底层 Runner.Run 的命令必须被捕获")
	}
	if !strings.Contains(mu.cmds[0], "version") {
		t.Fatalf("捕获的命令不对: %q", mu.cmds[0])
	}
}
