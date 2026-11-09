package compose

// precheck_test.go 覆盖"落盘前先校验"这条规则。
//
// 为什么值得单独一组：
// 这条规则替代了早先的"命令失败也要保住内容"。动机没变（用户写的 YAML 是资产），
// 但做法必须变——把 compose 解析不了的 compose.yml 写进项目目录，
// 会让这个项目之后**所有**命令都失败，包括 down/stop，
// 用户连"把它停掉"都做不到，只能登机器手工删文件。

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestApplyRejectsWhenPrecheckFails 验证校验失败时**一个字节都不写**。
func TestApplyRejectsWhenPrecheckFails(t *testing.T) {
	m, _ := newSafeManager(t)
	m.applier = func(context.Context, string, string, string) PrecheckResult {
		return PrecheckResult{Reason: "配置不合法（测试桩）"}
	}

	spec := ProjectSpec{Name: "bad-config", Content: "services: [\n", EnvContent: "A=1\n"}
	spec.ExpectHash = HashProject(spec.Content, spec.EnvContent)

	_, err := m.ApplyProject(context.Background(), spec)
	if err == nil {
		t.Fatal("校验失败应返回错误")
	}
	if !strings.Contains(err.Error(), "不合法") {
		t.Errorf("应回传校验原因，实际 %q", err.Error())
	}
	if m.ws.Exists("bad-config") {
		t.Error("校验失败时不得写入 compose.yml")
	}
	if m.ws.EnvExists("bad-config") {
		t.Error("校验失败时不得写入 .env")
	}
}

// TestApplyRejectsWhenPrecheckFailsKeepsRunningProject 验证校验失败时不动现有项目。
//
// 这是最关键的一条：项目已经在跑，用户下发了一份不合法的配置。
// 结果必须是"现有配置完好、服务不受影响"，而不是"配置被换成坏的，
// 于是 stop/restart 全部失败"。
func TestApplyRejectsWhenPrecheckFailsKeepsRunningProject(t *testing.T) {
	m, _ := newSafeManager(t)

	// 先建立一个正常项目。
	good := ProjectSpec{Name: "running", Content: "GOOD-COMPOSE\n", EnvContent: "A=good\n"}
	good.ExpectHash = HashProject(good.Content, good.EnvContent)
	if _, err := m.ApplyProject(context.Background(), good); err != nil {
		t.Fatal(err)
	}

	// 让校验失败，下发一份坏配置。
	m.applier = func(context.Context, string, string, string) PrecheckResult {
		return PrecheckResult{Reason: "配置不合法（测试桩）"}
	}
	bad := ProjectSpec{Name: "running", Content: "BAD-COMPOSE\n", EnvContent: "A=bad\n"}
	bad.ExpectHash = HashProject(bad.Content, bad.EnvContent)
	if _, err := m.ApplyProject(context.Background(), bad); err == nil {
		t.Fatal("应返回错误")
	}

	// 现有配置必须完好。
	cfg, err := m.ws.Read("running")
	if err != nil {
		t.Fatal(err)
	}
	if cfg != "GOOD-COMPOSE\n" {
		t.Errorf("校验失败不得改动现有 compose.yml，实际 %q", cfg)
	}
	env, _ := m.ws.ReadEnv("running")
	if env != "A=good\n" {
		t.Errorf("校验失败不得改动现有 .env，实际 %q", env)
	}

	// 哈希也必须仍反映旧内容，否则界面会显示"已更新"。
	if h := m.projectHash("running", filepath.Join(mustDir(t, m, "running"), composeFileName)); h != good.ExpectHash {
		t.Errorf("哈希应仍为旧内容 %q，实际 %q", good.ExpectHash, h)
	}
}

// mustDir 返回项目目录（测试用，失败即终止）。
func mustDir(t *testing.T, m *Manager, name string) string {
	t.Helper()
	dir, err := m.ws.ProjectDir(name)
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

// TestPrecheckUsesTempDirNotProjectDir 验证校验过程不在项目目录留下痕迹。
//
// 若在项目目录里"试写再校验再回滚"，中间那一瞬正是本项目禁止的半写状态。
func TestPrecheckUsesTempDirNotProjectDir(t *testing.T) {
	m, _ := newTestManager(t)
	// 用真实校验：本机没有 docker，会失败。重点是它**没有碰项目目录**。
	pc := m.precheckProject(context.Background(), "tmpcheck", "services: {}\n", "A=1\n")
	_ = pc

	// 项目目录要么不存在，要么存在但为空（不含 compose.yml / .env）。
	dir, err := m.ws.ProjectDir("tmpcheck")
	if err != nil {
		t.Fatal(err)
	}
	if _, statErr := os.Stat(dir); statErr == nil {
		entries, _ := os.ReadDir(dir)
		for _, e := range entries {
			switch e.Name() {
			case composeFileName, envFileName:
				t.Errorf("校验不应在项目目录创建 %s", e.Name())
			}
		}
	}
}

// TestPrecheckNeverEchoesOutput 验证校验失败时不回传 compose 的输出。
//
// `docker compose config` 会把 ${PASSWORD} 展开成真实值再打印。
// 一旦把它的输出塞进错误信息，密钥就直接进了 Hub 的审计记录和浏览器页面。
// 因此失败文案必须是**我们自己写的**固定句子，不含任何命令输出。
func TestPrecheckNeverEchoesOutput(t *testing.T) {
	m, _ := newTestManager(t)
	const secret = "SUPER-SECRET-PASSWORD-VALUE"

	pc := m.precheckProject(context.Background(), "echo-check",
		"services:\n  web:\n    image: nginx\n    environment:\n      PW: ${PW}\n", "PW="+secret)
	if pc.OK {
		t.Skip("本机有可用的 docker compose，无法制造校验失败场景")
	}
	if strings.Contains(pc.Reason, secret) {
		t.Errorf("校验失败文案泄漏了环境变量值: %q", pc.Reason)
	}
}

// TestPrecheckIsDefaultOn 验证生产路径默认真实校验（applier 为 nil）。
//
// 这是防"配置里忘了打开校验"的最后一道结构保证：
// NewManager 不设置 applier，因此 checkConfig 一定走真实实现。
func TestPrecheckIsDefaultOn(t *testing.T) {
	m, _ := newTestManager(t)
	if m.applier != nil {
		t.Fatal("NewManager 不应设置 applier（生产路径必须真实校验）")
	}

	// 真实校验在没有 docker 的环境里必然失败 → ApplyProject 必须拒绝。
	spec := ProjectSpec{Name: "default-on", Content: "services: {}\n"}
	spec.ExpectHash = HashProject(spec.Content, "")
	if _, err := m.ApplyProject(context.Background(), spec); err == nil {
		t.Skip("本机有可用的 docker compose，无法验证失败分支")
	} else if m.ws.Exists("default-on") {
		t.Error("真实校验失败时不得写入 compose.yml")
	}
}

// TestPrecheckTempEnvIsSecretMode 验证临时目录里的 .env 也是 0600。
//
// 校验用的临时文件同样含明文密钥。若按 0644 写，
// 在同机多用户的场景下就等于把密钥临时公开了一次。
func TestPrecheckTempEnvIsSecretMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows 不使用 POSIX 权限位")
	}
	dir := t.TempDir()
	p := filepath.Join(dir, ".env")
	if err := writeSecretFile(p, "SECRET=abc\n"); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("临时 .env 权限应为 0600，实际 %04o", perm)
	}
}
