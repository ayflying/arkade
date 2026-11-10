package runtime

// compose_source_test.go：从容器标签反查工作目录、路径安全校验。
//
// 这些用例全部围绕"**不把可读范围扩大**"这一个目标：
// 标签由宿主机管理员可写，因此每条路径都必须被主动校验，
// 而不是因为"它来自引擎"就信任。

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
)

// composeLabelsServer 返回一个只提供容器列表的假引擎。
func composeLabelsServer(t *testing.T, containers []map[string]any) *engine {
	t.Helper()
	body := mustJSON(t, containers)
	return newTestEngine(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == pathContainers {
			_, _ = w.Write([]byte(body))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	})
}

// mustJSON 把值序列化成 JSON 字符串（测试辅助）。
func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("序列化失败: %v", err)
	}
	return string(b)
}

// composeContainer 构造一个带 compose 标签的容器响应条目。
func composeContainer(project, wd, cf string) map[string]any {
	return map[string]any{
		"Id":    "id-" + project,
		"Name":  "/" + project + "_svc_1",
		"State": "running",
		"Labels": map[string]string{
			"com.docker.compose.project":              project,
			"com.docker.compose.service":              "svc",
			"com.docker.compose.project.working_dir":  wd,
			"com.docker.compose.project.config_files": cf,
		},
	}
}

// TestResolveComposeSourceAcceptsNormalProject 验证正常项目的解析结果。
func TestResolveComposeSourceAcceptsNormalProject(t *testing.T) {
	e := composeLabelsServer(t, []map[string]any{
		composeContainer("demo", "/srv/apps/demo", "/srv/apps/demo/compose.yml"),
	})
	wd, rel, err := e.resolveComposeSource(context.Background(), "demo")
	if err != nil {
		t.Fatalf("不应失败: %v", err)
	}
	if wd != "/srv/apps/demo" || rel != "compose.yml" {
		t.Fatalf("解析结果不符: wd=%q rel=%q", wd, rel)
	}
}

// TestResolveComposeSourceRejectsInconsistentLabels 验证容器标签不一致时拒绝。
func TestResolveComposeSourceRejectsInconsistentLabels(t *testing.T) {
	e := composeLabelsServer(t, []map[string]any{
		composeContainer("demo", "/srv/apps/demo", "/srv/apps/demo/compose.yml"),
		composeContainer("demo", "/srv/apps/other", "/srv/apps/other/compose.yml"),
	})
	if _, _, err := e.resolveComposeSource(context.Background(), "demo"); err == nil {
		t.Fatal("标签不一致必须拒绝")
	}
}

// TestResolveComposeSourceRejectsMultiFile 验证多配置文件被拒并给出原因。
func TestResolveComposeSourceRejectsMultiFile(t *testing.T) {
	cf := "/srv/apps/demo/compose.yml,/srv/apps/demo/override.yml"
	e := composeLabelsServer(t, []map[string]any{
		composeContainer("demo", "/srv/apps/demo", cf),
	})
	_, _, err := e.resolveComposeSource(context.Background(), "demo")
	if err == nil {
		t.Fatal("多配置文件必须拒绝")
	}
	if !strings.Contains(err.Error(), "仅支持单配置文件") {
		t.Fatalf("应说明原因，实际: %v", err)
	}
}

// TestResolveComposeSourceRequiresConfigInsideWorkDir 验证配置必须在工作目录内。
func TestResolveComposeSourceRequiresConfigInsideWorkDir(t *testing.T) {
	e := composeLabelsServer(t, []map[string]any{
		composeContainer("demo", "/srv/apps/demo", "/etc/passwd"),
	})
	if _, _, err := e.resolveComposeSource(context.Background(), "demo"); err == nil {
		t.Fatal("工作目录外的配置必须拒绝")
	}
}

// TestValidateWorkingDirRejectsDangerous 验证危险工作目录被拒。
func TestValidateWorkingDirRejectsDangerous(t *testing.T) {
	bad := []string{
		"",             // 空
		"relative/dir", // 相对
		"/",            // 根
		"/etc",         // 系统
		"/proc",        //
		"/sys",
		"/dev",
		"/run",
		"/var/run",
		"/root", // 家目录
		"/home",
		"/opt",
		"/var",
		"/tmp",
		"/srv",            // 顶层公共目录本身
		"/var/lib/docker", // 引擎数据
		"/srv/../etc",     // 含 ..
		"/srv//apps",      // 未规范化
		"/srv/apps\x00x",  // NUL
		`/srv/apps\demo`,  // 反斜杠
	}
	for _, p := range bad {
		if _, err := validateWorkingDir(p); err == nil {
			t.Fatalf("工作目录 %q 必须拒绝", p)
		}
	}
}

// TestValidateWorkingDirAllowsProjectSubdir 验证项目子目录被允许。
func TestValidateWorkingDirAllowsProjectSubdir(t *testing.T) {
	good := []string{
		"/srv/apps/demo",
		"/opt/apps/demo",
		"/home/deploy/app", // 允许其子目录
		"/tmp/work/app",    // 允许其子目录
	}
	for _, p := range good {
		if _, err := validateWorkingDir(p); err != nil {
			t.Fatalf("工作目录 %q 应允许，实际: %v", p, err)
		}
	}
}

