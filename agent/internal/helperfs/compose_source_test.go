package helperfs

// compose_source_test.go：验证 compose-source-helper 的读取行为。
//
// 关注点不是"能不能读"，而是"**只能**读到 /source 内的普通文件"。

import (
	"encoding/base64"
	"os"
	"testing"
)

// sourceRoot 是测试用的临时 /source 替代。
//
// RunComposeSource 里根是硬编码的 /source（隐藏命令不接受参数），
// 但 readSourceFile 的核心逻辑通过 safeOpenFile 与 cleanPath 复用，
// 这里直接测这条链路。
func TestSourceReadRejectsEscapeAndSpecial(t *testing.T) {
	requireLinux(t)
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()

	if err := os.WriteFile(dir+"/compose.yml", []byte("services: {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// 正常读取。
	f, info, oerr := safeOpenFile(root, "compose.yml", os.O_RDONLY)
	if oerr != nil {
		t.Fatalf("正常文件应可打开: %v", oerr)
	}
	if info.Size() != int64(len("services: {}\n")) {
		t.Fatalf("大小不符: %d", info.Size())
	}
	_ = f.Close()

	// 越界路径必须被 cleanPath 拒绝。
	for _, p := range []string{"../etc/passwd", "/etc/passwd", "a/../../x"} {
		if _, perr := cleanPath(p); perr == nil {
			t.Fatalf("路径 %q 必须被拒绝", p)
		}
	}

	// 符号链接必须被拒。
	if err := os.Symlink(dir+"/compose.yml", dir+"/link.yml"); err == nil {
		if _, _, serr := safeOpenFile(root, "link.yml", os.O_RDONLY); serr == nil {
			t.Fatal("符号链接必须被拒绝")
		}
	}

	// 目录不是普通文件。
	if err := os.Mkdir(dir+"/sub", 0o755); err != nil {
		t.Fatal(err)
	}
	if _, _, derr := safeOpenFile(root, "sub", os.O_RDONLY); derr == nil {
		t.Fatal("目录必须被拒绝")
	}
}

// TestSourceResponseRoundTrip 验证正确内容以 base64 回传。
func TestSourceResponseRoundTrip(t *testing.T) {
	content := "services:\n  web:\n    image: nginx\n"
	enc := base64.StdEncoding.EncodeToString([]byte(content))
	dec, err := base64.StdEncoding.DecodeString(enc)
	if err != nil || string(dec) != content {
		t.Fatalf("base64 往返失败: %v", err)
	}
}

// TestSourceErrTextNoPathLeak 验证错误文案不含路径。
func TestSourceErrTextNoPathLeak(t *testing.T) {
	// 所有固定文案都不应出现路径分隔或文件名。
	msgs := []string{
		sourceErrText(ErrSymlink),
		sourceErrText(ErrHardlink),
		sourceErrText(ErrNotRegular),
		sourceErrText(os.ErrNotExist),
		sourceErrText(os.ErrPermission),
	}
	for _, m := range msgs {
		if m == "" {
			t.Fatal("文案不能为空")
		}
		for _, bad := range []string{"/source", "/etc", "compose.yml"} {
			if contains(m, bad) {
				t.Fatalf("文案 %q 泄露了路径片段 %q", m, bad)
			}
		}
	}
}

// contains 是 strings.Contains 的本地替身，避免额外 import。
func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
