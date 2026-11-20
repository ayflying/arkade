package runtime

import (
	"archive/tar"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"path"
	"reflect"
	"strings"
	"testing"

	"arkade/protocol/envelope"
)

func managedUpdateFixture() DetachedComposeUp {
	return DetachedComposeUp{
		ImageID: "sha256:img", Project: "arkade", Service: "hub", Name: "arkade-selfupdate-test", RequestID: "test",
		WorkingDir: "/data/arkade", ConfigFile: "/data/arkade/compose.yml",
		Managed: &envelope.SelfUpdateManagedConfig{Project: "arkade", Content: "services:\n  hub:\n    image: sample:latest\n    volumes: ['./state:/state']\n", EnvContent: "PASSWORD=fake-unit-test-secret\n"},
	}
}

func TestManagedSelfUpdateUsesPrivateArchiveBeforeStart(t *testing.T) {
	input := managedUpdateFixture()
	var calls []string
	e := newTestEngine(t, func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.Method+" "+r.URL.Path)
		switch r.URL.Path {
		case "/containers/json":
			_, _ = w.Write([]byte(`[]`))
		case "/containers/create":
			raw, _ := io.ReadAll(r.Body)
			if strings.Contains(string(raw), "fake-unit-test-secret") || strings.Contains(string(raw), "sample:latest") {
				t.Error("快照不能进入 Docker inspect 的创建参数")
			}
			var cfg helperContainerConfig
			if err := json.Unmarshal(raw, &cfg); err != nil {
				t.Error(err)
			}
			want := []string{"compose", "--project-name", "arkade", "--project-directory", "/data/arkade", "--file", selfUpdatePrivateCompose, "--env-file", selfUpdatePrivateEnv, "up", "-d", "--no-deps", "--no-build", "--pull", "always", "hub"}
			if !reflect.DeepEqual(cfg.Cmd, want) || cfg.WorkingDir != input.WorkingDir || cfg.Entrypoint[2] != selfUpdateManagedScript {
				t.Errorf("未保留原工作目录、项目身份和固定命令边界：%v", cfg.Cmd)
			}
			if cfg.HostConfig.Privileged || len(cfg.HostConfig.CapAdd) != 0 || !reflect.DeepEqual(cfg.HostConfig.CapDrop, []string{"ALL"}) || len(cfg.HostConfig.Mounts) != 2 || !cfg.HostConfig.Mounts[1].ReadOnly || cfg.HostConfig.Tmpfs["/tmp"] == "" {
				t.Error("更新助手权限或暂存不符合要求")
			}
			_, _ = w.Write([]byte(`{"Id":"helper"}`))
		case "/containers/helper/archive":
			if r.URL.Query().Get("path") != "/" || r.Header.Get("Content-Type") != "application/x-tar" {
				t.Error("快照必须通过助手归档接口注入")
			}
			tr := tar.NewReader(r.Body)
			dir := strings.TrimPrefix(selfUpdateArchiveDir, "/")
			h, err := tr.Next()
			if err != nil || h.Name != dir || h.Mode != 0o700 || h.Typeflag != tar.TypeDir {
				t.Error("归档父目录应为私有 0700")
				break
			}
			for _, f := range []struct{ name, value string }{{selfUpdateComposeName, input.Managed.Content}, {selfUpdateEnvName, input.Managed.EnvContent}} {
				h, err := tr.Next()
				if err != nil || h.Name != path.Join(dir, f.name) || h.Mode != 0o600 || h.Typeflag != tar.TypeReg {
					t.Error("归档快照应为私有 0600")
					break
				}
				body, _ := io.ReadAll(tr)
				if string(body) != f.value {
					t.Error("归档快照内容偏离输入")
				}
			}
		default:
			w.WriteHeader(http.StatusNoContent)
		}
	})
	e.endpoint = "unix:///var/run/docker.sock"
	got, err := e.RunDetachedComposeUp(context.Background(), input)
	want := []string{"GET /containers/json", "POST /containers/create", "PUT /containers/helper/archive", "POST /containers/helper/start"}
	if err != nil || got != "helper" || !reflect.DeepEqual(calls, want) {
		t.Fatalf("归档必须在容器启动之前完成：id=%q err=%v calls=%v", got, err, calls)
	}
}

