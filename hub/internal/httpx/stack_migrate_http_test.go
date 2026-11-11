package httpx

// stack_migrate_http_test.go：迁移接口的 HTTP 级测试。
//
// ============================ 为什么必须有 HTTP 级测试 ============================
// 接口层能测到的东西，处理器内部测试测不到：
//
//	路由挂上了没有        —— 忘了注册 → 404 → 前端显示「后端尚未部署」
//	鉴权生效了没有        —— 权限没查 → 任何人都能迁别人的编排
//	JSON 字段绑对了没有   —— 字段名不匹配 → include_volumes 永远是默认值
//	状态码对不对          —— 409/403/400/202 混淆 → 前端文案全错
//
// 这四类正是与前端对接时最容易坏的地方，也都是"单测全绿但一联调就废"的经典成因。
//
// ============================ 只测三条主路径 + 边界 ============================
// 阶段编排、失败报告、卷分支等逻辑已在 stackmigrate 包内用 30+ 用例覆盖。
// 这里只测**HTTP 契约**，不重复那些断言。

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"testing"
	"time"

	"arkade/hub/internal/stackmigrate"
)

// httpMigrateBackend 是迁移编排的假后端。
//
// 全部立即返回，不做任何 IO：HTTP 测试要验的是契约，不是传输。
type httpMigrateBackend struct {
	mu sync.Mutex
	// existsTarget 决定 StackExists 的返回值。
	existsTarget bool
	// canManage 决定 CanManage 的返回值。
	canManage bool
	// images / volumes 决定清单内容。
	images  []string
	volumes []string
	// copyErr 让镜像搬运失败。
	copyErr error
	// calls 记录被调用的方法名。
	calls []string
}

func newHTTPMigrateBackend() *httpMigrateBackend {
	return &httpMigrateBackend{
		canManage: true,
		images:    []string{"nginx:latest"},
		volumes:   []string{"demo_data"},
	}
}

func (b *httpMigrateBackend) record(s string) {
	b.mu.Lock()
	b.calls = append(b.calls, s)
	b.mu.Unlock()
}

func (b *httpMigrateBackend) CanManage(ctx context.Context, agentID, stackName string) (bool, error) {
	b.record("canmanage:" + agentID)
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.canManage, nil
}

func (b *httpMigrateBackend) AgentOnline(agentID string) bool {
	b.record("online:" + agentID)
	return true
}

func (b *httpMigrateBackend) StackExists(ctx context.Context, agentID, stackName string) (bool, error) {
	b.record("exists:" + agentID)
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.existsTarget, nil
}

func (b *httpMigrateBackend) StackBusy(ctx context.Context, agentID, stackName string) (bool, error) {
	b.record("busy:" + agentID)
	return false, nil
}

func (b *httpMigrateBackend) ReadCompose(ctx context.Context, agentID, stackName string) (string, string, string, error) {
	b.record("read:" + agentID)
	return "services:\n  web:\n    image: nginx:latest\n", "", "abcdef0123456789", nil
}

func (b *httpMigrateBackend) ImagesOf(ctx context.Context, agentID, stackName string) ([]string, error) {
	b.record("images:" + agentID)
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.images, nil
}

func (b *httpMigrateBackend) CopyImage(ctx context.Context, source, target, reference string, progress func(int64, int64)) error {
	b.record("copyimage:" + reference)
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.copyErr
}

func (b *httpMigrateBackend) VolumesOf(ctx context.Context, agentID, stackName string) (stackmigrate.VolumePlan, error) {
	b.record("volumes:" + agentID)
	b.mu.Lock()
	defer b.mu.Unlock()
	return stackmigrate.VolumePlan{Volumes: b.volumes}, nil
}

func (b *httpMigrateBackend) CopyVolume(ctx context.Context, source, target, volume string, progress func(int64, int64)) error {
	b.record("copyvolume:" + volume)
	return nil
}

func (b *httpMigrateBackend) ImportCompose(ctx context.Context, agentID, stackName, config, env, hash string) error {
	b.record("import:" + agentID)
	return nil
}

func (b *httpMigrateBackend) StartStack(ctx context.Context, agentID, stackName string) error {
	b.record("start:" + agentID)
	return nil
}

// callCount 统计某前缀的调用次数。
func (b *httpMigrateBackend) callCount(prefix string) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := 0
	for _, c := range b.calls {
		if len(c) >= len(prefix) && c[:len(prefix)] == prefix {
			n++
		}
	}
	return n
}

