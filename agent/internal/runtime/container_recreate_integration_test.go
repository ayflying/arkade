//go:build integration

package runtime

// container_recreate_integration_test.go：在**真实** Docker 守护进程上验证重建。
//
// 为什么非要在真机上跑：重建是手写的 Docker API 调用序列
// （rename → create → start → remove）。假引擎能验证"我们发出了正确的请求"，
// 但验证不了"真实引擎接受这个请求体"——
// HostConfig 的字段名写错、Mounts 与 Binds 混用、NetworkingConfig 形状不对，
// httptest 一律返回 204，只有真守护进程会报错。
//
// 覆盖面：
//  1. 运行中的容器重建后**仍在运行**，且名称/镜像/环境变量/标签/挂载/端口/重启策略全部保留；
//  2. 已停止的容器重建后**仍然是停止状态**（不会被顺手启动）；
//  3. 旧容器确实被删掉，同一时刻只有一个同名容器。
//
// 手动跑法（Linux + 可访问的套接字）：
//
//	ARKADE_TEST_DOCKER_SOCKET=/var/run/docker.sock \
//	go test -tags integration ./internal/runtime/ -run TestRecreateContainerRealDaemon -v
//
// 测试自建容器并自行清理，不依赖机器上已有的任何容器；
// 镜像默认 alpine:3.20（可用 ARKADE_TEST_RECREATE_IMAGE 覆盖），需事先存在。

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRecreateContainerRealDaemon(t *testing.T) {
	sock := strings.TrimSpace(os.Getenv("ARKADE_TEST_DOCKER_SOCKET"))
	if sock == "" {
		t.Skip("未提供 ARKADE_TEST_DOCKER_SOCKET，跳过真实守护进程验证")
	}
	image := strings.TrimSpace(os.Getenv("ARKADE_TEST_RECREATE_IMAGE"))
	if image == "" {
		image = "alpine:3.20"
	}

	ctx := context.Background()
	rt, err := probe(ctx, "unix://"+sock, false)
	if err != nil {
		t.Fatalf("探测真实运行时失败: %v", err)
	}
	e, ok := rt.(*engine)
	if !ok {
		t.Fatalf("端点未解析为 Docker 引擎: %T", rt)
	}
	cap, ok := rt.(ContainerRecreateRuntime)
	if !ok {
		t.Fatal("引擎未实现 ContainerRecreateRuntime")
	}

	// 绑定挂载的源目录：真实存在于被测机器上，否则引擎会当成"要新建的空目录"
	// 而给出意料之外的结果，掩盖真正的配置丢失。
	dataDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dataDir, "keep.txt"), []byte("keep"), 0o644); err != nil {
		t.Fatalf("准备挂载目录失败: %v", err)
	}

	// 清理：无论用例怎么失败，都不能在机器上留下测试容器。
	name := "arkade-it-recreate-" + shortID()
	// 场景二的容器名在这里先算好，供清理使用：重建会换 ID，
	// 按"创建时捕获的 ID"清理会漏掉重建后的新容器。
	stoppedName := name + "-stopped"
	t.Cleanup(func() {
		/*
		 * 一律按**名字**清理，不按 ID。
		 * 原因：重建本身就换了 ID，用创建时捕获的旧 ID 清理只能删掉"已经被
		 * 重建删掉的那个"，真正残留的新容器反而留下。这里也顺带兜住
		 * "名字-arkade-old-*" 的回滚残留。
		 */
		list, lerr := e.List(context.Background(), true)
		if lerr != nil {
			return
		}
		for _, c := range list {
			base := strings.TrimPrefix(c.Name, "/")
			if base == name || base == stoppedName || strings.HasPrefix(base, name+"-arkade-old-") {
				_ = e.RemoveContainer(context.Background(), c.ID, true, false)
			}
		}
	})

	// ---- 场景一：运行中的容器 ----
	oldID := createIntegrationContainer(t, e, name, image, dataDir, true)

	before, err := e.InspectContainer(ctx, oldID)
	if err != nil {
		t.Fatalf("重建前读取详情失败: %v", err)
	}
	if before.State != "running" {
		t.Fatalf("前置条件不满足：容器应在运行，实际 %q", before.State)
	}

	newID, err := cap.RecreateContainer(ctx, name, false)
	if err != nil {
		t.Fatalf("真实守护进程上重建失败: %v", err)
	}
	if newID == "" || newID == oldID {
		t.Fatalf("应返回新容器 ID，old=%s new=%s", oldID, newID)
	}

	after, err := e.InspectContainer(ctx, newID)
	if err != nil {
		t.Fatalf("重建后读取详情失败: %v", err)
	}
	// 名称必须还是原来那个：重建的语义是"同一个服务换了容器对象"，
	// 名字变了意味着所有按名字互访的地方全断。
	if after.Name != name {
		t.Errorf("名称应保留，期望 %q 实际 %q", name, after.Name)
	}
	if after.Image != image {
		t.Errorf("镜像应保留，期望 %q 实际 %q", image, after.Image)
	}
	// 运行中的容器重建后必须仍在运行，否则等于"重建把服务停了"。
	if after.State != "running" {
		t.Errorf("原本在运行的容器重建后应仍在运行，实际 %q", after.State)
	}
	// 端口映射是重建最容易丢的字段之一：丢了服务就对外不可达。
	published := 0
	for _, p := range after.Ports {
		if p.Public > 0 {
			published++
		}
	}
	if published == 0 {
		t.Errorf("端口映射丢失：重建后没有任何已发布端口，实际 %+v", after.Ports)
	}
	// 挂载丢失会导致容器看起来正常、数据却写进了容器层。
	foundMount := false
	for _, m := range after.Mounts {
		if m.Destination == "/data" {
			foundMount = true
			if !strings.Contains(m.Source, strings.TrimSpace(dataDir)) {
				t.Errorf("挂载源应保留，期望含 %q 实际 %q", dataDir, m.Source)
			}
		}
	}
	if !foundMount {
		t.Errorf("挂载丢失：重建后没有 /data 挂载，实际 %+v", after.Mounts)
	}

	/*
	 * 重启策略、标签、环境变量都在 InspectDetail 的投影之外
	 * （它只保留列表/详情展示需要的那部分字段），
	 * 所以这里直接读一次原始 inspect JSON。
	 * 这三项恰好都是"丢了不会立刻报错，但会在某天以奇怪方式炸掉"的类型：
	 * 重启策略丢了宿主重启后服务不会自己回来；标签丢了编排/统计侧就看不到它。
	 */
	raw := fetchRawInspect(t, e, newID)
	if got := raw.HostConfig.RestartPolicy.Name; got != "no" {
		t.Errorf("重启策略应保留，期望 no 实际 %q", got)
	}
	if raw.Config.Labels["arkade.it"] != "recreate" {
		t.Errorf("标签应保留，实际 %v", raw.Config.Labels)
	}
	envJoined := strings.Join(raw.Config.Env, ",")
	for _, want := range []string{"IT_A=1", "IT_B=two"} {
		if !strings.Contains(envJoined, want) {
			t.Errorf("环境变量应保留 %q，实际 %v", want, raw.Config.Env)
		}
	}

	// 旧容器必须已被删除，且同名容器只有一个。
	if _, err := e.InspectContainer(ctx, oldID); err == nil {
		t.Error("旧容器仍然存在，重建没有完成收尾")
	} else if !errors.Is(err, ErrNotFound) {
		t.Errorf("查询旧容器应得到 NotFound，实际 %v", err)
	}
	list, err := e.List(ctx, true)
	if err != nil {
		t.Fatalf("列出容器失败: %v", err)
	}
	sameName := 0
	for _, c := range list {
		if strings.TrimPrefix(c.Name, "/") == name {
			sameName++
		}
	}
	if sameName != 1 {
		t.Errorf("同名容器应恰好 1 个，实际 %d", sameName)
	}

	// ---- 场景二：已停止的容器 ----
	// 清理已由上面的按名字清理统一兜住（stoppedName 就是为此提前算出的），
	// 这里不再单独按 ID 注册清理：重建会换 ID，按 ID 清会漏。
	stoppedID := createIntegrationContainer(t, e, stoppedName, image, dataDir, false)
	_ = stoppedID

	stoppedNewID, err := cap.RecreateContainer(ctx, stoppedName, false)
	if err != nil {
		t.Fatalf("重建已停止的容器失败: %v", err)
	}
	stopped, err := e.InspectContainer(ctx, stoppedNewID)
	if err != nil {
		t.Fatalf("读取重建后的停止容器失败: %v", err)
	}
	if stopped.State == "running" {
		t.Error("已停止的容器重建后不应被启动")
	}
}

