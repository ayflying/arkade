package compose

// operation_secret_test.go：up 场景下"新配置的秘密"必须被遮住。
//
// ============================ 这两条为什么不可缺少 ============================
//
// 只靠旧的 pull 测试不足以证明 up 的安全：
//   - pull 用的 .env 是磁盘上已有的，任务开始时就能读到；
//   - up 的 .env 是**本次新写入**的，任务开始时磁盘上还是旧值。
//
// 因此"用新配置里的值去遮挡新配置产生的输出"必须单独验证，
// 否则会出现"新密码本不在脱敏集合里"的漏洞。

import (
	"runtime"
	"strings"
	"testing"
)

// TestOperationUpPrecheckNewSecret 验证 up 会把新 .env 与内联秘密从输出里遮掉。
//
// 场景设定：
//   - 新 .env 里是 PASSWORD=new-val-x7；编排正文 environment 里内联 API_TOKEN；
//   - 假 docker 模拟 compose 把这些值回显到 stdout（真实场景里
//     compose 的报错/进度信息确实可能带出插值后的值）。
//
// 期望：两个值都被替换成占位符，原文一个字节都不出现。
func TestOperationUpPrecheckNewSecret(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("假 docker 是 shell 脚本，仅在 Linux 上运行")
	}
	const (
		envSecret    = "new-val-x7"
		inlineSecret = "inline-tok-y9"
	)
	// 模拟 compose 回显插值结果。
	script := writeScript(t,
		"echo 'config check ok'\n"+
			"echo 'PASSWORD="+envSecret+"'\n"+
			"echo 'API_TOKEN="+inlineSecret+"'\n"+
			"echo 'up done'\n")

	_, p := newPullTestManager(t, script)

	config := "services:\n  web:\n    image: nginx\n    environment:\n" +
		"      - API_TOKEN=" + inlineSecret + "\n"
	snap, err := p.StartOperation(OperationSpec{
		Action: ActionUp,
		Config: config,
		Env:    "PASSWORD=" + envSecret + "\n",
	}, "demo", "up-newsecret-req")
	if err != nil {
		t.Fatal(err)
	}
	final := waitTerminal(t, p, snap.TaskID)

	joined := joinEvents(final.Output)
	if strings.Contains(joined, envSecret) {
		t.Fatalf("新 .env 的值被泄露: %q", joined)
	}
	if strings.Contains(joined, inlineSecret) {
		t.Fatalf("编排内联秘密被泄露: %q", joined)
	}
	if !strings.Contains(joined, "***") {
		t.Fatalf("应出现脱敏占位符: %q", joined)
	}
	// 非敏感内容仍应保留，证明不是"一刀切隐藏"。
	if !strings.Contains(joined, "config check ok") {
		t.Fatalf("普通输出不应被隐藏: %q", joined)
	}
}

// TestOperationUpInvalidSpecSticky 验证新 spec 的秘密无法解析时，
// 后续"旧配置解析成功"的刷新**不能**重新打开输出。
//
// ============================ 这是 sticky 的核心断言 ============================
//
// 攻击面：新 .env 含 ${OTHER}，我们不知其真实值 → 必须隐藏。
// 但 up 执行过程中，某一阶段可能用**磁盘上的旧配置**（能解析）去重建脱敏器。
// 若那时把 hideAll 置回 false，后续输出的新值就漏了。
//
// 因此这里断言：即使存在一份完全可解析的旧 .env，
// 整场任务的输出仍然保持隐藏。
func TestOperationUpInvalidSpecSticky(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("假 docker 是 shell 脚本，仅在 Linux 上运行")
	}
	// 命令会打印新秘密，用来检测是否漏出。
	script := writeScript(t, "echo leaked-value-z1\necho second-leak-z2\n")

	m, p := newPullTestManager(t, script)
	// 磁盘上放一份**可解析**的旧配置：这正是会让 hideAll 被重置的诱因。
	if _, _, err := m.ws.CommitProject("demo",
		"services:\n  web:\n    image: nginx\n", "OLD=simple-old-value\n"); err != nil {
		t.Fatal(err)
	}

	snap, err := p.StartOperation(OperationSpec{
		Action: ActionUp,
		Config: "services:\n  web:\n    image: nginx\n",
		// 插值写法：真实值取决于运行时环境，无法安全脱敏。
		Env: "PASSWORD=${OTHER}\n",
	}, "demo", "up-invalidspec-req")
	if err != nil {
		t.Fatalf("启动不应失败: %v", err)
	}
	final := waitTerminal(t, p, snap.TaskID)

	joined := joinEvents(final.Output)
	for _, secret := range []string{"leaked-value-z1", "second-leak-z2"} {
		if strings.Contains(joined, secret) {
			t.Fatalf("新 spec 无法解析时仍泄露 %q: %q", secret, joined)
		}
	}
	if !strings.Contains(joined, hideNoticeText) {
		t.Fatalf("应给出固定的隐藏说明: %q", joined)
	}
}