func (b *httpMigrateBackend) reset() {
	b.mu.Lock()
	b.calls = nil
	b.mu.Unlock()
}

// seedTargetAgent 在库里登记一台目标服务器。
//
// ============================ 为什么必须真的登记 ============================
// requirePermission 会先确认 Agent 存在，否则直接 404「服务器不存在」。
// 这正是我们要的语义（不能对不存在的服务器查询授权），
// 因此测试必须先把目标机建出来，否则测到的是"服务器不存在"而不是迁移行为。
//
// 权限：admin 角色对所有服务器都有 manage，因此不需要额外授权。
func seedTargetAgent(t *testing.T, e *testEnv, id, name string) {
	t.Helper()
	if err := e.db.UpsertAgent(newTestContext(), agentRecordFor(id, name)); err != nil {
		t.Fatalf("登记目标服务器失败: %v", err)
	}
}

// migrateTaskDTO 是接口返回的任务视图（前端读的字段）。
type migrateTaskDTO struct {
	Task struct {
		TaskID string `json:"task_id"`
		Name   string `json:"name"`
		Action string `json:"action"`
		Status string `json:"status"`
		Output []struct {
			Seq    int64  `json:"seq"`
			Stream string `json:"stream"`
			Text   string `json:"text"`
		} `json:"output"`
		NextCursor int64  `json:"next_cursor"`
		Error      string `json:"error"`
		Migrate    struct {
			SourceAgentID  string `json:"source_agent_id"`
			TargetAgentID  string `json:"target_agent_id"`
			IncludeVolumes bool   `json:"include_volumes"`
			StartAfter     bool   `json:"start_after"`
			Stage          string `json:"stage"`
			SkippedVolumes int    `json:"skipped_volumes"`
		} `json:"migrate"`
	} `json:"task"`
}