func TestManagedSelfUpdateDiscardsFailedArchiveAndSanitizesError(t *testing.T) {
	var removed bool
	e := newTestEngine(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.Path {
		case "GET /containers/json":
			_, _ = w.Write([]byte(`[]`))
		case "POST /containers/create":
			_, _ = w.Write([]byte(`{"Id":"helper"}`))
		case "PUT /containers/helper/archive":
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"message":"fake-unit-test-secret"}`))
		case "DELETE /containers/helper":
			removed = r.URL.Query().Get("force") == "1" && r.URL.Query().Get("v") == "1"
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("归档失败后不能启动助手：%s %s", r.Method, r.URL.Path)
		}
	})
	e.endpoint = "unix:///var/run/docker.sock"
	if _, err := e.RunDetachedComposeUp(context.Background(), managedUpdateFixture()); err == nil || strings.Contains(err.Error(), "fake-unit-test-secret") || !removed {
		t.Fatalf("归档失败必须回收容器且不能透出原文：removed=%v err=%v", removed, err)
	}
}

func TestSelfUpdateDoesNotDeleteActiveHelper(t *testing.T) {
	e := newTestEngine(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/containers/json" {
			t.Errorf("有运行中的助手时必须拒绝，不能清理或创建：%s %s", r.Method, r.URL.Path)
		}
		_, _ = w.Write([]byte(`[{"Id":"active","State":"running","Names":["/arkade-selfupdate-previous"]}]`))
	})
	e.endpoint = "unix:///var/run/docker.sock"
	var blocked *SelfUpdateBlocker
	if _, err := e.RunDetachedComposeUp(context.Background(), managedUpdateFixture()); !errors.As(err, &blocked) {
		t.Fatalf("有运行中的助手应阻断并发任务：%v", err)
	}
}

func TestManagedSelfUpdateRejectsUnsafeSnapshot(t *testing.T) {
	cases := map[string]func(*DetachedComposeUp){
		"项目不符":            func(in *DetachedComposeUp) { in.Managed.Project = "arkade-other" },
		"试图改变 Compose 来源": func(in *DetachedComposeUp) { in.Managed.EnvContent = "export\tCOMPOSE_FILE=other.yaml\n" },
		"非字面副本数":          func(in *DetachedComposeUp) { in.Managed.Content += "    scale: '${REPLICAS}'\n" },
		"扩展外部文件":          func(in *DetachedComposeUp) { in.Managed.Content += "    extends: {file: another.yml, service: hub}\n" },
		"包含外部文件":          func(in *DetachedComposeUp) { in.Managed.Content += "include: [another.yml]\n" },
		"原服务不存在":          func(in *DetachedComposeUp) { in.Service = "agent" },
		"哨兵无纳管快照":         func(in *DetachedComposeUp) { in.Managed = nil; in.ConfigFile = selfUpdatePrivateCompose },
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			input := managedUpdateFixture()
			change(&input)
			var blocked *SelfUpdateBlocker
			if err := validateManagedSelfUpdate(input); !errors.As(err, &blocked) {
				t.Fatalf("应以固定诊断拒绝：%v", err)
			}
		})
	}
}

func TestSelfContainerInfoAcceptsManagedSentinelForNextUpdate(t *testing.T) {
	e := newTestEngine(t, func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"Id": "current", "Image": "sha256:img", "Config": map[string]any{"Labels": map[string]string{
			labelSelfComposeProject: "arkade", labelSelfComposeService: "hub", labelComposeWorkingDir: "/data/arkade", labelComposeConfigFiles: selfUpdatePrivateCompose,
		}}})
	})
	info, err := e.SelfContainerInfo(context.Background())
	if err != nil || info.WorkingDir != "/data/arkade" || info.Project != "arkade" || info.ConfigFile != selfUpdatePrivateCompose {
		t.Fatalf("下次探测必须仍能定位相同项目：%+v %v", info, err)
	}
}
