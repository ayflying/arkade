package compose

// workspace_test.go 覆盖编排文件落盘与路径安全。
//
// 重点是「项目名不可能把文件写到数据目录之外」——这是整个 Agent 侧
// 最危险的攻击面（Hub 下发的项目名最终会变成文件路径）。

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// newTestWorkspace 构造指向临时目录的工作区。
func newTestWorkspace(t *testing.T) (*Workspace, string) {
	t.Helper()
	dir := t.TempDir()
	return &Workspace{DataDir: dir}, dir
}

// TestWriteAndRead 验证写入后能读回原文。
func TestWriteAndRead(t *testing.T) {
	ws, dir := newTestWorkspace(t)
	content := "services:\n  web:\n    image: nginx\n"

	path, err := ws.Write("web", content)
	if err != nil {
		t.Fatalf("写入失败: %v", err)
	}
	want := filepath.Join(dir, "stacks", "web", "compose.yml")
	if path != want {
		t.Errorf("路径应为 %s，实际 %s", want, path)
	}

	got, err := ws.Read("web")
	if err != nil {
		t.Fatalf("读取失败: %v", err)
	}
	if got != content {
		t.Errorf("内容应原样保留，实际 %q", got)
	}
}

// TestWriteRejectsTraversal 验证写入路径穿越的项目名被拒，
// 且**数据目录外没有产生任何文件**。
func TestWriteRejectsTraversal(t *testing.T) {
	ws, dir := newTestWorkspace(t)

	bad := []string{"../escape", "..", "a/b", "a\\b", "/etc/passwd", "a..b"}
	for _, name := range bad {
		if _, err := ws.Write(name, "x"); err == nil {
			t.Errorf("%q 应被拒绝写入", name)
		}
	}

	// 关键断言：数据目录的父目录里不能出现任何新东西。
	parent := filepath.Dir(dir)
	entries, err := os.ReadDir(parent)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.Contains(e.Name(), "escape") {
			t.Fatalf("穿越写入了 %s", e.Name())
		}
	}
}

// TestComposePathRejectsTraversal 验证取路径时同样拒绝。
func TestComposePathRejectsTraversal(t *testing.T) {
	ws, _ := newTestWorkspace(t)
	for _, name := range []string{"../x", "a/b", "", ".", ".."} {
		if _, err := ws.ComposePath(name); err == nil {
			t.Errorf("%q 应被拒绝", name)
		}
	}
}

// TestReadMissing 验证读取不存在的编排给出明确中文错误。
func TestReadMissing(t *testing.T) {
	ws, _ := newTestWorkspace(t)
	_, err := ws.Read("ghost")
	if err == nil {
		t.Fatal("应报错")
	}
	if !strings.Contains(err.Error(), "不存在") {
		t.Errorf("错误应为中文说明，实际 %q", err.Error())
	}
}

// TestExists 验证存在性判断。
func TestExists(t *testing.T) {
	ws, _ := newTestWorkspace(t)
	if ws.Exists("web") {
		t.Error("未写入前不应存在")
	}
	if _, err := ws.Write("web", "x"); err != nil {
		t.Fatal(err)
	}
	if !ws.Exists("web") {
		t.Error("写入后应存在")
	}
	// 非法名一律视为不存在，不走文件系统。
	if ws.Exists("../etc") {
		t.Error("非法名应视为不存在")
	}
}

// TestListProjects 验证列表按名称升序，且跳过非法目录与普通文件。
func TestListProjects(t *testing.T) {
	ws, dir := newTestWorkspace(t)

	for _, n := range []string{"zeta", "alpha", "mid"} {
		if _, err := ws.Write(n, "services: {}\n"); err != nil {
			t.Fatal(err)
		}
	}
	// 手工造一个非法目录名与一个普通文件，它们都应被跳过。
	if err := os.MkdirAll(filepath.Join(dir, "stacks", "Bad Name"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "stacks", "loose.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	list, err := ws.List()
	if err != nil {
		t.Fatalf("列表失败: %v", err)
	}
	if len(list) != 3 {
		t.Fatalf("应返回 3 个项目，实际 %d: %+v", len(list), list)
	}
	want := []string{"alpha", "mid", "zeta"}
	for i, n := range want {
		if list[i].Name != n {
			t.Errorf("第 %d 个应为 %s，实际 %s", i, n, list[i].Name)
		}
		if !list[i].HasComposeFile {
			t.Errorf("%s 应标记为有 compose.yml", n)
		}
		if list[i].UpdatedAt.IsZero() {
			t.Errorf("%s 应带修改时间", n)
		}
	}
}

// TestListEmpty 验证数据目录不存在时返回空列表而不是错误。
func TestListEmpty(t *testing.T) {
	ws, _ := newTestWorkspace(t)
	list, err := ws.List()
	if err != nil {
		t.Fatalf("空目录不应报错: %v", err)
	}
	if len(list) != 0 {
		t.Errorf("应为空，实际 %d", len(list))
	}
}

// TestListMissingComposeFile 验证目录存在但缺 compose.yml 时被标出来。
func TestListMissingComposeFile(t *testing.T) {
	ws, dir := newTestWorkspace(t)
	if err := os.MkdirAll(filepath.Join(dir, "stacks", "halfway"), 0o755); err != nil {
		t.Fatal(err)
	}
	list, err := ws.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 {
		t.Fatalf("应有 1 项，实际 %d", len(list))
	}
	if list[0].HasComposeFile {
		t.Error("缺 compose.yml 时应标记 HasComposeFile=false")
	}
}

// TestRemove 验证删除目录。
func TestRemove(t *testing.T) {
	ws, dir := newTestWorkspace(t)
	if _, err := ws.Write("web", "x"); err != nil {
		t.Fatal(err)
	}
	if err := ws.Remove("web"); err != nil {
		t.Fatalf("删除失败: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "stacks", "web")); !os.IsNotExist(err) {
		t.Error("目录应已删除")
	}
	// 删除不存在的项目不报错（幂等）。
	if err := ws.Remove("web"); err != nil {
		t.Errorf("重复删除应幂等: %v", err)
	}
}

// TestWriteAtomicNoTempLeftover 验证写入后不残留临时文件。
func TestWriteAtomicNoTempLeftover(t *testing.T) {
	ws, dir := newTestWorkspace(t)
	if _, err := ws.Write("web", "v1"); err != nil {
		t.Fatal(err)
	}
	if _, err := ws.Write("web", "v2"); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(filepath.Join(dir, "stacks", "web"))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Errorf("不应残留临时文件 %s", e.Name())
		}
	}
	got, _ := ws.Read("web")
	if got != "v2" {
		t.Errorf("应读到最后一次写入的内容，实际 %q", got)
	}
}
