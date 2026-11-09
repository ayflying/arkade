package user

// identity_test.go 覆盖用户名/组名规整与校验。
//
// 这些规则同时被「创建用户」「登录」「初始化管理员」三个入口使用，
// 任何一处改动都会波及登录，因此必须逐条固定住。

import "testing"

// TestNormalizeUsername 验证用户名规整：去首尾空白 + 转小写。
func TestNormalizeUsername(t *testing.T) {
	cases := []struct{ in, want string }{
		{"  Admin  ", "admin"},
		{"ADMIN", "admin"},
		{"Alice.Wang", "alice.wang"},
		{"a-_b", "a-_b"},
		{"", ""},
	}
	for _, c := range cases {
		if got := NormalizeUsername(c.in); got != c.want {
			t.Errorf("NormalizeUsername(%q) = %q，期望 %q", c.in, got, c.want)
		}
	}
}

// TestValidateUsername 验证用户名字符集与长度的边界。
func TestValidateUsername(t *testing.T) {
	valid := []string{"abc", "user_1", "a-b", "a.b", "张三", "user123"}
	for _, s := range valid {
		if err := ValidateUsername(s); err != nil {
			t.Errorf("%q 应合法，实际报错 %v", s, err)
		}
	}

	invalid := []string{
		"",           // 空
		"ab",         // 太短
		"a b",        // 空格
		"a@b",        // 邮箱符号
		"a/b",        // 斜杠（会与 URL 路径冲突）
		"agent:x",    // 冒号（与 agent:xxx 这类目标格式混淆）
		"12345",      // 全数字
		"用户名带中文逗号，x", // 中文标点不在允许集合
	}
	for _, s := range invalid {
		if err := ValidateUsername(s); err == nil {
			t.Errorf("%q 应报错，实际通过", s)
		}
	}

	// 纯连字符是允许的：字符集校验只管非法字符，不额外限制组合方式。
	// 这类边界的取舍写进测试，免得以后有人「顺手」加限制而破坏已有账号。
	if err := ValidateUsername("----"); err != nil {
		t.Errorf("纯连字符用户名按现行规则应合法，实际 %v", err)
	}
}

// TestNormalizeGroupName 验证组名只去空白、不转小写（与用户名刻意不同）。
func TestNormalizeGroupName(t *testing.T) {
	if got := NormalizeGroupName("  DEV  "); got != "DEV" {
		t.Errorf("组名应保留大小写，实际 %q", got)
	}
	if got := NormalizeGroupName("\t运维组\n"); got != "运维组" {
		t.Errorf("组名应去除首尾空白，实际 %q", got)
	}
}

// TestValidateGroupName 验证组名边界。
func TestValidateGroupName(t *testing.T) {
	if err := ValidateGroupName(""); err == nil {
		t.Error("空组名应报错")
	}
	if err := ValidateGroupName("运维组"); err != nil {
		t.Errorf("中文组名应合法，实际 %v", err)
	}
	// 超长。
	long := make([]rune, maxGroupNameLen+1)
	for i := range long {
		long[i] = 'a'
	}
	if err := ValidateGroupName(string(long)); err == nil {
		t.Error("超长组名应报错")
	}
}

// TestValidateDisplayName 验证显示名规则。
func TestValidateDisplayName(t *testing.T) {
	if err := ValidateDisplayName("   "); err == nil {
		t.Error("全空白显示名应报错")
	}
	if err := ValidateDisplayName("张三 (运维)"); err != nil {
		t.Errorf("含特殊字符的显示名应合法，实际 %v", err)
	}
}

// TestValidRole 验证角色校验：输入侧必须严格，不接受未知值。
func TestValidRole(t *testing.T) {
	if r, err := ValidRole("admin"); err != nil || r != RoleAdmin {
		t.Errorf("admin 应合法，实际 %v/%v", r, err)
	}
	if r, err := ValidRole("user"); err != nil || r != RoleUser {
		t.Errorf("user 应合法，实际 %v/%v", r, err)
	}
	// 大小写错误必须报错，不能被静默降级成普通用户。
	for _, bad := range []string{"Admin", "ADMIN", "", "root", "guest"} {
		if _, err := ValidRole(bad); err == nil {
			t.Errorf("%q 应报错（不能静默降级）", bad)
		}
	}
}

// TestValidatePassword 验证密码策略前置校验与 HashPassword 一致。
func TestValidatePassword(t *testing.T) {
	if err := ValidatePassword("short"); err == nil {
		t.Error("短密码应报错")
	}
	if err := ValidatePassword("Password123"); err != nil {
		t.Errorf("合法密码不应报错，实际 %v", err)
	}
	// 超长（bcrypt 上限 72 字节）。
	long := make([]byte, maxPasswordBytes+1)
	for i := range long {
		long[i] = 'a'
	}
	if err := ValidatePassword(string(long)); err == nil {
		t.Error("超过 72 字节的密码应报错")
	}
}
