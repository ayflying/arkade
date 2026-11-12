package main

import (
	"flag"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func runWithArgs(t *testing.T, args ...string) error {
	t.Helper()
	oldFlags, oldArgs := flag.CommandLine, os.Args
	flag.CommandLine = flag.NewFlagSet("hub", flag.ContinueOnError)
	flag.CommandLine.SetOutput(io.Discard)
	os.Args = append([]string{"hub"}, args...)
	t.Cleanup(func() {
		flag.CommandLine = oldFlags
		os.Args = oldArgs
	})
	return run()
}

func TestRunWithoutConfigUsesEnv(t *testing.T) {
	t.Setenv("ARKADE_INITIAL_PASS", "short")
	// 在配置校验阶段停止，不创建数据库或监听网络。
	err := runWithArgs(t)
	if err == nil || !strings.Contains(err.Error(), "管理员密码至少 8 位") {
		t.Fatalf("不传 -c 应加载环境配置并进入校验，实际: %v", err)
	}
}

func TestRunExplicitMissingConfigFails(t *testing.T) {
	t.Setenv("ARKADE_INITIAL_PASS", "EnvPass123!")
	err := runWithArgs(t, "-c", filepath.Join(t.TempDir(), "missing.toml"))
	if err == nil || !strings.Contains(err.Error(), "读取配置文件失败") {
		t.Fatalf("显式 -c 文件缺失应报错: %v", err)
	}
}

func TestInitConfigKeepsDefaultPath(t *testing.T) {
	t.Chdir(t.TempDir())
	if err := runWithArgs(t, "-init-config"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat("config.toml"); err != nil {
		t.Fatalf("未指定 -c 时仍应生成 config.toml: %v", err)
	}
}
