package compose

// env_test.go 覆盖项目 .env 的解析、校验、落盘权限，以及
// **最关键的**一件事：.env 里的变量必须真的压过 Agent 进程环境。
//
// 最后一类测试是这组里最重要的。compose 的插值优先级是
// shell 环境 > .env，如果不过滤继承环境，用户在 .env 里写的值
// 会被 Agent 自己的同名变量静默压住——界面上显示"已保存"，
// 实际跑的是别的东西，而且没有任何报错。

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestValidateEnvContentAccepts 验证正常 .env 被接受。
func TestValidateEnvContentAccepts(t *testing.T) {
	cases := []string{
		"",
		"FOO=bar\n",
		"# 注释\n\nFOO=bar\nBAZ=qux\n",
		"export FOO=bar\n",
		`PASSWORD="a=b c"`,
		"EMPTY=\n",
	}
	for _, c := range cases {
		if err := ValidateEnvContent(c); err != nil {
			t.Errorf("应接受 %q，实际报错: %v", c, err)
		}
	}
}

// TestValidateEnvContentRejects 验证明显不合理的 .env 被拒。
//
// 拒绝的价值：一份 compose 解析不了的 .env 会让该项目**之后所有**
// 命令都失败，包括本应能救场的 down/stop，用户就被卡死了。
func TestValidateEnvContentRejects(t *testing.T) {
	cases := []struct{ name, content string }{
		{"含 NUL", "FOO=a\x00b"},
		{"非 KEY=VALUE 行", "FOO=bar\n这就是一句话\n"},
		{"超大内容", strings.Repeat("K=v\n", maxEnvContentBytes/4+10)},
	}
	for _, c := range cases {
		if err := ValidateEnvContent(c.content); err == nil {
			t.Errorf("%s 应被拒绝", c.name)
		}
	}
}

// TestParseEnvKeys 验证变量名提取。
func TestParseEnvKeys(t *testing.T) {
	content := "FOO=1\n# 注释\nBAR=2\nexport BAZ=3\nQUX=\n\n  SPACED = 4  \n"
	got := ParseEnvKeys(content)
	want := []string{"FOO", "BAR", "BAZ", "QUX", "SPACED"}
	if len(got) != len(want) {
		t.Fatalf("期望 %v，实际 %v", want, got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("第 %d 个键期望 %q 实际 %q（顺序也重要：保持文件顺序便于排查）", i, want[i], got[i])
		}
	}
}

// TestParseEnvKeysSkipsInvalid 验证非法行被跳过而不是报错。
//
// 这个函数的产物只用于"从继承环境剔除同名变量"，
// 为一个格式怪异的行中断整个操作代价更大。
func TestParseEnvKeysSkipsInvalid(t *testing.T) {
	got := ParseEnvKeys("1BAD=x\n=novalue\nGOOD=1\n含中文=2\n")
	for _, k := range got {
		if k != "GOOD" {
			t.Errorf("只应提取 GOOD，实际还包括 %q", k)
		}
	}
	if len(got) != 1 {
		t.Errorf("应只提取 1 个键，实际 %v", got)
	}
}

// TestWriteEnvPermission 验证 .env 权限是 0600。
func TestWriteEnvPermission(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows 不使用 POSIX 权限位，0600 语义不适用")
	}
	w, _ := newTestWorkspace(t)

	path, written, err := w.WriteEnv("perm-test", "SECRET=abc\n")
	if err != nil {
		t.Fatal(err)
	}
	if !written {
		t.Fatal("应报告已写入")
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf(".env 权限应为 0600，实际 %04o（可能被同机其它用户读到）", perm)
	}

	// 覆盖写也必须仍是 0600：os.WriteFile 不会改已存在文件的权限，
	// 所以这里验证我们用的是"删除+O_EXCL 创建"而不是直接覆写。
	if _, _, err := w.WriteEnv("perm-test", "SECRET=def\n"); err != nil {
		t.Fatal(err)
	}
	info, err = os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("覆盖写后权限仍应为 0600，实际 %04o", perm)
	}
}

