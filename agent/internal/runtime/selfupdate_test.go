package runtime

// selfupdate_test.go：自更新的安全边界测试。
//
// ============================ 这些用例在防什么 ============================
//
// 自更新会创建一个**挂着宿主 socket、以 root 运行**的容器去执行 compose。
// 一旦参数校验被放宽，这个能力就从"更新自己"变成"在宿主机上任意起容器"。
// 因此下面每一条都不是形式主义：
//
//   - 命令必须是**写死的固定 argv**，不能出现 sh -c / 变量拼接；
//   - 服务名、项目名必须被白名单挡住（含 ; $ 空格 一律拒绝）；
//   - 挂载只能是"socket + 编排目录"两处，不能多；
//   - 非 compose 部署必须原地拒绝，而不是猜一个项目名去 up。

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
)

func TestSelfUpdateHelperName(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"abc123", "arkade-selfupdate-abc123"},
		{"a/b:c d", "arkade-selfupdate-a-b-c-d"},
		{"--", "arkade-selfupdate-manual"},
		{"", "arkade-selfupdate-manual"},
		{strings.Repeat("z", 80), "arkade-selfupdate-" + strings.Repeat("z", 40)},
	}
	for _, c := range cases {
		got := SelfUpdateHelperName(c.in)
		if got != c.want {
			t.Errorf("SelfUpdateHelperName(%q) = %q，期望 %q", c.in, got, c.want)
		}
		if !strings.HasPrefix(got, selfUpdateHelperPrefix) {
			t.Errorf("名字 %q 缺少固定前缀，清理逻辑会认不出来", got)
		}
	}
}

func TestIsSafeHostPath(t *testing.T) {
	ok := []string{"/data/arkade", "/vol1/1000/docker/arkade", "/opt/app"}
	bad := []string{
		"", "relative/path", "/", "/proc/self", "/sys/fs", "/dev", "/etc/arkade",
		"/run/docker", "/var/run/docker.sock", "/data/arkade/", "/data/../etc",
	}
	for _, p := range ok {
		if !isSafeHostPath(p) {
			t.Errorf("应允许 %q", p)
		}
	}
	for _, p := range bad {
		if isSafeHostPath(p) {
			t.Errorf("必须拒绝 %q", p)
		}
	}
}

func TestFirstContainerID(t *testing.T) {
	id := strings.Repeat("a1", 32)
	cases := []struct {
		name    string
		content string
		want    string
	}{
		{"cgroup v1", "12:devices:/docker/" + id + "\n", id},
		{"cgroup v2", "0::/system.slice/docker-" + id + ".scope\n", id},
		{"无容器 ID", "0::/user.slice/session-1.scope\n", ""},
		{"hex 太短", "0::/docker/abc123def\n", ""},
	}
	for _, c := range cases {
		if got := firstContainerID(c.content); got != c.want {
			t.Errorf("%s: firstContainerID = %q，期望 %q", c.name, got, c.want)
		}
	}
}

// TestSelfContainerInfoBlocksNonCompose 验证"不是 compose 部署"时
// 返回的是**结论**（SelfUpdateBlocker），而不是含糊的故障。
func TestSelfContainerInfoBlocksNonCompose(t *testing.T) {
	e := newTestEngine(t, func(w http.ResponseWriter, r *http.Request) {
		// 任何 /containers/{id}/json 都返回一个没有编排标签的容器。
		_, _ = w.Write([]byte(`{"Id":"c1","Name":"/arkade-hub","Image":"sha256:img","Config":{"Image":"ayflying/arkade-hub:latest","Labels":{}}}`))
	})
	var blocked *SelfUpdateBlocker
	if _, err := e.SelfContainerInfo(context.Background()); !errors.As(err, &blocked) {
		t.Fatalf("缺少编排标签时必须拒绝自更新，实际 err=%v", err)
	}
}

// TestSelfContainerInfoRejectsMultiConfig 验证多配置文件被拒。
func TestSelfContainerInfoRejectsMultiConfig(t *testing.T) {
	e := newTestEngine(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"Id":"c1","Name":"/h","Image":"sha256:img","Config":{"Image":"x:1","Labels":{
			"com.docker.compose.project":"arkade",
			"com.docker.compose.service":"hub",
			"com.docker.compose.project.working_dir":"/data/arkade",
			"com.docker.compose.project.config_files":"/data/arkade/a.yml,/data/arkade/b.yml"}}}`))
	})
	var blocked *SelfUpdateBlocker
	if _, err := e.SelfContainerInfo(context.Background()); !errors.As(err, &blocked) {
		t.Fatalf("多配置文件必须拒绝，实际 err=%v", err)
	}
}

// TestSelfContainerInfoReadsLabels 验证正常部署能读全四项。
func TestSelfContainerInfoReadsLabels(t *testing.T) {
	e := newTestEngine(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"Id":"cid","Name":"/arkade-hub","Image":"sha256:img","Config":{"Image":"ayflying/arkade-hub:latest","Labels":{
			"com.docker.compose.project":"arkade",
			"com.docker.compose.service":"hub",
			"com.docker.compose.project.working_dir":"/data/arkade",
			"com.docker.compose.project.config_files":"/data/arkade/compose.yml"}}}`))
	})
	info, err := e.SelfContainerInfo(context.Background())
	if err != nil {
		t.Fatalf("应能读取自己：%v", err)
	}
	if info.Project != "arkade" || info.Service != "hub" ||
		info.WorkingDir != "/data/arkade" || info.ConfigFile != "/data/arkade/compose.yml" {
		t.Fatalf("标签读取不完整: %+v", info)
	}
	if info.ImageRef != "ayflying/arkade-hub:latest" || info.ImageID != "sha256:img" {
		t.Fatalf("镜像信息不正确: %+v", info)
	}
}