// TestRelativeConfigPathRejectsEscape 验证配置文件不得逃出工作目录。
func TestRelativeConfigPathRejectsEscape(t *testing.T) {
	bad := []string{
		"/srv/apps/other/compose.yml",  // 不在工作目录内
		"/srv/apps",                    // 工作目录本身
		"/srv/apps/demo/../demo/x.yml", // 含 ..
		"srv/apps/demo/compose.yml",    // 非绝对
	}
	for _, cf := range bad {
		if _, err := relativeConfigPath("/srv/apps/demo", cf); err == nil {
			t.Fatalf("配置路径 %q 必须拒绝", cf)
		}
	}
	// 嵌套子路径允许。
	rel, err := relativeConfigPath("/srv/apps/demo", "/srv/apps/demo/conf/compose.yml")
	if err != nil {
		t.Fatalf("嵌套路径应允许: %v", err)
	}
	if rel != "conf/compose.yml" {
		t.Fatalf("相对路径不符: %q", rel)
	}
}

// TestResolveComposeSourceIgnoresOneoff 验证一次性容器不参与一致性判定。
func TestResolveComposeSourceIgnoresOneoff(t *testing.T) {
	oneoff := composeContainer("demo", "/tmp/run-xyz", "/tmp/run-xyz/compose.yml")
	oneoff["Labels"].(map[string]string)["com.docker.compose.oneoff"] = "True"
	e := composeLabelsServer(t, []map[string]any{
		composeContainer("demo", "/srv/apps/demo", "/srv/apps/demo/compose.yml"),
		oneoff,
	})
	wd, _, err := e.resolveComposeSource(context.Background(), "demo")
	if err != nil {
		t.Fatalf("oneoff 容器不应干扰: %v", err)
	}
	if wd != "/srv/apps/demo" {
		t.Fatalf("工作目录不符: %q", wd)
	}
}

// TestResolveComposeSourceUnknownProject 验证不存在的项目给出 blocker 而非普通错误。
func TestResolveComposeSourceUnknownProject(t *testing.T) {
	e := composeLabelsServer(t, []map[string]any{})
	_, _, err := e.resolveComposeSource(context.Background(), "nope")
	if err == nil {
		t.Fatal("不存在的项目必须报错")
	}
	var blocked *SourceBlockerError
	if !errors.As(err, &blocked) {
		t.Fatalf("期望 SourceBlockerError，实际 %T: %v", err, err)
	}
	if blocked.Reason == "" {
		t.Fatal("blocker 必须有固定中文原因")
	}
}

// TestSourceBlockersAreTyped 验证"预期不能接管"的每种情形都返回 typed blocker。
//
// 这一组是 Hub 能显示具体原因的前提：任何一条退化成普通 error，
// 前端就只能看到通用的"读取配置失败"。
func TestSourceBlockersAreTyped(t *testing.T) {
	cases := []struct {
		name string
		wd   string
		cf   string
	}{
		{"多配置文件", "/srv/apps/demo", "/srv/apps/demo/a.yml,/srv/apps/demo/b.yml"},
		{"缺工作目录标签", "", "/srv/apps/demo/compose.yml"},
		{"缺配置文件标签", "/srv/apps/demo", ""},
		{"工作目录是系统目录", "/etc", "/etc/compose.yml"},
		{"工作目录是根", "/", "/compose.yml"},
		{"配置在目录外", "/srv/apps/demo", "/opt/secret.yml"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := composeLabelsServer(t, []map[string]any{
				composeContainer("demo", c.wd, c.cf),
			})
			_, _, err := e.resolveComposeSource(context.Background(), "demo")
			if err == nil {
				t.Fatalf("%s 应被拒绝", c.name)
			}
			var blocked *SourceBlockerError
			if !errors.As(err, &blocked) {
				t.Fatalf("%s 应返回 SourceBlockerError，实际 %T: %v", c.name, err, err)
			}
			if strings.TrimSpace(blocked.Reason) == "" {
				t.Fatalf("%s 的 blocker 原因不能为空", c.name)
			}
			// 原因里绝不能出现原始宿主路径。
			for _, leak := range []string{"/srv/apps", "/etc", "/opt", "compose.yml"} {
				if strings.Contains(blocked.Reason, leak) {
					t.Fatalf("%s 的原因泄露了路径片段 %q: %q", c.name, leak, blocked.Reason)
				}
			}
		})
	}
}

// TestSourceBlockerIsNotRuntimeFailure 验证 blocker 不会被当成引擎故障。
func TestSourceBlockerIsNotRuntimeFailure(t *testing.T) {
	var blocked *SourceBlockerError
	err := sourceBlocked("示例原因")
	if !errors.As(err, &blocked) {
		t.Fatal("sourceBlocked 必须返回 SourceBlockerError")
	}
	// 不应被误判成超时/忙等故障类别。
	if errors.Is(err, ErrHelperBusy) || errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("blocker 不应匹配故障类别")
	}
}