// createIntegrationContainer 直接调用引擎接口建一个带完整配置的容器。
//
// 刻意不走 docker run：这里要考的正是"我们自己的创建请求体能不能被接受"，
// 用 CLI 建容器会绕过被测代码。
func createIntegrationContainer(t *testing.T, e *engine, name, image, dataDir string, start bool) string {
	t.Helper()
	ctx := context.Background()

	body := map[string]any{
		"Image": image,
		// 保持运行的命令：alpine 自带的 sleep，避免容器立刻退出。
		"Cmd":    []string{"sleep", "600"},
		"Env":    []string{"IT_A=1", "IT_B=two"},
		"Labels": map[string]string{"arkade.it": "recreate"},
		"ExposedPorts": map[string]any{
			"8080/tcp": map[string]any{},
		},
		"HostConfig": map[string]any{
			"Binds": []string{dataDir + ":/data:rw"},
			// HostPort 留空让引擎随机分配：写死端口会与机器上已有服务冲突，
			// 让测试变得依赖环境；随机端口同样能验证"端口映射被复制"。
			"PortBindings": map[string]any{
				"8080/tcp": []map[string]string{{"HostIp": "", "HostPort": ""}},
			},
			"RestartPolicy": map[string]any{"Name": "no"},
			"NetworkMode":   "bridge",
		},
	}
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("构造创建请求失败: %v", err)
	}
	id, err := e.createContainer(ctx, name, raw)
	if err != nil {
		t.Fatalf("创建测试容器 %s 失败（镜像 %s 是否已存在？）: %v", name, image, err)
	}
	if start {
		if err := e.Start(ctx, id); err != nil {
			t.Fatalf("启动测试容器失败: %v", err)
		}
	}
	return id
}

// rawInspect 只抽取集成测试关心的字段，避免依赖 InspectDetail 的投影口径。
type rawInspect struct {
	Config struct {
		Env    []string          `json:"Env"`
		Labels map[string]string `json:"Labels"`
	} `json:"Config"`
	HostConfig struct {
		RestartPolicy struct {
			Name string `json:"Name"`
		} `json:"RestartPolicy"`
	} `json:"HostConfig"`
}

// fetchRawInspect 直接读一次原始 inspect JSON。
//
// 不经过 InspectContainer：那个方法为了详情页只投影了一部分字段，
// 重启策略/标签/环境变量都不在其中，而它们正是重建最容易漏拷的东西。
func fetchRawInspect(t *testing.T, e *engine, id string) rawInspect {
	t.Helper()
	var out rawInspect
	if err := e.client.getJSON(context.Background(), "/containers/"+id+"/json", &out); err != nil {
		t.Fatalf("读取原始 inspect 失败: %v", err)
	}
	return out
}