// TestRunDetachedComposeUpUsesFixedArgv 是核心断言：
// 命令必须是一条写死的 argv，且只挂两处。
func TestRunDetachedComposeUpUsesFixedArgv(t *testing.T) {
	var created struct {
		Image      string            `json:"Image"`
		Entrypoint []string          `json:"Entrypoint"`
		Cmd        []string          `json:"Cmd"`
		WorkingDir string            `json:"WorkingDir"`
		Labels     map[string]string `json:"Labels"`
		HostConfig struct {
			Privileged bool     `json:"Privileged"`
			CapDrop    []string `json:"CapDrop"`
			Mounts     []struct {
				Type   string `json:"Type"`
				Source string `json:"Source"`
				Target string `json:"Target"`
			} `json:"Mounts"`
		} `json:"HostConfig"`
	}
	var started bool
	e := newTestEngine(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/containers/create"):
			_ = json.NewDecoder(r.Body).Decode(&created)
			_, _ = w.Write([]byte(`{"Id":"newid"}`))
		case strings.HasSuffix(r.URL.Path, "/start"):
			started = true
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})
	// 用 unix 端点覆盖，因为自更新只支持 unix；客户端仍指向测试服务器。
	e.endpoint = "unix:///var/run/docker.sock"

	id, err := e.RunDetachedComposeUp(context.Background(), DetachedComposeUp{
		ImageID:    "sha256:img",
		Project:    "arkade",
		WorkingDir: "/data/arkade",
		ConfigFile: "/data/arkade/compose.yml",
		Service:    "hub",
		Name:       "arkade-selfupdate-abc",
		RequestID:  "abc",
	})
	if err != nil {
		t.Fatalf("创建自更新容器失败: %v", err)
	}
	if id != "newid" || !started {
		t.Fatalf("必须创建并启动容器，id=%q started=%v", id, started)
	}

	want := []string{
		"compose",
		"--project-name", "arkade",
		"--project-directory", "/data/arkade",
		"--file", "/data/arkade/compose.yml",
		"up", "-d", "--no-deps", "--pull", "always", "hub",
	}
	if strings.Join(created.Cmd, " ") != strings.Join(want, " ") {
		t.Fatalf("命令被改动：\n实际 %v\n期望 %v", created.Cmd, want)
	}
	if len(created.Entrypoint) != 1 || created.Entrypoint[0] != "docker" {
		t.Fatalf("入口必须是 docker，实际 %v", created.Entrypoint)
	}
	for _, banned := range []string{"sh", "-c", "bash", "/bin/sh"} {
		for _, a := range append(append([]string{}, created.Entrypoint...), created.Cmd...) {
			if a == banned {
				t.Fatalf("不允许出现 shell：%v", created.Cmd)
			}
		}
	}
	if created.Image != "sha256:img" || created.WorkingDir != "/data/arkade" {
		t.Fatalf("镜像或工作目录不正确: %+v", created)
	}
	if created.Labels[selfUpdateLabel] != "abc" {
		t.Fatalf("缺少自更新标签，重启后无法对账: %v", created.Labels)
	}
	if created.HostConfig.Privileged {
		t.Fatal("helper 不允许以 privileged 运行")
	}
	if len(created.HostConfig.CapDrop) != 1 || created.HostConfig.CapDrop[0] != "ALL" {
		t.Fatalf("必须 CapDrop ALL，实际 %v", created.HostConfig.CapDrop)
	}
	if len(created.HostConfig.Mounts) != 2 {
		t.Fatalf("只允许挂 socket 与编排目录两处，实际 %+v", created.HostConfig.Mounts)
	}
	if created.HostConfig.Mounts[0].Source != "/var/run/docker.sock" ||
		created.HostConfig.Mounts[1].Source != "/data/arkade" ||
		created.HostConfig.Mounts[1].Target != "/data/arkade" {
		t.Fatalf("挂载点不正确: %+v", created.HostConfig.Mounts)
	}
}

// TestRunDetachedComposeUpRejectsBadService 验证注入尝试被白名单挡住。
func TestRunDetachedComposeUpRejectsBadService(t *testing.T) {
	e := newTestEngine(t, func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("校验失败时不允许发出任何请求，收到 %s %s", r.Method, r.URL.Path)
	})
	e.endpoint = "unix:///var/run/docker.sock"

	bad := []string{"hub; rm -rf /", "hub && whoami", "$(id)", "hub`id`", "hub 2", "../hub", ""}
	for _, svc := range bad {
		_, err := e.RunDetachedComposeUp(context.Background(), DetachedComposeUp{
			ImageID:    "sha256:img",
			Project:    "arkade",
			WorkingDir: "/data/arkade",
			ConfigFile: "/data/arkade/compose.yml",
			Service:    svc,
			Name:       "arkade-selfupdate-abc",
			RequestID:  "abc",
		})
		if err == nil {
			t.Errorf("服务名 %q 必须被拒绝", svc)
		}
	}
}

// TestRunDetachedComposeUpRejectsTCPEndpoint 验证非 unix 端点被拒。
func TestRunDetachedComposeUpRejectsTCPEndpoint(t *testing.T) {
	e := newTestEngine(t, func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("端点不支持时不允许发出任何请求")
	})
	if _, err := e.RunDetachedComposeUp(context.Background(), DetachedComposeUp{
		ImageID:    "sha256:img",
		Project:    "arkade",
		WorkingDir: "/data/arkade",
		ConfigFile: "/data/arkade/compose.yml",
		Service:    "hub",
		Name:       "arkade-selfupdate-abc",
		RequestID:  "abc",
	}); err == nil {
		t.Fatal("tcp 端点必须拒绝自更新")
	}
}
