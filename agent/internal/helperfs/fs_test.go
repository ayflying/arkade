package helperfs

// 用真实的临时目录做安全测试：这些用例直接操作 os.Root，
// 与 helper 容器内跑的是同一份代码路径。

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// newRoot 建一个临时卷根。
func newRoot(t *testing.T) (*os.Root, string) {
	t.Helper()
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatalf("打开根目录失败: %v", err)
	}
	t.Cleanup(func() { _ = root.Close() })
	return root, dir
}

func b64(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }

func sha(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// requireLinux 跳过依赖 Linux 语义的用例。
//
// 这些用例在 Windows 上会**按设计失败**（fail closed）：
//   - nlink 取不到 → 一律按"可能有多硬链接"拒绝；
//   - 没有 renameat2(RENAME_NOREPLACE) → 明确不支持原子改名。
//
// 那不是缺陷，所以这里显式跳过而不是放宽实现。
// 真实的 helper 只在 Linux 容器内运行，Linux CI/真机会真正执行它们。
func requireLinux(t *testing.T) {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("该用例依赖 Linux 语义（nlink / renameat2），在非 Linux 上按设计 fail closed")
	}
}

func TestWriteReadRoundTrip(t *testing.T) {
	requireLinux(t)
	requireLinux(t)
	root, dir := newRoot(t)
	if _, err := Do(root, Request{Operation: OpWrite, Path: "a.txt", DataBase64: b64("hello"), CreateOnly: true}); err != nil {
		t.Fatalf("写入失败: %v", err)
	}
	// 真的落到磁盘上了。
	if data, err := os.ReadFile(filepath.Join(dir, "a.txt")); err != nil || string(data) != "hello" {
		t.Fatalf("落盘内容不符: %q %v", data, err)
	}

	res, err := Do(root, Request{Operation: OpRead, Path: "a.txt"})
	if err != nil {
		t.Fatalf("读取失败: %v", err)
	}
	raw, _ := base64.StdEncoding.DecodeString(res.DataBase64)
	if string(raw) != "hello" || res.SHA256 != sha("hello") || res.Size != 5 {
		t.Fatalf("读取结果不符: %+v", res)
	}
}

