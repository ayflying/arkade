package runtime

// container_recreate_test.go 验证"重建"的顺序与回滚。
//
// 这些用例的价值不在覆盖率，而在**锁住顺序**：重建是一串破坏性动作，
// 顺序错了就会出现"容器没了也没建回来"。因此断言的重点是
// "哪个请求在哪个请求之前"、"失败后有没有把旧容器恢复原样"。

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// recreateRecorder 记录假引擎收到的请求，供顺序断言使用。
type recreateRecorder struct {
	mu    sync.Mutex
	calls []string
	// createBody 保存 /containers/create 的请求体，用于断言"复制了哪些配置"。
	createBody []byte
}

func (r *recreateRecorder) add(call string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, call)
}

func (r *recreateRecorder) snapshot() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.calls...)
}

// indexOf 返回第一次出现的位置，找不到返回 -1。
func indexOf(calls []string, want string) int {
	for i, c := range calls {
		if c == want {
			return i
		}
	}
	return -1
}

// recreateInspectFixture 是一份"有代表性的"inspect 结果：
// 带环境变量、端口映射、绑定挂载、自定义网络别名、重启策略。
const recreateInspectFixture = `{
  "Id": "oldid",
  "Name": "/web",
  "State": {"Running": true, "Status": "running"},
  "Config": {
    "Hostname": "abc123",
    "Image": "nginx:1.27",
    "Env": ["A=1", "B=2"],
    "Cmd": ["nginx", "-g", "daemon off;"],
    "Entrypoint": ["/docker-entrypoint.sh"],
    "WorkingDir": "/work",
    "User": "root",
    "Labels": {"com.docker.compose.project": "demo"},
    "ExposedPorts": {"80/tcp": {}},
    "HostConfig": {"Binds": null}
  },
  "HostConfig": {
    "Binds": ["/data:/data:rw"],
    "PortBindings": {"80/tcp": [{"HostIp": "", "HostPort": "8080"}]},
    "RestartPolicy": {"Name": "unless-stopped", "MaximumRetryCount": 0},
    "NetworkMode": "demo-net",
    "Memory": 536870912,
    "NanoCpus": 1000000000
  },
  "NetworkSettings": {
    "Networks": {"demo-net": {"Aliases": ["web", "frontend"]}}
  }
}`

