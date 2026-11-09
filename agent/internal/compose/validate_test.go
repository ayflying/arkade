package compose

// validate_test.go 覆盖项目名与服名的白名单。
//
// 这是安全边界测试，每一个"非法但被接受"的用例都意味着可以越界写文件
// 或向命令行注入内容，因此用例要覆盖各种攻击形态而不只是常见的几种。

import (
	"strings"
	"testing"
)

// TestValidateNameAccepts 验证合法项目名。
func TestValidateNameAccepts(t *testing.T) {
	ok := []string{"web", "a1", "my-stack", "a_b", "a-b_c", "0", "abc123-_x", "z"}
	for _, s := range ok {
		if err := ValidateName(s); err != nil {
			t.Errorf("%q 应合法，实际报错: %v", s, err)
		}
	}
}

// TestValidateNameRejects 验证非法项目名全部被拒。
func TestValidateNameRejects(t *testing.T) {
	bad := []struct {
		name   string
		reason string
	}{
		{"", "空"},
		{"../etc", "路径穿越"},
		{"..", "父目录"},
		{".", "当前目录"},
		{"a/b", "斜杠"},
		{"a\\b", "反斜杠"},
		{"a..b", "内含双点"},
		{"-a", "连字符开头（参数注入）"},
		{"_a", "下划线开头"},
		{"WEB", "大写"},
		{"Web", "混合大小写"},
		{"my stack", "空格"},
		{"a;rm -rf /", "命令分隔符"},
		{"$(whoami)", "命令替换"},
		{"a`id`b", "反引号"},
		{"a|b", "管道"},
		{"a&b", "后台符"},
		{"a\nb", "换行"},
		{"a\tb", "制表符"},
		{"名字", "非 ASCII"},
		{"a.b", "点号"},
		{strings.Repeat("a", 65), "超长"},
	}
	for _, c := range bad {
		if err := ValidateName(c.name); err == nil {
			t.Errorf("%q 应被拒绝（%s），但校验通过了", c.name, c.reason)
		}
	}
}

// TestValidateNameLengthBoundary 验证长度边界（64 通过，65 拒绝）。
func TestValidateNameLengthBoundary(t *testing.T) {
	ok := strings.Repeat("a", 64)
	if err := ValidateName(ok); err != nil {
		t.Errorf("64 字符应合法: %v", err)
	}
	bad := strings.Repeat("a", 65)
	if err := ValidateName(bad); err == nil {
		t.Error("65 字符应被拒绝")
	}
}

// TestValidateService 验证服务名校验。
func TestValidateService(t *testing.T) {
	ok := []string{"web", "web1", "my-service", "my_service", "a.b", "_x"}
	for _, s := range ok {
		if err := ValidateService(s); err != nil {
			t.Errorf("%q 应合法: %v", s, err)
		}
	}
	bad := []string{"", "../x", "a/b", "a b", "a;b", "$(id)", "-x", "名字", strings.Repeat("a", 64)}
	for _, s := range bad {
		if err := ValidateService(s); err == nil {
			t.Errorf("%q 应被拒绝", s)
		}
	}
}

// TestValidateServices 验证批量校验在第一个错误处返回。
func TestValidateServices(t *testing.T) {
	if err := ValidateServices([]string{"web", "db"}); err != nil {
		t.Errorf("全部合法时不应报错: %v", err)
	}
	if err := ValidateServices([]string{"web", "../bad", "db"}); err == nil {
		t.Error("含非法项时应报错")
	}
}

// TestCleanName 验证去空白后校验。
func TestCleanName(t *testing.T) {
	got, err := cleanName("  web  ")
	if err != nil {
		t.Fatalf("带空白的合法名应通过: %v", err)
	}
	if got != "web" {
		t.Errorf("应去掉空白，实际 %q", got)
	}
	if _, err := cleanName("   "); err == nil {
		t.Error("全空白应被拒绝")
	}
	// 大小写不做转换：Compose 要求小写，转了小写会掩盖用户的输入错误。
	if _, err := cleanName(" Web "); err == nil {
		t.Error("大写应被拒绝而不是被转成小写")
	}
}