func TestMkdirListDeleteEmpty(t *testing.T) {
	requireLinux(t)
	root, dir := newRoot(t)
	if _, err := Do(root, Request{Operation: OpMkdir, Path: "sub"}); err != nil {
		t.Fatalf("建目录失败: %v", err)
	}
	if st, err := os.Stat(filepath.Join(dir, "sub")); err != nil || !st.IsDir() {
		t.Fatalf("目录未创建: %v", err)
	}
	res, err := Do(root, Request{Operation: OpList, Path: ""})
	if err != nil {
		t.Fatalf("列目录失败: %v", err)
	}
	if len(res.Entries) != 1 || res.Entries[0].Kind != "directory" {
		t.Fatalf("目录项不符: %+v", res.Entries)
	}
	if _, err := Do(root, Request{Operation: OpDelete, Path: "sub"}); err != nil {
		t.Fatalf("删除空目录失败: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "sub")); !os.IsNotExist(err) {
		t.Fatalf("目录应被删除")
	}
}

// TestDeleteNonEmptyDirRejected 验证非空目录拒绝删除（绝不递归）。
func TestDeleteNonEmptyDirRejected(t *testing.T) {
	requireLinux(t)
	root, dir := newRoot(t)
	if err := os.MkdirAll(filepath.Join(dir, "d"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "d", "keep.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Do(root, Request{Operation: OpDelete, Path: "d"}); err == nil {
		t.Fatalf("非空目录必须拒绝删除")
	}
	if _, err := os.Stat(filepath.Join(dir, "d", "keep.txt")); err != nil {
		t.Fatalf("文件被误删，递归删除发生了: %v", err)
	}
}

// TestPathTraversalRejected 验证各类越界路径被拒。
func TestPathTraversalRejected(t *testing.T) {
	requireLinux(t)
	root, _ := newRoot(t)
	// 在卷外放一个哨兵文件，确认没有任何路径能碰到它。
	outside := filepath.Join(t.TempDir(), "sentinel.txt")
	if err := os.WriteFile(outside, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}

	bad := []string{
		"../sentinel.txt", "/etc/passwd", "a/../../x", "./x", "a//b",
		"..", ".", "a\\b", "x\x00y",
	}
	for _, p := range bad {
		_, err := Do(root, Request{Operation: OpRead, Path: p})
		if err == nil {
			t.Fatalf("路径 %q 应被拒绝", p)
		}
		// list 的根路径是合法的，单独放行；其它必须报 bad path。
	}
	// 写入也不得逃出。
	if _, err := Do(root, Request{Operation: OpWrite, Path: "../escaped.txt", DataBase64: b64("x")}); err == nil {
		t.Fatalf("写越界路径必须被拒")
	}
}

// TestSymlinkRejected 验证符号链接被拒（读、写、删）。
func TestSymlinkRejected(t *testing.T) {
	requireLinux(t)
	if os.PathSeparator != '/' {
		t.Skip("Windows 创建符号链接需要额外权限")
	}
	root, dir := newRoot(t)
	target := filepath.Join(dir, "real.txt")
	if err := os.WriteFile(target, []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(dir, "link.txt")); err != nil {
		t.Skipf("无法创建符号链接: %v", err)
	}

	if _, err := Do(root, Request{Operation: OpRead, Path: "link.txt"}); err == nil {
		t.Fatalf("读符号链接必须被拒")
	}
	if _, err := Do(root, Request{Operation: OpWrite, Path: "link.txt", DataBase64: b64("evil")}); err == nil {
		t.Fatalf("写符号链接必须被拒")
	}
	if _, err := Do(root, Request{Operation: OpDelete, Path: "link.txt"}); err == nil {
		t.Fatalf("删符号链接必须被拒")
	}
	// 目标文件内容必须完好。
	if data, _ := os.ReadFile(target); string(data) != "data" {
		t.Fatalf("链接目标被改写: %q", data)
	}
}

// TestListShowsSymlinkKind 验证 list 能显示 symlink 但标记出来。
func TestListShowsSymlinkKind(t *testing.T) {
	requireLinux(t)
	if os.PathSeparator != '/' {
		t.Skip("Windows 创建符号链接需要额外权限")
	}
	root, dir := newRoot(t)
	if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("f.txt", filepath.Join(dir, "ln")); err != nil {
		t.Skipf("无法创建符号链接: %v", err)
	}
	res, err := Do(root, Request{Operation: OpList, Path: ""})
	if err != nil {
		t.Fatalf("列目录失败: %v", err)
	}
	var sawLink bool
	for _, e := range res.Entries {
		if e.Name == "ln" && e.Kind == "symlink" {
			sawLink = true
		}
	}
	if !sawLink {
		t.Fatalf("list 应显示 symlink 并标记 kind: %+v", res.Entries)
	}
}

// TestCreateOnlyRejectsOverwrite 验证 CreateOnly 拒绝覆盖。
func TestCreateOnlyRejectsOverwrite(t *testing.T) {
	requireLinux(t)
	requireLinux(t)
	root, dir := newRoot(t)
	if _, err := Do(root, Request{Operation: OpWrite, Path: "f.txt", DataBase64: b64("v1"), CreateOnly: true}); err != nil {
		t.Fatalf("首次写入失败: %v", err)
	}
	if _, err := Do(root, Request{Operation: OpWrite, Path: "f.txt", DataBase64: b64("v2"), CreateOnly: true}); err == nil {
		t.Fatalf("CreateOnly 必须拒绝覆盖")
	}
	if data, _ := os.ReadFile(filepath.Join(dir, "f.txt")); string(data) != "v1" {
		t.Fatalf("原内容被改写: %q", data)
	}
}

// TestOverwriteRequiresChecksum 验证覆盖必须带 expected_sha256 且必须匹配。
func TestOverwriteRequiresChecksum(t *testing.T) {
	requireLinux(t)
	requireLinux(t)
	root, dir := newRoot(t)
	if _, err := Do(root, Request{Operation: OpWrite, Path: "f.txt", DataBase64: b64("v1"), CreateOnly: true}); err != nil {
		t.Fatalf("首次写入失败: %v", err)
	}
	// 不带校验和：拒绝。
	if _, err := Do(root, Request{Operation: OpWrite, Path: "f.txt", DataBase64: b64("v2")}); err == nil {
		t.Fatalf("覆盖必须要求校验和")
	}
	// 带错误校验和：拒绝。
	if _, err := Do(root, Request{Operation: OpWrite, Path: "f.txt",
		DataBase64: b64("v2"), ExpectedSHA256: sha("wrong")}); err == nil {
		t.Fatalf("校验和不匹配必须拒绝")
	}
	if data, _ := os.ReadFile(filepath.Join(dir, "f.txt")); string(data) != "v1" {
		t.Fatalf("内容被误改: %q", data)
	}
	// 带正确校验和：成功。
	if _, err := Do(root, Request{Operation: OpWrite, Path: "f.txt",
		DataBase64: b64("v2"), ExpectedSHA256: sha("v1")}); err != nil {
		t.Fatalf("校验和正确时应允许覆盖: %v", err)
	}
	if data, _ := os.ReadFile(filepath.Join(dir, "f.txt")); string(data) != "v2" {
		t.Fatalf("覆盖未生效: %q", data)
	}
}

// TestRenameRejectsOverwriteAndRoot 验证改名不覆盖、不动根。
func TestRenameRejectsOverwriteAndRoot(t *testing.T) {
	requireLinux(t)
	requireLinux(t)
	root, dir := newRoot(t)
	if _, err := Do(root, Request{Operation: OpWrite, Path: "a.txt", DataBase64: b64("a"), CreateOnly: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := Do(root, Request{Operation: OpWrite, Path: "b.txt", DataBase64: b64("b"), CreateOnly: true}); err != nil {
		t.Fatal(err)
	}
	// 目标已存在：拒绝。
	if _, err := Do(root, Request{Operation: OpRename, Path: "a.txt", Destination: "b.txt"}); err == nil {
		t.Fatalf("改名到已存在目标必须拒绝")
	}
	if data, _ := os.ReadFile(filepath.Join(dir, "b.txt")); string(data) != "b" {
		t.Fatalf("目标被覆盖: %q", data)
	}
	// 改名为空目标：拒绝。
	if _, err := Do(root, Request{Operation: OpRename, Path: "a.txt", Destination: ""}); err == nil {
		t.Fatalf("改名为根必须拒绝")
	}
	// 正常改名。
	if _, err := Do(root, Request{Operation: OpRename, Path: "a.txt", Destination: "c.txt"}); err != nil {
		t.Fatalf("正常改名失败: %v", err)
	}
}

// TestListRejectsInRootSymlinkDir 验证"指向卷内"的符号链接目录同样被拒。
//
// 这是一个容易漏的边界：os.Root 只保证不逃出根目录，
// 所以一个指向 /data 内部其它目录的链接**能通过 root.Open**。
// 若不显式拒绝，list "link" 会静默列出 target 的内容，
// 与"拒绝符号链接"的契约矛盾，用户也无法察觉自己看的不是那个名字。
func TestListRejectsInRootSymlinkDir(t *testing.T) {
	requireLinux(t)
	root, dir := newRoot(t)
	real := filepath.Join(dir, "real")
	if err := os.MkdirAll(real, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(real, "inside.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	// 卷**内部**的符号链接：目标没有逃出根目录。
	if err := os.Symlink("real", filepath.Join(dir, "linkdir")); err != nil {
		t.Skipf("无法创建符号链接: %v", err)
	}

	// list 该链接目录必须被拒（不是列出 real 的内容）。
	if _, err := Do(root, Request{Operation: OpList, Path: "linkdir"}); err == nil {
		t.Fatal("指向卷内的符号链接目录也必须拒绝 list")
	}
	// 对照：真实目录可以正常列出。
	res, err := Do(root, Request{Operation: OpList, Path: "real"})
	if err != nil {
		t.Fatalf("真实目录应可列出: %v", err)
	}
	if len(res.Entries) != 1 || res.Entries[0].Name != "inside.txt" {
		t.Fatalf("目录内容不符: %+v", res.Entries)
	}
}

// TestListMissingPathKeepsNotFound 验证不存在的目录仍走 not_found 类别。
func TestListMissingPathKeepsNotFound(t *testing.T) {
	requireLinux(t)
	root, _ := newRoot(t)
	_, err := Do(root, Request{Operation: OpList, Path: "nope"})
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("期望 os.ErrNotExist，实际 %v", err)
	}
}

// TestSizeLimit 验证 4MiB 上限。
func TestSizeLimit(t *testing.T) {
	requireLinux(t)
	root, _ := newRoot(t)
	big := strings.Repeat("A", MaxFileBytes+1)
	if _, err := Do(root, Request{Operation: OpWrite, Path: "big.txt", DataBase64: b64(big), CreateOnly: true}); err == nil {
		t.Fatalf("超过 4MiB 的写入必须拒绝")
	}
}

// TestListTooManyEntries 验证超过 2000 项时报错而不是静默截断。
func TestListTooManyEntries(t *testing.T) {
	requireLinux(t)
	root, dir := newRoot(t)
	for i := 0; i < MaxListEntries+1; i++ {
		if err := os.WriteFile(filepath.Join(dir, "f"+itoa(i)), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := Do(root, Request{Operation: OpList, Path: ""}); err == nil {
		t.Fatalf("超过 2000 项必须报错（不能静默截断）")
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

// TestUnsupportedOperation 验证未知操作被拒。
func TestUnsupportedOperation(t *testing.T) {
	requireLinux(t)
	root, _ := newRoot(t)
	if _, err := Do(root, Request{Operation: "exec"}); err == nil {
		t.Fatalf("未知操作必须被拒")
	}
}
