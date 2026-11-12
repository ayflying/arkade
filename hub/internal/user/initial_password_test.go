package user

import (
	"strings"
	"testing"
)

func TestInitialPasswordAllowsShortOnlyForBootstrap(t *testing.T) {
	hash, err := HashInitialPassword("1")
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyPassword(hash, "1"); err != nil {
		t.Fatal(err)
	}
	if _, err := HashPassword("1"); err == nil {
		t.Fatal("正常创建账号和修改密码仍须校验长度")
	}
	for _, value := range []string{"", "   ", strings.Repeat("x", 73)} {
		if _, err := HashInitialPassword(value); err == nil {
			t.Fatal("空白或超长初始密码必须拒绝")
		}
	}
}
