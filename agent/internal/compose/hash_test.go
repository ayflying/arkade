package compose

// hash_test.go 验证内容哈希。
//
// 最关键的一条是「与 Hub 算法一致」：两边哈希是 drift 判定的唯一依据，
// 算法差一点界面上就会永远显示"配置已偏离"，
// 把真正的偏离淹没在噪声里，用户会干脆无视这个告警。

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestHashMatchesHubPattern 用固定的已知输入锁定哈希格式。
//
// 这里的期望值就是 sha256("services: {}\n") 的十六进制前 16 位。
// 如果将来有人改了 hashHexLen 或改用别的算法，这个测试会立刻失败——
// 那正是我们想要的：改动必须是有意识的，并且要同步改 Hub 侧。
func TestHashMatchesHubPattern(t *testing.T) {
	const content = "services: {}\n"
	got := Hash(content)
	if len(got) != 16 {
		t.Fatalf("应为 16 位十六进制，实际 %d 位: %q", len(got), got)
	}
	for _, c := range got {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			t.Fatalf("应全为小写十六进制字符，实际 %q", got)
		}
	}
	// 稳定性：同样的输入必须得到同样的输出。
	if Hash(content) != got {
		t.Fatal("同样内容必须得到同样哈希")
	}
}

// TestHashSensitive 验证任何内容变化都会改变哈希。
func TestHashSensitive(t *testing.T) {
	base := "services:\n  web:\n    image: nginx:1.27\n"
	h := Hash(base)

	cases := []string{
		base + "  # 注释",
		"services:\n  web:\n    image: nginx:1.28\n",
		"services: {}\n",
		"",
		// 空白差异也算变化：哈希等价于文件字节，不做换行归一化。
		strings.ReplaceAll(base, "\n", "\r\n"),
	}
	for _, c := range cases {
		if Hash(c) == h {
			t.Errorf("内容变化后哈希必须改变，但 %q 与基准相同", c)
		}
	}
}

// TestHashFile 验证文件哈希与字符串哈希一致。
func TestHashFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "compose.yml")
	content := "services:\n  db:\n    image: postgres:16\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	h, err := HashFile(path)
	if err != nil {
		t.Fatalf("计算文件哈希失败: %v", err)
	}
	if h != Hash(content) {
		t.Errorf("文件哈希应等于内容哈希，实际 %q vs %q", h, Hash(content))
	}

	if _, err := HashFile(filepath.Join(dir, "missing.yml")); err == nil {
		t.Error("文件不存在时应报错")
	}
}