// TestWriteEnvEmptyRemoves 验证空内容删除文件。
//
// 留着旧文件会让"用户以为已清空的密钥"仍在磁盘上——
// 这是安全上最坏的一类不一致。
func TestWriteEnvEmptyRemoves(t *testing.T) {
	w, _ := newTestWorkspace(t)

	if _, _, err := w.WriteEnv("clear-test", "SECRET=abc\n"); err != nil {
		t.Fatal(err)
	}
	if !w.EnvExists("clear-test") {
		t.Fatal(".env 应存在")
	}

	path, written, err := w.WriteEnv("clear-test", "")
	if err != nil {
		t.Fatal(err)
	}
	if written {
		t.Error("空内容不应报告已写入")
	}
	if path != "" {
		t.Errorf("空内容不应返回路径，实际 %q", path)
	}
	if w.EnvExists("clear-test") {
		t.Error("空内容应删除 .env 文件")
	}

	// 幂等：再清一次不应报错（记录本来就没有 .env 也要能下发）。
	if _, _, err := w.WriteEnv("clear-test", ""); err != nil {
		t.Errorf("重复清除应幂等: %v", err)
	}
}

// TestWriteEnvAtomicNoTempLeftover 验证不留临时文件。
func TestWriteEnvAtomicNoTempLeftover(t *testing.T) {
	w, _ := newTestWorkspace(t)
	if _, _, err := w.WriteEnv("atomic-test", "A=1\n"); err != nil {
		t.Fatal(err)
	}
	dir, err := w.ProjectDir("atomic-test")
	if err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Errorf("不应留下临时文件: %s", e.Name())
		}
	}
}

// TestHashProjectCompatWithEmptyEnv 是兼容性的核心断言：
// env 为空时必须与旧算法 Hash(content) **完全相同**。
//
// 破了这条的后果：升级后所有既有编排会同时显示"配置已偏离"，
// 一次升级引发全量误报，真正的偏离反而被淹没。
func TestHashProjectCompatWithEmptyEnv(t *testing.T) {
	contents := []string{
		"",
		"services:\n  web:\n    image: nginx\n",
		"a",
		"含中文的内容\n",
	}
	for _, c := range contents {
		if got, want := HashProject(c, ""), Hash(c); got != want {
			t.Errorf("env 为空时应等于 Hash：HashProject=%q Hash=%q", got, want)
		}
	}
}

// TestHashProjectIncludesEnv 验证 .env 参与哈希。
//
// 只哈希 compose.yml 的话，用户改了 .env 后界面仍显示"配置一致"，
// 而实际运行的是旧变量——这类偏差最难排查。
func TestHashProjectIncludesEnv(t *testing.T) {
	const content = "services:\n  web:\n    image: ${TAG}\n"

	a := HashProject(content, "TAG=1.0\n")
	b := HashProject(content, "TAG=2.0\n")
	if a == b {
		t.Error("不同的 .env 必须得到不同的项目哈希")
	}
	// 与"只有 content"的哈希也要不同。
	if a == Hash(content) {
		t.Error("有 .env 时不应与仅有 content 的哈希相同")
	}
}

// TestHashProjectNoAmbiguity 验证长度前缀确实消除了拼接歧义。
//
// 没有长度前缀时，"a" + "bc" 与 "ab" + "c" 会拼成同一个字节串，
// 于是两份不同的项目得到同一个哈希——那会让"配置一致"的判定彻底失去意义。
func TestHashProjectNoAmbiguity(t *testing.T) {
	if HashProject("a", "bc") == HashProject("ab", "c") {
		t.Error("不同字段切分不应产生相同哈希（缺少长度前缀）")
	}
	if HashProject("ab", "c") == HashProject("abc", "") {
		t.Error("env 为空与非空之间不应碰撞")
	}
}

// TestHashProjectDeterministic 验证同输入同输出（跨进程也一致）。
func TestHashProjectDeterministic(t *testing.T) {
	const c, e = "services:\n  a:\n    image: x\n", "A=1\n"
	first := HashProject(c, e)
	for i := 0; i < 100; i++ {
		if got := HashProject(c, e); got != first {
			t.Fatalf("哈希不稳定：%q != %q", got, first)
		}
	}
	if len(first) != hashHexLen {
		t.Errorf("哈希长度应为 %d，实际 %d", hashHexLen, len(first))
	}
}

