package compose

// atomic_test.go 覆盖"compose.yml 与 .env 一起提交"的原子性。
//
// 为什么值得单独一组测试：
// 两个文件共同决定哈希与运行结果。若其中一个失败而另一个成功，
// 留在磁盘上的组合**从来没有被任何哈希校验过**——
// 它既不是旧状态也不是新状态，且不会有任何报错。
// 具体危害：用户把镜像 tag 改到 2.0 并写进 .env，
// 若 .env 失败而 compose.yml 成功，服务会用 1.0 的变量配 2.0 的编排启动。

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestCommitProjectWritesBoth 验证两个文件一起落盘。
func TestCommitProjectWritesBoth(t *testing.T) {
	w, _ := newTestWorkspace(t)

	cfgPath, envPath, err := w.CommitProject("both", "services:\n  a:\n    image: x\n", "A=1\n")
	if err != nil {
		t.Fatal(err)
	}
	if cfgPath == "" || envPath == "" {
		t.Fatalf("两个路径都应返回，实际 cfg=%q env=%q", cfgPath, envPath)
	}
	cfg, _ := os.ReadFile(cfgPath)
	env, _ := os.ReadFile(envPath)
	if string(cfg) != "services:\n  a:\n    image: x\n" {
		t.Errorf("compose.yml 内容不符: %q", cfg)
	}
	if string(env) != "A=1\n" {
		t.Errorf(".env 内容不符: %q", env)
	}
}

// TestCommitProjectReplacesBoth 验证覆盖时两个文件都更新（不残留旧内容）。
func TestCommitProjectReplacesBoth(t *testing.T) {
	w, _ := newTestWorkspace(t)

	if _, _, err := w.CommitProject("replace", "old\n", "A=old\n"); err != nil {
		t.Fatal(err)
	}
	cfgPath, envPath, err := w.CommitProject("replace", "new\n", "A=new\n")
	if err != nil {
		t.Fatal(err)
	}
	cfg, _ := os.ReadFile(cfgPath)
	env, _ := os.ReadFile(envPath)
	if string(cfg) != "new\n" {
		t.Errorf("compose.yml 应已更新，实际 %q", cfg)
	}
	if string(env) != "A=new\n" {
		t.Errorf(".env 应已更新，实际 %q", env)
	}
}

// TestCommitProjectEmptyEnvRemovesFile 验证空 env 会删除 .env（不留旧密钥）。
func TestCommitProjectEmptyEnvRemovesFile(t *testing.T) {
	w, _ := newTestWorkspace(t)

	if _, _, err := w.CommitProject("clearboth", "services: {}\n", "SECRET=abc\n"); err != nil {
		t.Fatal(err)
	}
	cfgPath, envPath, err := w.CommitProject("clearboth", "services: {}\n", "")
	if err != nil {
		t.Fatal(err)
	}
	if envPath != "" {
		t.Errorf("空 env 不应返回 .env 路径，实际 %q", envPath)
	}
	if w.EnvExists("clearboth") {
		t.Error("空 env 应删除 .env 文件（否则旧密钥仍留在磁盘上）")
	}
	// compose.yml 仍然更新。
	if data, _ := os.ReadFile(cfgPath); string(data) != "services: {}\n" {
		t.Errorf("compose.yml 应存在且内容正确，实际 %q", data)
	}
}

// TestCommitProjectNoTempLeftover 验证不残留临时文件。
//
// 两次 rename 之间可能出现".tmp 残留"；残留本身不致命，
// 但会让下次写入遇到 O_EXCL 冲突，所以必须清理。
func TestCommitProjectNoTempLeftover(t *testing.T) {
	w, _ := newTestWorkspace(t)

	if _, _, err := w.CommitProject("notmp", "services: {}\n", "A=1\n"); err != nil {
		t.Fatal(err)
	}
	dir, err := w.ProjectDir("notmp")
	if err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		name := e.Name()
		if filepath.Ext(name) == ".tmp" || filepath.Ext(name) == ".rollback" {
			t.Errorf("不应残留临时文件: %s", name)
		}
	}
}

// TestCommitProjectUnwritableDirKeepsOldState 验证写入失败时旧状态完好。
//
// 这是"绝不半写"的核心断言：失败后磁盘上必须是**原来的完整旧状态**，
// 而不是新 compose.yml + 旧 .env。
//
// 制造失败的方式：把 compose.yml 的**目标路径**占成一个非空目录。
// 这样临时的写文件都成功，只有最后的 rename 会失败——
// 正好覆盖"第 1 个 rename 失败"这条路径。
// （早先版本用固定名 <.tmp> 占位，临时名改成唯一后那种手法已不适用。）
func TestCommitProjectUnwritableDirKeepsOldState(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows 不允许删除/替换被占用的目录，失败方式与 POSIX 不同")
	}
	w, _ := newTestWorkspace(t)

	// 先建立旧状态。
	if _, _, err := w.CommitProject("rollback", "OLD-COMPOSE\n", "A=old\n"); err != nil {
		t.Fatal(err)
	}

	dir, err := w.ProjectDir("rollback")
	if err != nil {
		t.Fatal(err)
	}
	// 让 compose.yml 的目标路径无法被 rename 覆盖：把它换成非空目录。
	cfgTarget := filepath.Join(dir, composeFileName)
	if err := os.Remove(cfgTarget); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(cfgTarget, "occupied"), 0o755); err != nil {
		t.Fatal(err)
	}

	if _, _, err := w.CommitProject("rollback", "NEW-COMPOSE\n", "A=new\n"); err == nil {
		t.Fatal("写入应失败")
	}

	// .env 必须还是旧内容：失败发生在 compose.yml 那一步，绝不能走到 .env。
	env, _ := w.ReadEnv("rollback")
	if env != "A=old\n" {
		t.Errorf("失败后 .env 应保持旧内容，实际 %q（出现半写！）", env)
	}
}

