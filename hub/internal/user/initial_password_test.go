package user

import (
	"strings"
	"testing"
)

func TestInitialPasswordSharesNormalPolicy(t *testing.T) {
	hash, err := HashInitialPassword("1")
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyPassword(hash, "1"); err != nil {
		t.Fatal(err)
	}
	// 正常创建账号、改密与部署初始化现在共用同一策略：短密码都允许。
	if _, err := HashPassword("1"); err != nil {
		t.Fatalf("短密码应允许: %v", err)
	}
	for _, value := range []string{"", "   ", strings.Repeat("x", 73)} {
		if _, err := HashInitialPassword(value); err == nil {
			t.Fatal("空白或超长初始密码必须拒绝")
		}
	}
}
