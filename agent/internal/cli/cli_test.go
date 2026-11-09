package cli

import (
	"bytes"
	"flag"
	"strings"
	"testing"
)

// TestMainNoArgs 无参数时应打印用法并以用法错误码退出（而不是静默成功）。
func TestMainNoArgs(t *testing.T) {
	var out, errOut bytes.Buffer
	code := Main(nil, &out, &errOut)
	if code != exitUsage {
		t.Fatalf("退出码 = %d，期望 %d", code, exitUsage)
	}
	if !strings.Contains(out.String(), "arkade-agent") {
		t.Fatalf("未打印用法: %q", out.String())
	}
}

// TestMainUnknownCommand 未知子命令必须给出明确提示。
func TestMainUnknownCommand(t *testing.T) {
	var out, errOut bytes.Buffer
	code := Main([]string{"frobnicate"}, &out, &errOut)
	if code != exitUsage {
		t.Fatalf("退出码 = %d，期望 %d", code, exitUsage)
	}
	if !strings.Contains(errOut.String(), "未知子命令") {
		t.Fatalf("错误提示缺失: %q", errOut.String())
	}
}

// TestMainVersion 版本子命令不依赖任何配置或运行时，必须能离线执行。
func TestMainVersion(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := Main([]string{"version"}, &out, &errOut); code != exitOK {
		t.Fatalf("退出码 = %d", code)
	}
	if !strings.Contains(out.String(), Version) {
		t.Fatalf("版本输出缺少版本号: %q", out.String())
	}
}

// TestRunRequiresConfigFile run 子命令必须显式指定 -c：
// 服务模式没有配置文件就等于没有 Hub 地址与令牌，猜默认值只会掩盖配置错误。
func TestRunRequiresConfigFile(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := Main([]string{"run"}, &out, &errOut); code != exitUsage {
		t.Fatalf("退出码 = %d，期望 %d", code, exitUsage)
	}
	if !strings.Contains(errOut.String(), "-c") {
		t.Fatalf("应提示需要 -c: %q", errOut.String())
	}
}

// TestRunInvalidConfig run 子命令遇到非法配置应给出人话错误。
func TestRunInvalidConfig(t *testing.T) {
	dir := t.TempDir()
	p := dir + "/agent.toml"
	// 用 http:// 而不是 ws://，同时也缺 token，校验必须失败。
	if err := writeFile(p, "hub_url = \"http://hub:8080\"\n"); err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	if code := Main([]string{"run", "-c", p}, &out, &errOut); code != exitFail {
		t.Fatalf("退出码 = %d，期望 %d；stderr=%q", code, exitFail, errOut.String())
	}
	if !strings.Contains(errOut.String(), "ws://") {
		t.Fatalf("错误信息应给出修复建议: %q", errOut.String())
	}
}

// TestLocalCommandWithoutRuntime 本机没有运行时的时候，本地命令要给出可读提示而不是裸错误。
func TestLocalCommandWithoutRuntime(t *testing.T) {
	dir := t.TempDir()
	p := dir + "/agent.toml"
	// 指向一个确定不存在的套接字，强制探测失败 → 显式端点失败应报错退出。
	if err := writeFile(p, "socket = \"unix:///nonexistent/arkade-test.sock\"\n"); err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	code := Main([]string{"ps", "-c", p}, &out, &errOut)
	if code != exitFail {
		t.Fatalf("退出码 = %d，期望 %d", code, exitFail)
	}
	if !strings.Contains(errOut.String(), "探测") && !strings.Contains(errOut.String(), "运行时") {
		t.Fatalf("错误信息不够人话: %q", errOut.String())
	}
}

// TestParseArgsMixed 验证选项与位置参数的混合解析（`logs web -n 100` 必须生效）。
func TestParseArgsMixed(t *testing.T) {
	fs := flag.NewFlagSet("t", flag.ContinueOnError)
	n := fs.Int("n", 200, "行数")
	flagC := fs.String("c", "", "配置")
	boolF := fs.Bool("force", false, "强制")

	cases := []struct {
		name    string
		args    []string
		wantN   int
		wantC   string
		wantF   bool
		wantPos string
	}{
		{"选项在前", []string{"-n", "50", "web"}, 50, "", false, "web"},
		{"选项在后", []string{"web", "-n", "100"}, 100, "", false, "web"},
		{"等号形式", []string{"web", "-n=30"}, 30, "", false, "web"},
		{"布尔选项", []string{"--force", "db"}, 200, "", true, "db"},
		{"多个选项", []string{"db", "-c", "/tmp/a.toml", "-n", "5"}, 5, "/tmp/a.toml", false, "db"},
	}
	for _, c := range cases {
		// 每轮重置默认值：FlagSet 会保留上一次解析结果。
		*n = 200
		*flagC = ""
		*boolF = false
		pa, err := parseArgs(fs, c.args)
		if err != nil {
			t.Fatalf("%s: parseArgs 失败: %v", c.name, err)
		}
		if *n != c.wantN {
			t.Fatalf("%s: n = %d，期望 %d", c.name, *n, c.wantN)
		}
		if *flagC != c.wantC {
			t.Fatalf("%s: c = %q，期望 %q", c.name, *flagC, c.wantC)
		}
		if *boolF != c.wantF {
			t.Fatalf("%s: force = %v，期望 %v", c.name, *boolF, c.wantF)
		}
		if got := pa.First(); got != c.wantPos {
			t.Fatalf("%s: 位置参数 = %q，期望 %q", c.name, got, c.wantPos)
		}
	}
}

// TestParsedArgsFirst 无位置参数时返回空串。
func TestParsedArgsFirst(t *testing.T) {
	fs := flag.NewFlagSet("t", flag.ContinueOnError)
	_ = fs.Int("n", 200, "行数")
	pa, err := parseArgs(fs, []string{"-n", "10"})
	if err != nil {
		t.Fatal(err)
	}
	if got := pa.First(); got != "" {
		t.Fatalf("位置参数 = %q，期望空", got)
	}
	pa, err = parseArgs(fs, []string{"-n", "10", "web"})
	if err != nil {
		t.Fatal(err)
	}
	if got := pa.First(); got != "web" {
		t.Fatalf("位置参数 = %q，期望 web", got)
	}
}

// TestEmptyDash 空字符串显示为 "-"。
func TestEmptyDash(t *testing.T) {
	if emptyDash("") != "-" {
		t.Fatal("空字符串应显示为 -")
	}
	if emptyDash("x") != "x" {
		t.Fatal("非空字符串不应被改写")
	}
}

// TestStartStopRequiresRef start / stop 缺容器引用时必须以用法错误退出。
func TestStartStopRequiresRef(t *testing.T) {
	for _, cmd := range []string{"start", "stop", "logs"} {
		var out, errOut bytes.Buffer
		code := Main([]string{cmd}, &out, &errOut)
		if code != exitUsage {
			t.Fatalf("%s: 退出码 = %d，期望 %d", cmd, code, exitUsage)
		}
	}
}