// TestCommitProjectSecondRenameFailsRollsBack 验证第 2 个 rename 失败时回滚 compose.yml。
//
// 这是"绝不半写"的另一半：.env 提交失败时，compose.yml 必须被恢复成旧内容，
// 否则就留下"新编排 + 旧 env"——一个从未被哈希校验过的组合。
func TestCommitProjectSecondRenameFailsRollsBack(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows 不允许删除/替换被占用的目录，失败方式与 POSIX 不同")
	}
	w, _ := newTestWorkspace(t)
	if _, _, err := w.CommitProject("rollback2", "OLD-COMPOSE\n", "A=old\n"); err != nil {
		t.Fatal(err)
	}
	dir, err := w.ProjectDir("rollback2")
	if err != nil {
		t.Fatal(err)
	}
	// 让 .env 的 rename 失败（目标是非空目录）。
	envTarget := filepath.Join(dir, envFileName)
	if err := os.Remove(envTarget); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(envTarget, "occupied"), 0o755); err != nil {
		t.Fatal(err)
	}

	if _, _, err := w.CommitProject("rollback2", "NEW-COMPOSE\n", "A=new\n"); err == nil {
		t.Fatal("写入应失败")
	}

	// compose.yml 必须回滚成旧内容。
	cfg, _ := os.ReadFile(filepath.Join(dir, composeFileName))
	if string(cfg) != "OLD-COMPOSE\n" {
		t.Errorf("第 2 个 rename 失败后 compose.yml 应回滚为旧内容，实际 %q（出现半写！）", cfg)
	}
}

// TestCommitTempNameUnique 验证临时文件名唯一。
//
// 唯一性不是洁癖：早先用固定名 `<target>.tmp` 并"清理残留"，
// 并发时会删掉别人正在写的临时文件，导致 rename 拿到空文件，
// 而对方已经报告成功。唯一名保证**绝不互删**。
func TestCommitTempNameUnique(t *testing.T) {
	target := filepath.Join("x", composeFileName)
	seen := make(map[string]bool, 200)
	for i := 0; i < 200; i++ {
		n := commitTempName(target)
		if seen[n] {
			t.Fatalf("临时文件名重复: %s", n)
		}
		seen[n] = true
		if !strings.Contains(n, ".tmp.") {
			t.Errorf("临时文件名应含 .tmp. 标记，实际 %s", n)
		}
	}
}

// TestProjectHashIncludesEnv 验证 List/Status 的哈希包含 .env。
//
// 只哈希 compose.yml 的话，用户只改 .env（换镜像 tag、改密码）时
// 界面会一直显示"配置一致"，而实际运行的东西已经变了。
func TestProjectHashIncludesEnv(t *testing.T) {
	m, _ := newSafeManager(t)

	spec := ProjectSpec{Name: "hash-env", Content: "services:\n  web:\n    image: nginx\n", EnvContent: "TAG=1.0\n"}
	spec.ExpectHash = HashProject(spec.Content, spec.EnvContent)
	if _, err := m.ApplyProject(t.Context(), spec); err != nil {
		t.Fatal(err)
	}

	st, err := m.Status(t.Context(), "hash-env")
	if err != nil {
		t.Fatal(err)
	}
	if st.Hash != HashProject(spec.Content, spec.EnvContent) {
		t.Errorf("状态哈希应等于联合哈希 %q，实际 %q（只算了 compose.yml？）",
			HashProject(spec.Content, spec.EnvContent), st.Hash)
	}

	// 只改 .env（compose.yml 不变）→ 哈希必须变化。
	if _, _, err := m.ws.CommitProject("hash-env", spec.Content, "TAG=2.0\n"); err != nil {
		t.Fatal(err)
	}
	st2, err := m.Status(t.Context(), "hash-env")
	if err != nil {
		t.Fatal(err)
	}
	if st2.Hash == st.Hash {
		t.Error("只改 .env 也必须让哈希变化，否则界面会显示'一致'而实际已变")
	}

	// List 也必须用联合哈希。
	list, err := m.List(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range list {
		if p.Name == "hash-env" && p.Hash != st2.Hash {
			t.Errorf("List 的哈希应与 Status 一致（都用联合哈希），实际 %q vs %q", p.Hash, st2.Hash)
		}
	}
}