// newRecreateEngine 构造一个假引擎。
//
// failOn 用来注入失败：命中该路径片段时返回 500，其它端点正常返回。
// 返回 recorder 以便断言顺序与创建请求体。
func newRecreateEngine(t *testing.T, inspectJSON string, failOn string) (Runtime, *recreateRecorder) {
	t.Helper()
	rec := &recreateRecorder{}
	mux := http.NewServeMux()
	// probe 需要 /version 才能判定引擎类型与 API 版本。
	mux.HandleFunc(pathVersion, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"Version":"27.3.1","ApiVersion":"1.47","Components":[{"Name":"Engine","Version":"27.3.1"}]}`))
	})
	mux.HandleFunc("/containers/", func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		switch {
		case strings.HasSuffix(path, "/json") && r.Method == http.MethodGet:
			rec.add("inspect")
			fmt.Fprint(w, inspectJSON)

		case strings.HasSuffix(path, "/stop"):
			rec.add("stop")
			w.WriteHeader(http.StatusNoContent)

		case strings.HasSuffix(path, "/rename"):
			// 记下目标名字，回滚测试要确认改回的正是原名。
			rec.add("rename:" + r.URL.Query().Get("name"))
			w.WriteHeader(http.StatusNoContent)

		case strings.HasSuffix(path, "/start"):
			rec.add("start:" + strings.TrimSuffix(strings.TrimPrefix(path, "/containers/"), "/start"))
			if failOn == "start" && strings.Contains(path, "newid") {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			w.WriteHeader(http.StatusNoContent)

		case r.Method == http.MethodDelete:
			rec.add("remove")
			w.WriteHeader(http.StatusNoContent)

		default:
			rec.add("other:" + path)
			w.WriteHeader(http.StatusNoContent)
		}
	})
	mux.HandleFunc(pathContainersCreate, func(w http.ResponseWriter, r *http.Request) {
		rec.add("create")
		body, _ := io.ReadAll(r.Body)
		rec.mu.Lock()
		rec.createBody = body
		rec.mu.Unlock()
		if failOn == "create" {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		fmt.Fprint(w, `{"Id":"newid"}`)
	})
	// 镜像拉取走 NDJSON 流，给一行合法进度即可。
	mux.HandleFunc(pathImageCreate, func(w http.ResponseWriter, r *http.Request) {
		rec.add("pull")
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintln(w, `{"status":"Pulling from library/nginx"}`)
		fmt.Fprintln(w, `{"status":"Download complete"}`)
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	rt, err := probe(context.Background(), "tcp://"+strings.TrimPrefix(srv.URL, "http://"), false)
	if err != nil {
		t.Fatalf("探测假引擎失败: %v", err)
	}
	return rt, rec
}

// TestRecreateOrderAndNewID 验证成功路径的动作顺序，并确认返回的是新容器 ID。
func TestRecreateOrderAndNewID(t *testing.T) {
	rt, rec := newRecreateEngine(t, recreateInspectFixture, "")
	cap, ok := rt.(ContainerRecreateRuntime)
	if !ok {
		t.Fatal("假引擎应实现 ContainerRecreateRuntime")
	}

	newID, err := cap.RecreateContainer(context.Background(), "web", false)
	if err != nil {
		t.Fatalf("重建失败: %v", err)
	}
	if newID != "newid" {
		t.Fatalf("应返回新容器 ID，实际 %q", newID)
	}

	calls := rec.snapshot()
	// 顺序是安全性本身：inspect → stop → rename → create → start → remove。
	want := []string{"inspect", "stop", "rename:web-arkade-old-", "create", "start:newid", "remove"}
	if len(calls) != len(want) {
		t.Fatalf("动作数量不符，期望 %d 个，实际 %v", len(want), calls)
	}
	if calls[0] != want[0] || calls[1] != want[1] {
		t.Fatalf("前两步应为 inspect→stop，实际 %v", calls)
	}
	// 改名必须带随机后缀，否则重试时会撞名；这里只校验前缀。
	if !strings.HasPrefix(calls[2], want[2]) {
		t.Fatalf("第三步应为改名并加临时后缀，实际 %q", calls[2])
	}
	if calls[3] != "create" || calls[4] != "start:newid" || calls[5] != "remove" {
		t.Fatalf("后续顺序应为 create→start→remove，实际 %v", calls)
	}
	if indexOf(calls, "pull") != -1 {
		t.Error("未勾选重新拉取时不应拉镜像")
	}
}

// TestRecreateCopiesConfig 验证创建请求里保留了关键配置。
func TestRecreateCopiesConfig(t *testing.T) {
	rt, rec := newRecreateEngine(t, recreateInspectFixture, "")
	cap := rt.(ContainerRecreateRuntime)
	if _, err := cap.RecreateContainer(context.Background(), "web", false); err != nil {
		t.Fatalf("重建失败: %v", err)
	}

	var body recreateBody
	if err := json.Unmarshal(rec.createBody, &body); err != nil {
		t.Fatalf("创建请求体无法解析: %v", err)
	}
	if body.Image != "nginx:1.27" {
		t.Errorf("镜像应保留，实际 %q", body.Image)
	}
	if len(body.Env) != 2 || body.Env[0] != "A=1" {
		t.Errorf("环境变量应保留，实际 %v", body.Env)
	}
	if body.WorkingDir != "/work" {
		t.Errorf("工作目录应保留，实际 %q", body.WorkingDir)
	}
	if body.Labels["com.docker.compose.project"] != "demo" {
		t.Errorf("标签应保留（否则编排会认不出这个容器），实际 %v", body.Labels)
	}
	// 端口映射漏了就会变成"起得来但外部访问不了"。
	if got := body.HostConfig.PortBindings["80/tcp"]; len(got) != 1 || got[0].HostPort != "8080" {
		t.Errorf("端口映射应保留，实际 %v", body.HostConfig.PortBindings)
	}
	if body.HostConfig.RestartPolicy.Name != "unless-stopped" {
		t.Errorf("重启策略应保留，实际 %q", body.HostConfig.RestartPolicy.Name)
	}
	if len(body.HostConfig.Binds) != 1 || body.HostConfig.Binds[0] != "/data:/data:rw" {
		t.Errorf("绑定挂载应保留，实际 %v", body.HostConfig.Binds)
	}
	if body.HostConfig.Memory != 536870912 || body.HostConfig.NanoCpus != 1000000000 {
		t.Errorf("资源限制应保留，实际 %+v", body.HostConfig)
	}
	// 网络别名丢了就等于"容器之间靠服务名互访"全部失效，且没有任何报错。
	if body.NetworkingConfig == nil {
		t.Fatal("自定义网络应带 NetworkingConfig")
	}
	if eps := body.NetworkingConfig.EndpointsConfig["demo-net"]; len(eps.Aliases) != 2 {
		t.Errorf("网络别名应保留，实际 %v", eps.Aliases)
	}
}

// TestRecreatePullsBeforeStopping 验证拉镜像发生在停容器之前。
//
// 反过来的顺序会让"拉取失败"变成"容器已停但没建回来"。
func TestRecreatePullsBeforeStopping(t *testing.T) {
	rt, rec := newRecreateEngine(t, recreateInspectFixture, "")
	cap := rt.(ContainerRecreateRuntime)
	if _, err := cap.RecreateContainer(context.Background(), "web", true); err != nil {
		t.Fatalf("重建失败: %v", err)
	}
	calls := rec.snapshot()
	pull := indexOf(calls, "pull")
	stop := indexOf(calls, "stop")
	if pull == -1 {
		t.Fatal("勾选重新拉取后应调用拉取端点")
	}
	if pull > stop {
		t.Fatalf("拉取必须在停止之前，实际顺序 %v", calls)
	}
}

// TestRecreateRollbackWhenCreateFails 验证创建失败时旧容器被改回原名并重新启动。
func TestRecreateRollbackWhenCreateFails(t *testing.T) {
	rt, rec := newRecreateEngine(t, recreateInspectFixture, "create")
	cap := rt.(ContainerRecreateRuntime)

	if _, err := cap.RecreateContainer(context.Background(), "web", false); err == nil {
		t.Fatal("创建失败时重建必须报错")
	}
	calls := rec.snapshot()
	// 回滚的两步：改回原名 + 重新启动旧容器。
	if indexOf(calls, "rename:web") == -1 {
		t.Fatalf("应把旧容器改回原名，实际 %v", calls)
	}
	if indexOf(calls, "start:oldid") == -1 {
		t.Fatalf("应重新启动旧容器，实际 %v", calls)
	}
	if indexOf(calls, "remove") != -1 {
		t.Errorf("创建阶段失败时没有新容器可删，不应调用删除，实际 %v", calls)
	}
}

// TestRecreateRollbackWhenStartFails 验证新容器启动失败时删掉半成品并恢复旧容器。
func TestRecreateRollbackWhenStartFails(t *testing.T) {
	rt, rec := newRecreateEngine(t, recreateInspectFixture, "start")
	cap := rt.(ContainerRecreateRuntime)

	if _, err := cap.RecreateContainer(context.Background(), "web", false); err == nil {
		t.Fatal("启动失败时重建必须报错")
	}
	calls := rec.snapshot()
	if indexOf(calls, "remove") == -1 {
		t.Fatalf("应删除启动失败的半成品容器，实际 %v", calls)
	}
	if indexOf(calls, "rename:web") == -1 || indexOf(calls, "start:oldid") == -1 {
		t.Fatalf("应恢复旧容器原名并重新启动，实际 %v", calls)
	}
}

// TestRecreateRejectsHelperContainer 验证内部临时容器不会被重建。
func TestRecreateRejectsHelperContainer(t *testing.T) {
	fixture := strings.Replace(recreateInspectFixture, `"Name": "/web"`,
		`"Name": "/arkade-helper-abc123"`, 1)
	rt, rec := newRecreateEngine(t, fixture, "")
	cap := rt.(ContainerRecreateRuntime)

	if _, err := cap.RecreateContainer(context.Background(), "arkade-helper-abc123", false); err == nil {
		t.Fatal("内部临时容器必须被拒绝重建")
	}
	if calls := rec.snapshot(); len(calls) != 1 || calls[0] != "inspect" {
		t.Fatalf("拒绝时除 inspect 外不应有任何动作，实际 %v", calls)
	}
}

// TestRecreateSkipsEndpointsForHostNetwork 验证 host 网络模式不传 EndpointsConfig。
//
// 传了会被引擎拒绝，表现为一个看不懂的 500。
func TestRecreateSkipsEndpointsForHostNetwork(t *testing.T) {
	fixture := strings.Replace(recreateInspectFixture,
		`"NetworkMode": "demo-net"`, `"NetworkMode": "host"`, 1)
	rt, rec := newRecreateEngine(t, fixture, "")
	cap := rt.(ContainerRecreateRuntime)
	if _, err := cap.RecreateContainer(context.Background(), "web", false); err != nil {
		t.Fatalf("host 网络模式也应能重建: %v", err)
	}

	var body recreateBody
	if err := json.Unmarshal(rec.createBody, &body); err != nil {
		t.Fatalf("创建请求体无法解析: %v", err)
	}
	if body.NetworkingConfig != nil {
		t.Errorf("host 模式不应带 NetworkingConfig，实际 %+v", body.NetworkingConfig)
	}
}

// TestRecreateKeepsStoppedState 验证重建一个已停止的容器后它仍然是停止的。
//
// 这是很容易写错的一条：实现里为了腾端口会先停容器，
// 如果无条件再启动新容器，一个本该停着的服务就自己上线了。
func TestRecreateKeepsStoppedState(t *testing.T) {
	fixture := strings.Replace(recreateInspectFixture,
		`"State": {"Running": true, "Status": "running"}`,
		`"State": {"Running": false, "Status": "exited"}`, 1)
	rt, rec := newRecreateEngine(t, fixture, "")
	cap := rt.(ContainerRecreateRuntime)

	if _, err := cap.RecreateContainer(context.Background(), "web", false); err != nil {
		t.Fatalf("重建已停止的容器失败: %v", err)
	}
	calls := rec.snapshot()
	if indexOf(calls, "stop") != -1 {
		t.Errorf("已停止的容器不需要再停，实际 %v", calls)
	}
	for _, c := range calls {
		if strings.HasPrefix(c, "start:") {
			t.Fatalf("重建后不应启动容器，实际 %v", calls)
		}
	}
	if indexOf(calls, "create") == -1 || indexOf(calls, "remove") == -1 {
		t.Fatalf("仍应创建新容器并删除旧容器，实际 %v", calls)
	}
}

// TestRecreateMountsFallbackToInspect 验证 --mount 语法创建的容器不会丢挂载。
func TestRecreateMountsFallbackToInspect(t *testing.T) {
	// Binds 为空、HostConfig.Mounts 也为空，只有顶层 Mounts 有值。
	fixture := `{
      "Id":"oldid","Name":"/web",
      "Config":{"Image":"nginx:1.27"},
      "HostConfig":{"NetworkMode":"bridge"},
      "Mounts":[{"Type":"volume","Name":"v1","Source":"/var/lib/docker/volumes/v1/_data",
                 "Destination":"/data","ReadOnly":false}]
    }`
	old := recreateInspect{}
	if err := json.Unmarshal([]byte(fixture), &old); err != nil {
		t.Fatalf("夹具无法解析: %v", err)
	}
	mounts := recreateMounts(&old)
	if len(mounts) != 1 {
		t.Fatalf("应从顶层 Mounts 恢复挂载，实际 %v", mounts)
	}
	if mounts[0].Source != "/var/lib/docker/volumes/v1/_data" || mounts[0].Target != "/data" {
		t.Errorf("挂载内容不符，实际 %+v", mounts[0])
	}
}