// TestApplyProjectWritesEnv 验证下发时 .env 一起落盘，且命令带 --env-file。
//
// 不真的执行 docker：把 Binary 指向一个不存在的路径，
// 这样"执行"必然立刻失败，但**所有落盘动作在此之前已完成**，
// 因此可以安全地断言文件状态。参数则通过 runnerFor 直接断言，不需要子进程。
//
// 这样设计的原因：真跑 `docker compose up` 会在有 docker 的机器上
// 真的启停容器（可能是生产容器的同名项目），测试绝不能有这个副作用。
func TestApplyProjectWritesEnv(t *testing.T) {
	m, _ := newSafeManager(t)

	spec := ProjectSpec{
		Name:       "env-apply",
		Content:    "services:\n  web:\n    image: nginx\n",
		EnvContent: "SECRET=abc\n",
	}
	spec.ExpectHash = HashProject(spec.Content, spec.EnvContent)

	if _, err := m.ApplyProject(context.Background(), spec); err != nil {
		t.Fatalf("下发失败: %v", err)
	}

	// .env 内容正确。
	envPath, err := m.ws.EnvPath("env-apply")
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(envPath)
	if err != nil {
		t.Fatalf("应写入 .env: %v", err)
	}
	if string(data) != spec.EnvContent {
		t.Errorf(".env 内容不符: %q", string(data))
	}

	// 命令必须显式带 --env-file 指向项目 .env，且顺序正确。
	r := m.runnerFor("env-apply")
	if r.EnvFile != envPath {
		t.Errorf("Runner.EnvFile 应指向项目 .env（%s），实际 %q", envPath, r.EnvFile)
	}
	args := composeArgs("env-apply", r.EnvFile, "up", "-d")
	if !containsString(args, "--env-file") {
		t.Errorf("命令应显式带 --env-file，实际 %v", args)
	}
	if !containsString(args, envPath) {
		t.Errorf("--env-file 应指向项目 .env（%s），实际 %v", envPath, args)
	}
	// 顺序：全局选项必须在子命令之前，否则 compose 报未知参数。
	idxEnv, idxUp := -1, -1
	for i, a := range args {
		if a == "--env-file" {
			idxEnv = i
		}
		if a == "up" {
			idxUp = i
		}
	}
	if idxEnv < 0 || idxUp < 0 || idxEnv > idxUp {
		t.Errorf("--env-file 必须位于 up 之前，实际 %v", args)
	}

	// .env 里定义的键必须从继承环境剔除，否则 .env 不生效。
	if !containsString(r.EnvExcludeKeys, "SECRET") {
		t.Errorf("SECRET 应出现在 EnvExcludeKeys 里，实际 %v", r.EnvExcludeKeys)
	}
}