// ============================ 路径 1：成功发起迁移 ============================
func TestStackMigrateHTTPSuccess(t *testing.T) {
	e := newTestEnv(t)
	a := startImportAgent(t, e, nil)
	seedTargetAgent(t, e, "agent-target", "目标机")
	cookies := e.loginAsFor(t, a.url, "admin", testPassword)

	body := `{"target_agent_id":"agent-target","include_volumes":false,` +
		`"start_after":false,"confirmation":"demo"}`
	resp, raw := doHTTP(t, http.MethodPost,
		a.url+"/api/agents/"+a.id+"/stacks/demo/migrate", body, cookies)
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("发起迁移应返回 202，实际 %d %s", resp.StatusCode, raw)
	}

	var out migrateTaskDTO
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("响应不是合法 JSON: %v %s", err, raw)
	}
	// ============================ 契约字段逐项校验 ============================
	// 前端靠这几个字段匹配弹窗、渲染日志、显示动作名。
	if out.Task.TaskID == "" {
		t.Fatalf("必须返回 task_id: %s", raw)
	}
	if out.Task.Name != "demo" {
		t.Fatalf("task.name 必须是源编排名，实际 %q", out.Task.Name)
	}
	if out.Task.Action != "migrate" {
		t.Fatalf("task.action 必须是 migrate，实际 %q", out.Task.Action)
	}
	if out.Task.Status == "" {
		t.Fatalf("必须返回 status: %s", raw)
	}
	if out.Task.Migrate.TargetAgentID != "agent-target" {
		t.Fatalf("应回显目标服务器，实际 %q", out.Task.Migrate.TargetAgentID)
	}
	if out.Task.Migrate.IncludeVolumes {
		t.Fatalf("include_volumes=false 不应被绑成 true: %s", raw)
	}
	if out.Task.Migrate.StartAfter {
		t.Fatalf("start_after=false 不应被绑成 true: %s", raw)
	}

	// ============================ 进度端点必须能读到它 ============================
	// 这是前端点"查看日志"时真正走的路径。
	// 若 operation-tasks 的回落没接上，这里会 404，
	// 而前端会把 404 显示成「后端尚未部署」。
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		resp, raw := doHTTP(t, http.MethodGet,
			a.url+"/api/agents/"+a.id+"/stacks/demo/operation-tasks/"+out.Task.TaskID+"?after=0",
			"", cookies)
		if resp.StatusCode == http.StatusNotFound {
			t.Fatalf("迁移任务在进度端点上 404（前端会显示「后端尚未部署」）: %s", raw)
		}
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("读取迁移任务失败 %d %s", resp.StatusCode, raw)
		}
		var cur migrateTaskDTO
		if err := json.Unmarshal(raw, &cur); err != nil {
			t.Fatalf("进度响应不是合法 JSON: %v %s", err, raw)
		}
		if cur.Task.TaskID != out.Task.TaskID {
			t.Fatalf("进度端点返回了别的任务：%q vs %q", cur.Task.TaskID, out.Task.TaskID)
		}
		if cur.Task.Status == stackmigrate.StatusSucceeded || cur.Task.Status == stackmigrate.StatusFailed {
			// ============================ 未勾选卷的关键断言 ============================
			// 走完整个迁移，卷搬运一次都不该被调用。
			if n := e.migrateBackend.callCount("copyvolume:"); n != 0 {
				t.Fatalf("include_volumes=false 时不得触发任何卷导出，实际 %d 次", n)
			}
			if cur.Task.Status != stackmigrate.StatusSucceeded {
				t.Fatalf("迁移应成功，实际 %s: %s", cur.Task.Status, cur.Task.Error)
			}
			// 未勾选时跳过卷数应被如实上报。
			if cur.Task.Migrate.SkippedVolumes != 1 {
				t.Fatalf("应报告跳过 1 个数据卷，实际 %d", cur.Task.Migrate.SkippedVolumes)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("迁移任务未在 5 秒内结束")
}

// ============================ 路径 2：目标同名拒绝（409） ============================
func TestStackMigrateHTTPTargetConflict(t *testing.T) {
	e := newTestEnv(t)
	a := startImportAgent(t, e, nil)
	seedTargetAgent(t, e, "agent-target", "目标机")
	cookies := e.loginAsFor(t, a.url, "admin", testPassword)

	// 目标上已有同名编排。
	e.migrateBackend.mu.Lock()
	e.migrateBackend.existsTarget = true
	e.migrateBackend.mu.Unlock()

	body := `{"target_agent_id":"agent-target","include_volumes":false,` +
		`"start_after":false,"confirmation":"demo"}`
	resp, raw := doHTTP(t, http.MethodPost,
		a.url+"/api/agents/"+a.id+"/stacks/demo/migrate", body, cookies)
	// 受理是异步的：接口本身返回 202，冲突在任务里报告。
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("发起应被受理（冲突在任务内报告），实际 %d %s", resp.StatusCode, raw)
	}
	var out migrateTaskDTO
	_ = json.Unmarshal(raw, &out)

	// 等任务失败，并检查失败原因说明同名冲突且提示改名。
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		_, raw := doHTTP(t, http.MethodGet,
			a.url+"/api/agents/"+a.id+"/stacks/demo/operation-tasks/"+out.Task.TaskID+"?after=0",
			"", cookies)
		var cur migrateTaskDTO
		_ = json.Unmarshal(raw, &cur)
		if cur.Task.Status == stackmigrate.StatusFailed {
			if !containsAny(cur.Task.Error, "已存在同名编排", "改名") {
				t.Fatalf("失败原因应说明同名冲突并提示改名，实际 %q", cur.Task.Error)
			}
			// ============================ 绝不能有任何写入 ============================
			if n := e.migrateBackend.callCount("import:"); n != 0 {
				t.Fatalf("同名冲突时绝不能写入目标配置，实际 %d 次", n)
			}
			if n := e.migrateBackend.callCount("copyimage:"); n != 0 {
				t.Fatalf("同名冲突时不应开始搬运镜像，实际 %d 次", n)
			}
			return
		}
		if cur.Task.Status == stackmigrate.StatusSucceeded {
			t.Fatal("目标同名时迁移不应成功")
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("迁移任务未在 5 秒内结束")
}

// ============================ 路径 3：权限不足（403） ============================
func TestStackMigrateHTTPPermissionDenied(t *testing.T) {
	e := newTestEnv(t)
	a := startImportAgent(t, e, nil)

	// 未授权的普通用户。
	e.createUser(t, e.login(t), "migrate-ungranted", testPassword, "user")
	limited := e.loginAsFor(t, a.url, "migrate-ungranted", testPassword)

	body := `{"target_agent_id":"agent-target","include_volumes":false,` +
		`"start_after":false,"confirmation":"demo"}`
	resp, raw := doHTTP(t, http.MethodPost,
		a.url+"/api/agents/"+a.id+"/stacks/demo/migrate", body, limited)
	// ============================ 必须是 403，不能是 404 ============================
	// 前端约定：404 显示「后端尚未部署」，403 显示权限问题。
	// 若这里返回 404，用户会被告知"功能没部署"——而实际上是他的权限不够，
	// 他永远等不到功能上线。
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("无权限发起迁移应返回 403，实际 %d %s", resp.StatusCode, raw)
	}
	// 未授权请求绝不能到达编排器。
	if n := e.migrateBackend.callCount("online:"); n != 0 {
		t.Fatalf("无权限时不应开始任何迁移动作，实际调用后端 %d 次", n)
	}

	// 未登录。
	resp, _ = doHTTP(t, http.MethodPost,
		a.url+"/api/agents/"+a.id+"/stacks/demo/migrate", body, nil)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("未登录应返回 401，实际 %d", resp.StatusCode)
	}
}

