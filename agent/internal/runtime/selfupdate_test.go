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
	"os"
	"path/filepath"
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
		{"cgroup v2 + cgroupns=host", "0::/system.slice/docker-" + id + ".scope\n", id},
		{"无容器 ID", "0::/user.slice/session-1.scope\n", ""},
		{"hex 太短", "0::/docker/abc123def\n", ""},
		// cgroup v2 + **私有** cgroup 命名空间：Docker 20.10 起的默认形态。
		// 这里一条 ID 都没有，所以 cgroup 只能当兜底，不能当主线索
		// ——线上那次"无法确认自己运行在哪个容器里"正是踩在这里。
		{"cgroup v2 私有命名空间（没有 ID）", "0::/\n", ""},
	}
	for _, c := range cases {
		if got := firstContainerID(c.content); got != c.want {
			t.Errorf("%s: firstContainerID = %q，期望 %q", c.name, got, c.want)
		}
	}
}

// TestFirstContainerIDFromMounts 验证从 /proc/self/mountinfo 取容器 ID。
//
// 这条线索是 cgroup v2 私有命名空间下的救命稻草：
// Docker 必定把 /etc/hostname 等文件从 <data-root>/containers/<ID>/ 挂进来，
// 挂载来源里就带着完整 ID，与 cgroup 版本无关。
func TestFirstContainerIDFromMounts(t *testing.T) {
	id := strings.Repeat("b2", 32)
	cases := []struct {
		name    string
		content string
		want    string
	}{
		{
			"默认 data-root",
			"1 2 0:3 / /etc/hosts rw - ext4 /dev/sda1 /var/lib/docker/containers/" + id + "/hosts\n",
			id,
		},
		{
			// fnOS / 群晖这类把 docker 根目录放在存储卷上的形态
			"自定义 data-root",
			"1 2 0:3 / /etc/hostname rw - btrfs /dev/mapper/x /vol1/@docker/containers/" + id + "/hostname\n",
			id,
		},
		{"没有容器挂载", "1 2 0:3 / /proc rw - proc proc /proc\n", ""},
		{
			// 裸 64 位十六进制不能认：可能撞上设备名、卷路径里的哈希段，
			// 认错就会去重建**别人**的容器。
			"非 containers 目录下的 64 位十六进制",
			"1 2 0:3 / /data rw - ext4 /dev/" + id + " /data\n",
			"",
		},
	}
	for _, c := range cases {
		if got := firstContainerIDFromMounts(c.content); got != c.want {
			t.Errorf("%s: firstContainerIDFromMounts = %q，期望 %q", c.name, got, c.want)
		}
	}
}

// TestSelfContainerInfoFallsBackToMountinfo 是线上失败的回归测试。
//
// 现场：cgroup v2 私有命名空间 + 编排里写了 hostname。
// 两条老线索同时失效（hostname 解析不到容器、cgroup 里没有 ID），
// 功能报"无法确认自己运行在哪个容器里"。补上 mountinfo 后必须能定位。
func TestSelfContainerInfoFallsBackToMountinfo(t *testing.T) {
	const id = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

	dir := t.TempDir()
	mountinfo := filepath.Join(dir, "mountinfo")
	content := "1000 999 0:50 / /etc/hostname rw,relatime - ext4 /dev/sda1 /vol1/@docker/containers/" + id + "/hostname\n" +
		"1001 999 0:50 / /etc/resolv.conf rw,relatime - ext4 /dev/sda1 /vol1/@docker/containers/" + id + "/resolv.conf\n"
	if err := os.WriteFile(mountinfo, []byte(content), 0o600); err != nil {
		t.Fatalf("准备 mountinfo 失败: %v", err)
	}
	oldPath := selfMountinfoPath
	selfMountinfoPath = mountinfo
	t.Cleanup(func() { selfMountinfoPath = oldPath })

	// hostname 被编排显式覆盖成一个解析不到容器的名字
	t.Setenv("HOSTNAME", "arkade-hub")

	e := newTestEngine(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/containers/arkade-hub/json" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if r.URL.Path != "/containers/"+id+"/json" {
			t.Errorf("意外请求了 %q，只应查询自己", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte(`{"Id":"` + id + `","Name":"/arkade-hub","Image":"sha256:img",` +
			`"Config":{"Image":"ayflying/arkade-hub:latest","Labels":{` +
			`"` + labelSelfComposeProject + `":"arkade",` +
			`"` + labelSelfComposeService + `":"hub",` +
			`"` + labelComposeWorkingDir + `":"/opt/arkade",` +
			`"` + labelComposeConfigFiles + `":"/opt/arkade/compose.yml"}}}`))
	})

	info, err := e.SelfContainerInfo(context.Background())
	if err != nil {
		t.Fatalf("mountinfo 兜底应能定位自己，实际 err=%v", err)
	}
	if info.ContainerID != id {
		t.Errorf("ContainerID = %q，期望 %q", info.ContainerID, id)
	}
	if info.Project != "arkade" || info.Service != "hub" {
		t.Errorf("编排标签读错: project=%q service=%q", info.Project, info.Service)
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

// TestComposeUpArgsOmitsMissingConfigFile 验证配置文件缺失时**不猜名字**。
//
// 猜一个 compose.yml 会白失败一次（compose 报 no such file），而且这个
// 失败对用户毫无解释力；少传 --file 让 compose 在 --project-directory
// 里自动发现（compose.yml / compose.yaml / docker-compose.yml 都认）才稳。
func TestComposeUpArgsOmitsMissingConfigFile(t *testing.T) {
	got := composeUpArgs(DetachedComposeUp{
		Project:    "arkade",
		WorkingDir: "/data/arkade",
		Service:    "hub",
	})
	want := []string{
		"compose",
		"--project-name", "arkade",
		"--project-directory", "/data/arkade",
		"up", "-d", "--no-deps", "--pull", "always", "hub",
	}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("配置文件缺失时不应传 --file：\n实际 %v\n期望 %v", got, want)
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
			CapAdd     []string `json:"CapAdd"`
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
		case r.URL.Path == "/containers/json":
			_, _ = w.Write([]byte(`[]`))
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
	// 非纳管自更新要读宿主上的 .env 与编排文件（常为 0600/0700）。
	// CapDrop ALL 连 DAC_OVERRIDE 一起丢了，容器里的 root 也会读失败，
	// 所以必须加回 DAC_READ_SEARCH；除此以外不许多给任何能力。
	if len(created.HostConfig.CapAdd) != 1 || created.HostConfig.CapAdd[0] != "DAC_READ_SEARCH" {
		t.Fatalf("非纳管自更新必须只加回 DAC_READ_SEARCH，实际 %v", created.HostConfig.CapAdd)
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