// TestApplyProjectClearsEnv 验证空 env_content 会清除既有 .env。
func TestApplyProjectClearsEnv(t *testing.T) {
	m, _ := newSafeManager(t)

	// 先带 .env 下发。
	first := ProjectSpec{Name: "clear-env", Content: "services:\n  web:\n    image: nginx\n", EnvContent: "A=1\n"}
	first.ExpectHash = HashProject(first.Content, first.EnvContent)
	if _, err := m.ApplyProject(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	if !m.ws.EnvExists("clear-env") {
		t.Fatal(".env 应存在")
	}

	// 再下发空 env。
	second := ProjectSpec{Name: "clear-env", Content: first.Content, EnvContent: ""}
	second.ExpectHash = HashProject(second.Content, "")
	if _, err := m.ApplyProject(context.Background(), second); err != nil {
		t.Fatal(err)
	}
	if m.ws.EnvExists("clear-env") {
		t.Error("空 env_content 应删除 .env（否则旧密钥仍留在磁盘上）")
	}
	// 清除后命令行不应再带 --env-file。
	if r := m.runnerFor("clear-env"); r.EnvFile != "" {
		t.Errorf("清除 .env 后不应再传 --env-file，实际 %q", r.EnvFile)
	}
}

// TestApplyProjectHashMismatchRejectsWithoutWriting 验证哈希不符时**拒绝且不写盘**。
//
// 旧实现"先落盘再报错以便排查"是错的，而且危险：
// 落盘会覆盖**正在运行的项目**的配置，于是下一次 stop/restart
// 用的就是这份刚被判定为可疑的内容——用户看到"下发失败"，实际已经被改掉了。
// 排查所需信息（两个哈希值）在错误信息里已足够，不必去磁盘上看。
func TestApplyProjectHashMismatchRejectsWithoutWriting(t *testing.T) {
	m, _ := newSafeManager(t)

	// 先建立一个"正在运行"的项目状态。
	base := ProjectSpec{Name: "hash-mismatch", Content: "OLD-COMPOSE\n", EnvContent: "A=old\n"}
	base.ExpectHash = HashProject(base.Content, base.EnvContent)
	if _, err := m.ApplyProject(context.Background(), base); err != nil {
		t.Fatal(err)
	}

	spec := ProjectSpec{
		Name:       "hash-mismatch",
		Content:    "NEW-COMPOSE\n",
		EnvContent: "A=new\n",
		ExpectHash: "0000000000000000",
	}
	if _, err := m.ApplyProject(context.Background(), spec); err == nil {
		t.Fatal("哈希不符应返回错误")
	} else if !strings.Contains(err.Error(), "哈希") {
		t.Errorf("错误应说明哈希不符，实际 %q", err.Error())
	}

	// 磁盘上必须还是旧内容：既没写新 compose，也没写新 .env。
	cfg, err := m.ws.Read("hash-mismatch")
	if err != nil {
		t.Fatal(err)
	}
	if cfg != "OLD-COMPOSE\n" {
		t.Errorf("哈希不符不得改动 compose.yml，实际 %q", cfg)
	}
	env, err := m.ws.ReadEnv("hash-mismatch")
	if err != nil {
		t.Fatal(err)
	}
	if env != "A=old\n" {
		t.Errorf("哈希不符不得改动 .env，实际 %q", env)
	}
}

// TestApplyProjectRejectsBadEnv 验证非法 .env 在写盘前被拒。
//
// 落一份 compose 解析不了的 .env，会让该项目之后所有命令都失败，
// 包括本应能救场的 down/stop。
func TestApplyProjectRejectsBadEnv(t *testing.T) {
	m, _ := newTestManager(t)
	spec := ProjectSpec{
		Name:       "bad-env",
		Content:    "services:\n  web:\n    image: nginx\n",
		EnvContent: "这不是KEYVALUE\n",
	}
	spec.ExpectHash = HashProject(spec.Content, spec.EnvContent)

	if _, err := m.ApplyProject(context.Background(), spec); err == nil {
		t.Fatal("非法 .env 应被拒绝")
	}
	// 拒绝必须发生在写盘之前：不留下"有心跳但无凭据"的半成品。
	if m.ws.Exists("bad-env") {
		t.Error("非法 .env 不应留下 compose.yml")
	}
	if m.ws.EnvExists("bad-env") {
		t.Error("非法 .env 不应留下 .env")
	}
}

// TestApplyLegacySignatureStillWorks 验证旧签名未被破坏。
func TestApplyLegacySignatureStillWorks(t *testing.T) {
	m, _ := newSafeManager(t)

	const content = "services:\n  web:\n    image: nginx\n"
	if _, err := m.Apply(context.Background(), "legacy", content, Hash(content)); err != nil {
		t.Fatalf("旧签名应仍可用: %v", err)
	}
	if !m.ws.Exists("legacy") {
		t.Error("旧签名也应落盘 compose.yml")
	}
	// 旧签名等价于"没有 .env"：不应凭空创建。
	if m.ws.EnvExists("legacy") {
		t.Error("旧签名不应创建 .env")
	}
}

// ---- 环境隔离测试（本文件最重要的一组） ----

// TestRunnerExcludesAgentVars 验证 ARKADE_* 变量不会被传给 compose 子进程。
//
// 危害场景：编排里写了 ${ARKADE_AGENT_TOKEN}（恶意或笔误），
// 若子进程能读到该变量，它会被展开成 Hub 接入密钥，
// 写进容器配置、出现在 docker inspect、被容器内进程读走。
// 这不是理论风险——"只要写错一个变量名就泄漏"。
func TestRunnerExcludesAgentVars(t *testing.T) {
	t.Setenv("ARKADE_AGENT_TOKEN", "super-secret-token")
	t.Setenv("ARKADE_DATA_DIR", "/should/not/leak")

	r := &Runner{}
	env := r.environ()
	if hasEnvKey(env, "ARKADE_AGENT_TOKEN") {
		t.Error("ARKADE_AGENT_TOKEN 不能出现在 compose 子进程环境里")
	}
	if hasEnvKey(env, "ARKADE_DATA_DIR") {
		t.Error("ARKADE_* 变量一律不应传递")
	}
}

// TestRunnerExcludeKeysOverrideInherited 验证 .env 定义的键被从继承环境剔除。
//
// 这是让 .env 真正生效的关键：compose 的插值优先级是
// shell 环境 > .env。若 Agent 进程环境里已有同名变量，
// 用户在 .env 里写的值会被静默压住——改 .env "没有效果"且无报错。
func TestRunnerExcludeKeysOverrideInherited(t *testing.T) {
	t.Setenv("DB_PASSWORD", "agent-process-value")

	r := &Runner{EnvExcludeKeys: []string{"DB_PASSWORD"}}
	if hasEnvKey(r.environ(), "DB_PASSWORD") {
		t.Error("被 .env 定义的键必须从继承环境剔除，否则 .env 不生效")
	}
}

// TestRunnerKeepsRuntimeEssentials 验证运行必需的变量仍然传递。
//
// 过滤过度会直接弄坏功能：没有 PATH 连 docker 都找不到；
// 没有 DOCKER_HOST 会连到默认套接字（"Agent 能连、compose 连不上"）；
// 丢代理变量会让拉镜像失败。
func TestRunnerKeepsRuntimeEssentials(t *testing.T) {
	t.Setenv("PATH", os.Getenv("PATH"))
	t.Setenv("HOME", os.Getenv("HOME"))
	t.Setenv("HTTP_PROXY", "http://proxy.example:8080")

	r := &Runner{DockerHost: "tcp://custom:2375"}
	env := r.environ()

	for _, key := range []string{"PATH", "HOME", "HTTP_PROXY"} {
		if !hasEnvKey(env, key) {
			t.Errorf("%s 应保留（过滤过度会弄坏功能）", key)
		}
	}
	// DOCKER_HOST 必须是我们的值，且只有一份。
	count := 0
	for _, kv := range env {
		if k, v, ok := strings.Cut(kv, "="); ok && k == "DOCKER_HOST" {
			count++
			if v != "tcp://custom:2375" {
				t.Errorf("DOCKER_HOST 应为自定义值，实际 %q", v)
			}
		}
	}
	if count != 1 {
		t.Errorf("DOCKER_HOST 应恰好出现一次，实际 %d 次（重复会导致行为不确定）", count)
	}
}

// TestComposeArgsOrdering 验证 --env-file 位于子命令之前。
//
// --env-file 是 `docker compose` 的全局选项，放在 up 之后会被当成
// 未知参数而报错（表现为"编排起不来"，但错误信息与配置无关，很难联想）。
func TestComposeArgsOrdering(t *testing.T) {
	args := composeArgs("proj", "/data/stacks/proj/.env", "up", "-d")
	want := []string{"compose", "--project-name", "proj", "--env-file", "/data/stacks/proj/.env", "up", "-d"}
	if len(args) != len(want) {
		t.Fatalf("期望 %v，实际 %v", want, args)
	}
	for i := range want {
		if args[i] != want[i] {
			t.Fatalf("第 %d 项期望 %q 实际 %q（顺序错误会让 compose 报未知参数）", i, want[i], args[i])
		}
	}

	// 没有 .env 时不应出现 --env-file。
	if got := composeArgs("proj", "", "up", "-d"); containsString(got, "--env-file") {
		t.Errorf("没有 .env 时不应传 --env-file，实际 %v", got)
	}
}

// ---- 辅助 ----

// hasEnvKey 判断环境变量列表里是否有某个键。
func hasEnvKey(env []string, key string) bool {
	prefix := key + "="
	for _, kv := range env {
		if strings.HasPrefix(kv, prefix) {
			return true
		}
	}
	return false
}

// containsString 判断字符串切片里是否有某元素。
func containsString(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// newSafeManager 构造一个**绝不会真的执行 docker** 的 Manager，
// 并把配置校验桩设为"通过"。
//
// Binary 指向临时目录里一个不存在的路径：exec 会立刻失败，
// 因此落盘断言安全、跨平台、且不可能误启停宿主机上的容器。
//
// 校验必须一起打桩：真实校验要跑 `docker compose config -q`，
// 而测试环境里没有 docker。若不打桩，这些"落盘顺序/文件状态"的测试
// 会全部因为环境缺失而失败——被测逻辑其实是对的。
// "校验失败必须拒绝写入"这条规则由 precheck_test.go 专门覆盖。
func newSafeManager(t *testing.T) (*Manager, string) {
	t.Helper()
	m, dir := newTestManager(t)
	m.runner.Binary = filepath.Join(dir, "no-such-docker-binary")
	m.applier = func(context.Context, string, string, string) PrecheckResult {
		return PrecheckResult{OK: true}
	}
	return m, dir
}

// readCapturedArgs 已移除：参数断言改为直接检查 runnerFor 的产物，
// 不再依赖外部可执行文件，因此可在 Windows 上运行。