// ============================ 参数校验（400） ============================
func TestStackMigrateHTTPValidation(t *testing.T) {
	e := newTestEnv(t)
	a := startImportAgent(t, e, nil)
	cookies := e.loginAsFor(t, a.url, "admin", testPassword)
	path := a.url + "/api/agents/" + a.id + "/stacks/demo/migrate"

	cases := []struct {
		name string
		body string
	}{
		{"缺少目标服务器", `{"confirmation":"demo"}`},
		{"目标与源相同", `{"target_agent_id":"` + a.id + `","confirmation":"demo"}`},
		{"确认值缺失", `{"target_agent_id":"agent-target"}`},
		// ============================ 确认值必须等于编排名 ============================
		// 只判非空不够：一个写死 "yes" 的客户端也能通过，
		// 但它显然没让用户真的确认"迁移哪个编排"。
		{"确认值与编排名不符", `{"target_agent_id":"agent-target","confirmation":"other"}`},
		{"非法 JSON", `{not json`},
	}
	for _, c := range cases {
		resp, raw := doHTTP(t, http.MethodPost, path, c.body, cookies)
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("%s：应返回 400，实际 %d %s", c.name, resp.StatusCode, raw)
		}
	}
	// 全部在参数校验阶段被拒，未触达编排器。
	if n := e.migrateBackend.callCount("online:"); n != 0 {
		t.Fatalf("非法请求不应触达编排器，实际 %d 次", n)
	}
}

// ============================ include_volumes 默认值 ============================
func TestStackMigrateHTTPIncludeVolumesDefault(t *testing.T) {
	e := newTestEnv(t)
	a := startImportAgent(t, e, nil)
	seedTargetAgent(t, e, "agent-target", "目标机")
	cookies := e.loginAsFor(t, a.url, "admin", testPassword)

	// 完全不传 include_volumes：默认必须是**不迁卷**。
	body := `{"target_agent_id":"agent-target","confirmation":"demo"}`
	resp, raw := doHTTP(t, http.MethodPost,
		a.url+"/api/agents/"+a.id+"/stacks/demo/migrate", body, cookies)
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("应受理，实际 %d %s", resp.StatusCode, raw)
	}
	var out migrateTaskDTO
	_ = json.Unmarshal(raw, &out)
	if out.Task.Migrate.IncludeVolumes {
		t.Fatal("未传 include_volumes 时必须默认不迁卷（默认 true 会悄悄搬几十 GB）")
	}

	// 等任务结束，确认一次卷搬运都没发生。
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		_, raw := doHTTP(t, http.MethodGet,
			a.url+"/api/agents/"+a.id+"/stacks/demo/operation-tasks/"+out.Task.TaskID+"?after=0",
			"", cookies)
		var cur migrateTaskDTO
		_ = json.Unmarshal(raw, &cur)
		if cur.Task.Status == stackmigrate.StatusSucceeded || cur.Task.Status == stackmigrate.StatusFailed {
			if n := e.migrateBackend.callCount("copyvolume:"); n != 0 {
				t.Fatalf("默认不迁卷时不得触发任何卷导出，实际 %d 次", n)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("迁移任务未在 5 秒内结束")
}

// containsAny 判断字符串是否包含任一子串。
func containsAny(s string, subs ...string) bool {
	for _, sub := range subs {
		if len(sub) > 0 && len(s) >= len(sub) {
			for i := 0; i+len(sub) <= len(s); i++ {
				if s[i:i+len(sub)] == sub {
					return true
				}
			}
		}
	}
	return false
}
