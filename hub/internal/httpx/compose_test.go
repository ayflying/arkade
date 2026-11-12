package httpx

// compose_test.go 覆盖编排与资源代理接口。
//
// 测试策略说明（重要）：
// 编排接口依赖「Agent 在线」才能下发，但真正建一条 WebSocket 连接代价很高，
// 且与本文件的被测目标（Hub 侧存储 + 权限 + 乐观锁 + drift 合并）无关。
// 因此这里统一走「Agent 已登记但离线」这条真实路径：
//   - 列表/详情/创建/更新 在离线时依然必须成功（这正是任务要求的
//     "Agent 离线也不能丢用户写的编排"），测试直接断言这条规则；
//   - 需要 Agent 的动作类接口，离线时断言 503 agent_offline。
//
// 这样做的好处是测试完全确定、不依赖时序；代价是"下发成功"分支未被覆盖，
// 该分支由 agentlink 层的单元测试与端到端联调覆盖。

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"arkade/hub/internal/store"
	"arkade/hub/internal/user"
)

// sampleCompose 是测试用的合法编排内容。
const sampleCompose = `services:
  web:
    image: nginx:1.27-alpine
    ports:
      - "8080:80"
  cache:
    image: redis:7-alpine
`

// createStackVia 通过接口创建一个编排，返回解析后的响应体。
func (e *testEnv) createStackVia(t *testing.T, cookies []*http.Cookie, agentID, name, content string) map[string]any {
	t.Helper()
	body := `{"name":"` + name + `","content":` + jsonString(content) + `}`
	rec, parsed := e.do(t, http.MethodPost, "/api/agents/"+agentID+"/stacks", body, cookies)
	if rec.Code != http.StatusOK {
		t.Fatalf("创建编排 %s 应成功，实际 %d，响应 %s（错误码 %q）",
			name, rec.Code, rec.Body.String(), parsed.Error.Code)
	}
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("解析创建编排响应失败: %v", err)
	}
	return out
}

// jsonString 把任意文本安全地转成 JSON 字符串字面量（含换行与引号）。
func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// seedAgent 在数据库里登记一台 Agent 记录（不建立真实连接）。
//
// 为什么必须先登记：权限闸门 requirePermission 会先校验服务器是否存在，
// 对不存在的 agentID 一律返回 404「服务器不存在」——这是刻意的行为，
// 避免对随机 ID 反复查询授权表。因此所有编排/资源接口的测试都必须先有这台 Agent。
// 登记之后它依然是"离线"的（在线状态只认内存里的连接表），正好符合本文件的用例需求。
func (e *testEnv) seedAgent(t *testing.T, agentID string) {
	t.Helper()
	now := time.Now().UTC().Unix()
	if err := e.db.UpsertAgent(newTestContext(), store.AgentRecord{
		ID: agentID, Name: "测试机-" + agentID, Status: "offline",
		Labels: map[string]string{}, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("登记测试 Agent 失败: %v", err)
	}
}

// seedStack 直接往数据库插一条编排记录，用于绕过创建接口准备数据。
func (e *testEnv) seedStack(t *testing.T, agentID, name, content string) store.ComposeStack {
	t.Helper()
	now := time.Now().UTC()
	rec := store.ComposeStack{
		ID: "stk-test-" + name, AgentID: agentID, Name: name,
		Content: content, ConfigHash: composeConfigHash(content), Version: 1,
		CreatedAt: now, UpdatedAt: now,
	}
	if err := e.db.CreateComposeStack(newTestContext(), rec); err != nil {
		t.Fatalf("准备编排数据失败: %v", err)
	}
	return rec
}

// ---- 项目名校验 ----

// TestValidComposeName 验证项目名白名单，重点是路径穿越。
//
// 这是安全边界测试：项目名会拼进 Agent 本机路径
// （<data_dir>/stacks/<name>/compose.yml）与 docker compose -p 参数，
// 任何一个穿过的用例都意味着可以写到数据目录之外。
func TestValidComposeName(t *testing.T) {
	valid := []string{"web", "my-stack", "a1", "a_b", "a-b_c", "0", "abc123-_x"}
	for _, s := range valid {
		if !validComposeName(s) {
			t.Errorf("%q 应合法", s)
		}
	}

	invalid := []string{
		"",                      // 空
		"../etc",                // 路径穿越
		"..",                    // 父目录
		".",                     // 当前目录
		"a/b",                   // 路径分隔符
		"a\\b",                  // Windows 分隔符
		"a..b",                  // 内含 ..
		"-a",                    // 首字符是连字符（会被 docker 当参数）
		"_a",                    // 首字符是下划线
		"WEB",                   // 大写
		"Web",                   // 混合大小写
		"my stack",              // 空格
		"a;rm -rf /",            // 命令注入
		"$(whoami)",             // 命令替换
		"a`id`b",                // 反引号
		"a\nb",                  // 换行
		"a\tb",                  // 制表符
		"名字",                    // 非 ASCII
		"a.b",                   // 点号
		strings.Repeat("a", 65), // 超长
	}
	for _, s := range invalid {
		if validComposeName(s) {
			t.Errorf("%q 应被拒绝（可能是路径穿越或注入风险）", s)
		}
	}
}

// TestComposeConfigHash 验证哈希稳定性与长度。
func TestComposeConfigHash(t *testing.T) {
	h1 := composeConfigHash(sampleCompose)
	h2 := composeConfigHash(sampleCompose)
	if h1 != h2 {
		t.Fatalf("同样内容的哈希应稳定，实际 %q vs %q", h1, h2)
	}
	if len(h1) != composeHashHexLen {
		t.Fatalf("哈希长度应为 %d，实际 %d (%q)", composeHashHexLen, len(h1), h1)
	}
	// 一个字符之差必须产生不同哈希，否则 drift 检测会失效。
	changed := strings.Replace(sampleCompose, "1.27", "1.28", 1)
	if composeConfigHash(changed) == h1 {
		t.Fatal("内容变化后哈希必须改变")
	}
	if composeConfigHash("") == "" {
		t.Fatal("空内容的哈希不应为空串")
	}
}

// TestTruncateComposeOutput 验证输出截断保留尾部。
func TestTruncateComposeOutput(t *testing.T) {
	if got := truncateComposeOutput("  ok  \n"); got != "ok" {
		t.Errorf("短输出应原样保留，实际 %q", got)
	}
	// 失败原因在末尾，截断必须保留尾部。
	long := strings.Repeat("x", maxComposeOutputRunes) + "FAILURE_REASON"
	got := truncateComposeOutput(long)
	if !strings.HasSuffix(got, "FAILURE_REASON") {
		t.Error("截断后必须保留尾部（失败原因在末尾）")
	}
	if len([]rune(got)) > maxComposeOutputRunes+32 {
		t.Errorf("截断后长度超预期: %d", len([]rune(got)))
	}
}

// ---- 编排接口 ----

// TestCreateStackAgentOffline 验证核心规则：Agent 离线时创建仍然成功，
// 记录必须保存下来，不能因为下发失败就丢弃用户写的编排。
func TestCreateStackAgentOffline(t *testing.T) {
	env := newTestEnv(t)
	cookies := env.login(t)
	agentID := "agent-1"
	env.seedAgent(t, agentID)

	resp := env.createStackVia(t, cookies, agentID, "web", sampleCompose)

	rec, err := env.db.GetComposeStack(newTestContext(), agentID, "web")
	if err != nil {
		t.Fatalf("Agent 离线时编排记录必须已保存，实际查询失败: %v", err)
	}
	if rec.Content != sampleCompose {
		t.Error("保存的内容应与提交的一致")
	}
	if rec.ConfigHash != composeConfigHash(sampleCompose) {
		t.Error("保存的哈希应等于内容哈希")
	}
	if rec.Version != 1 {
		t.Errorf("新建编排版本应为 1，实际 %d", rec.Version)
	}

	if syncOK, _ := resp["sync_ok"].(bool); syncOK {
		t.Error("Agent 离线时 sync_ok 应为 false")
	}
	if hint, _ := resp["sync_hint"].(string); hint == "" {
		t.Error("下发失败时应给出 sync_hint 说明")
	}
}

// TestCreateStackValidation 验证创建接口的参数校验。
func TestCreateStackValidation(t *testing.T) {
	env := newTestEnv(t)
	cookies := env.login(t)
	agentID := "agent-1"
	env.seedAgent(t, agentID)

	cases := []struct {
		name string
		body string
		want string
	}{
		{"项目名含穿越", `{"name":"../evil","content":"services: {}"}`, "项目名不合法"},
		{"项目名大写", `{"name":"Web","content":"services: {}"}`, "项目名不合法"},
		{"项目名为空", `{"name":"","content":"services: {}"}`, "项目名不合法"},
		{"内容为空", `{"name":"web","content":""}`, "内容不能为空"},
		{"内容全是空白", `{"name":"web","content":"   \n\t "}`, "内容不能为空"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec, parsed := env.do(t, http.MethodPost, "/api/agents/"+agentID+"/stacks", c.body, cookies)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("应返回 400，实际 %d，响应 %s", rec.Code, rec.Body.String())
			}
			if !strings.Contains(parsed.Error.Message, c.want) {
				t.Errorf("错误文案应包含 %q，实际 %q", c.want, parsed.Error.Message)
			}
		})
	}
}

// TestCreateStackDuplicate 验证同一 Agent 下项目名重复返回 409。
func TestCreateStackDuplicate(t *testing.T) {
	env := newTestEnv(t)
	cookies := env.login(t)
	agentID := "agent-1"
	env.seedAgent(t, agentID)

	env.createStackVia(t, cookies, agentID, "web", sampleCompose)

	body := `{"name":"web","content":` + jsonString(sampleCompose) + `}`
	rec, parsed := env.do(t, http.MethodPost, "/api/agents/"+agentID+"/stacks", body, cookies)
	if rec.Code != http.StatusConflict {
		t.Fatalf("重名应返回 409，实际 %d，响应 %s", rec.Code, rec.Body.String())
	}
	if parsed.Error.Code != codeConflict {
		t.Errorf("错误码应为 %q，实际 %q", codeConflict, parsed.Error.Code)
	}
}

// TestUpdateStackOptimisticLock 验证乐观锁：用旧版本更新必须 409 且内容不变。
//
// 这是任务点名要"真测"的规则。测法：先正常更新一次把版本推到 2，
// 再用最初的 version=1 提交，必须被拒绝且数据库里的内容保持第 2 版。
func TestUpdateStackOptimisticLock(t *testing.T) {
	env := newTestEnv(t)
	cookies := env.login(t)
	agentID := "agent-1"
	env.seedAgent(t, agentID)
	env.seedStack(t, agentID, "web", sampleCompose)

	second := sampleCompose + "  # 第二版\n"
	body := `{"content":` + jsonString(second) + `,"version":1}`
	rec, parsed := env.do(t, http.MethodPut, "/api/agents/"+agentID+"/stacks/web", body, cookies)
	if rec.Code != http.StatusOK {
		t.Fatalf("用正确版本更新应成功，实际 %d，响应 %s（错误码 %q）", rec.Code, rec.Body.String(), parsed.Error.Code)
	}
	after1, err := env.db.GetComposeStack(newTestContext(), agentID, "web")
	if err != nil {
		t.Fatal(err)
	}
	if after1.Version != 2 {
		t.Fatalf("更新后版本应为 2，实际 %d", after1.Version)
	}

	// 用已过期的 version=1 提交——必须被拒绝。
	third := sampleCompose + "  # 第三版\n"
	staleBody := `{"content":` + jsonString(third) + `,"version":1}`
	rec, parsed = env.do(t, http.MethodPut, "/api/agents/"+agentID+"/stacks/web", staleBody, cookies)
	if rec.Code != http.StatusConflict {
		t.Fatalf("用旧版本更新应返回 409，实际 %d，响应 %s", rec.Code, rec.Body.String())
	}
	if parsed.Error.Code != codeConflict {
		t.Errorf("错误码应为 %q，实际 %q", codeConflict, parsed.Error.Code)
	}

	// 关键：内容绝不能被旧版本覆盖。
	after2, err := env.db.GetComposeStack(newTestContext(), agentID, "web")
	if err != nil {
		t.Fatal(err)
	}
	if after2.Content != second {
		t.Fatalf("版本冲突时内容不能被覆盖，期望仍为第二版，实际 %q", after2.Content)
	}
	if after2.Version != 2 {
		t.Errorf("版本冲突时版本号不应变化，实际 %d", after2.Version)
	}
}

// TestUpdateStackValidation 验证更新接口的参数校验。
func TestUpdateStackValidation(t *testing.T) {
	env := newTestEnv(t)
	cookies := env.login(t)
	agentID := "agent-1"
	env.seedAgent(t, agentID)
	env.seedStack(t, agentID, "web", sampleCompose)

	cases := []struct {
		name string
		body string
		want string
	}{
		{"缺 version", `{"content":"services: {}"}`, "version"},
		{"version 为 0", `{"content":"services: {}","version":0}`, "version"},
		{"内容为空", `{"content":"","version":1}`, "内容不能为空"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec, parsed := env.do(t, http.MethodPut, "/api/agents/"+agentID+"/stacks/web", c.body, cookies)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("应返回 400，实际 %d，响应 %s", rec.Code, rec.Body.String())
			}
			if !strings.Contains(parsed.Error.Message, c.want) {
				t.Errorf("错误文案应包含 %q，实际 %q", c.want, parsed.Error.Message)
			}
		})
	}
}

// TestUpdateStackNotFound 验证更新不存在的编排返回 404（而不是 409）。
//
// 记录不存在是 404，版本冲突才是 409。前端对两者的处理完全不同，必须分清。
func TestUpdateStackNotFound(t *testing.T) {
	env := newTestEnv(t)
	cookies := env.login(t)
	agentID := "agent-1"
	env.seedAgent(t, agentID)

	body := `{"content":"services: {}","version":1}`
	rec, parsed := env.do(t, http.MethodPut, "/api/agents/"+agentID+"/stacks/ghost", body, cookies)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("更新不存在的编排应返回 404，实际 %d，响应 %s", rec.Code, rec.Body.String())
	}
	if parsed.Error.Code != "not_found" {
		t.Errorf("错误码应为 not_found，实际 %q", parsed.Error.Code)
	}
}

// TestListStacksAgentOffline 验证 Agent 离线时列表仍返回 Hub 记录，状态是 offline。
func TestListStacksAgentOffline(t *testing.T) {
	env := newTestEnv(t)
	cookies := env.login(t)
	agentID := "agent-1"
	env.seedAgent(t, agentID)
	env.seedStack(t, agentID, "web", sampleCompose)
	env.seedStack(t, agentID, "db", "services:\n  db:\n    image: postgres:16\n")

	rec, parsed := env.do(t, http.MethodGet, "/api/agents/"+agentID+"/stacks", "", cookies)
	if rec.Code != http.StatusOK {
		t.Fatalf("Agent 离线时列表仍应返回 200，实际 %d，响应 %s（错误码 %q）",
			rec.Code, rec.Body.String(), parsed.Error.Code)
	}

	var resp struct {
		Stacks      []composeStackView `json:"stacks"`
		AgentOnline bool               `json:"agent_online"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("解析编排列表失败: %v", err)
	}
	if resp.AgentOnline {
		t.Error("agent_online 应为 false")
	}
	if len(resp.Stacks) != 2 {
		t.Fatalf("应返回 2 条 Hub 记录，实际 %d", len(resp.Stacks))
	}
	// 按项目名升序：db 在 web 前面。
	if resp.Stacks[0].Name != "db" || resp.Stacks[1].Name != "web" {
		t.Errorf("应按项目名升序，实际 %s, %s", resp.Stacks[0].Name, resp.Stacks[1].Name)
	}
	for _, s := range resp.Stacks {
		if s.Status != composeProjectStatusOffline {
			t.Errorf("%s 的状态应为 offline，实际 %q", s.Name, s.Status)
		}
		if s.ConfigHash == "" {
			t.Errorf("%s 应带 Hub 侧哈希", s.Name)
		}
		if s.Version != 1 {
			t.Errorf("%s 版本应为 1，实际 %d", s.Name, s.Version)
		}
		if s.Services == nil {
			t.Errorf("%s 的 services 应为空数组而不是 null", s.Name)
		}
	}
}

// TestGetStackDetail 验证详情返回内容与状态。
func TestGetStackDetail(t *testing.T) {
	env := newTestEnv(t)
	cookies := env.login(t)
	agentID := "agent-1"
	env.seedAgent(t, agentID)
	env.seedStack(t, agentID, "web", sampleCompose)

	rec, _ := env.do(t, http.MethodGet, "/api/agents/"+agentID+"/stacks/web", "", cookies)
	if rec.Code != http.StatusOK {
		t.Fatalf("详情应返回 200，实际 %d", rec.Code)
	}
	var resp struct {
		Stack   composeStackView `json:"stack"`
		Content string           `json:"content"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("解析详情失败: %v", err)
	}
	if resp.Content != "" {
		t.Error("运行详情不应包含项目配置内容")
	}
	if resp.Stack.Name != "web" {
		t.Errorf("名字应为 web，实际 %q", resp.Stack.Name)
	}
}

// TestGetStackConfig 验证/config以不可缓存JSON返回项目文件和版本。
func TestGetStackConfig(t *testing.T) {
	env := newTestEnv(t)
	cookies := env.login(t)
	agentID := "agent-1"
	env.seedAgent(t, agentID)
	env.seedStack(t, agentID, "web", sampleCompose)

	rec, _ := env.do(t, http.MethodGet, "/api/agents/"+agentID+"/stacks/web/config", "", cookies)
	if rec.Code != http.StatusOK {
		t.Fatalf("取配置应返回 200，实际 %d", rec.Code)
	}
	var config struct {
		Content    string `json:"content"`
		EnvContent string `json:"env_content"`
		Version    int64  `json:"version"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &config); err != nil {
		t.Fatal(err)
	}
	if config.Content != sampleCompose || config.EnvContent != "" || config.Version != 1 {
		t.Fatal("配置接口项目文件或版本不匹配")
	}
	if !strings.Contains(rec.Header().Get("Cache-Control"), "no-store") {
		t.Fatal("敏感配置响应必须禁止缓存")
	}
}

// TestStackNotFound 验证各接口对不存在的编排返回 404。
func TestStackNotFound(t *testing.T) {
	env := newTestEnv(t)
	cookies := env.login(t)
	agentID := "agent-1"
	env.seedAgent(t, agentID)
	base := "/api/agents/" + agentID + "/stacks/ghost"

	cases := []struct{ method, path string }{
		{http.MethodGet, base},
		{http.MethodGet, base + "/config"},
		{http.MethodGet, base + "/logs"},
		{http.MethodGet, base + "/ps"},
		{http.MethodDelete, base},
	}
	for _, c := range cases {
		t.Run(c.method+" "+c.path, func(t *testing.T) {
			rec, parsed := env.do(t, c.method, c.path, "", cookies)
			if c.method == http.MethodGet && c.path == base {
				// 统一详情还要查询引擎现存项目；离线不能断言不存在。
				if rec.Code != http.StatusServiceUnavailable || parsed.Error.Code != "agent_offline" {
					t.Fatalf("离线应返回503: %s", rec.Body.String())
				}
				return
			}
			if rec.Code != http.StatusNotFound {
				t.Fatalf("应返回 404，实际 %d，响应 %s", rec.Code, rec.Body.String())
			}
			if parsed.Error.Code != "not_found" {
				t.Errorf("错误码应为 not_found，实际 %q", parsed.Error.Code)
			}
		})
	}
}

// TestDeleteStackAgentOffline 验证Agent离线拒绝删除并保留管理记录。
func TestDeleteStackAgentOffline(t *testing.T) {
	env := newTestEnv(t)
	cookies := env.login(t)
	agentID := "agent-1"
	env.seedAgent(t, agentID)
	env.seedStack(t, agentID, "web", sampleCompose)

	rec, _ := env.do(t, http.MethodDelete, "/api/agents/"+agentID+"/stacks/web", "", cookies)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("离线删除必须拒绝并保留记录，实际 %d", rec.Code)
	}
	if _, err := env.db.GetComposeStack(newTestContext(), agentID, "web"); err != nil {
		t.Fatalf("删除未执行时必须保留管理记录: %v", err)
	}
}

// TestStackActionOffline 验证动作类接口在 Agent 离线时返回 503 agent_offline。
func TestStackActionOffline(t *testing.T) {
	env := newTestEnv(t)
	cookies := env.login(t)
	agentID := "agent-1"
	env.seedAgent(t, agentID)
	env.seedStack(t, agentID, "web", sampleCompose)

	rec, parsed := env.do(t, http.MethodPost, "/api/agents/"+agentID+"/stacks/web/actions",
		`{"action":"restart"}`, cookies)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("Agent 离线时动作应返回 503，实际 %d，响应 %s", rec.Code, rec.Body.String())
	}
	if parsed.Error.Code != "agent_offline" {
		t.Errorf("错误码应为 agent_offline，实际 %q", parsed.Error.Code)
	}
}

// TestStackActionUnknown 验证未知 action 返回 400。
func TestStackActionUnknown(t *testing.T) {
	env := newTestEnv(t)
	cookies := env.login(t)
	agentID := "agent-1"
	env.seedAgent(t, agentID)
	env.seedStack(t, agentID, "web", sampleCompose)

	rec, parsed := env.do(t, http.MethodPost, "/api/agents/"+agentID+"/stacks/web/actions",
		`{"action":"explode"}`, cookies)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("未知 action 应返回 400，实际 %d，响应 %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(parsed.Error.Message, "不支持的 action") {
		t.Errorf("错误文案应说明不支持的 action，实际 %q", parsed.Error.Message)
	}
}

// TestStackLogsValidation 验证日志接口的参数校验。
func TestStackLogsValidation(t *testing.T) {
	env := newTestEnv(t)
	cookies := env.login(t)
	agentID := "agent-1"
	env.seedAgent(t, agentID)
	env.seedStack(t, agentID, "web", sampleCompose)

	rec, parsed := env.do(t, http.MethodGet,
		"/api/agents/"+agentID+"/stacks/web/logs?service=../evil", "", cookies)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("非法服务名应返回 400，实际 %d", rec.Code)
	}
	if !strings.Contains(parsed.Error.Message, "服务名不合法") {
		t.Errorf("错误文案应说明服务名不合法，实际 %q", parsed.Error.Message)
	}

	rec, _ = env.do(t, http.MethodGet,
		"/api/agents/"+agentID+"/stacks/web/logs?tail=99999", "", cookies)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("超范围 tail 应返回 400，实际 %d", rec.Code)
	}
}

// ---- 资源代理接口 ----

// TestResourceProxiesRequireAgent 验证镜像/网络/卷接口在 Agent 离线时返回 503。
func TestResourceProxiesRequireAgent(t *testing.T) {
	env := newTestEnv(t)
	cookies := env.login(t)
	agentID := "agent-1"
	env.seedAgent(t, agentID)
	base := "/api/agents/" + agentID

	gets := []string{"/images", "/networks", "/volumes"}
	for _, p := range gets {
		t.Run("GET "+p, func(t *testing.T) {
			rec, parsed := env.do(t, http.MethodGet, base+p, "", cookies)
			if rec.Code != http.StatusServiceUnavailable {
				t.Fatalf("应返回 503，实际 %d，响应 %s", rec.Code, rec.Body.String())
			}
			if parsed.Error.Code != "agent_offline" {
				t.Errorf("错误码应为 agent_offline，实际 %q", parsed.Error.Code)
			}
		})
	}

	dels := []string{"/images/sha256:abc", "/networks/net-1", "/volumes/data"}
	for _, p := range dels {
		t.Run("DELETE "+p, func(t *testing.T) {
			rec, parsed := env.do(t, http.MethodDelete, base+p, "", cookies)
			if rec.Code != http.StatusServiceUnavailable {
				t.Fatalf("应返回 503，实际 %d，响应 %s", rec.Code, rec.Body.String())
			}
			if parsed.Error.Code != "agent_offline" {
				t.Errorf("错误码应为 agent_offline，实际 %q", parsed.Error.Code)
			}
		})
	}
}

// TestStackEndpointsRequire401 验证未登录访问编排接口一律 401。
func TestStackEndpointsRequire401(t *testing.T) {
	env := newTestEnv(t)
	base := "/api/agents/agent-1"
	paths := []struct{ method, path string }{
		{http.MethodGet, base + "/stacks"},
		{http.MethodPost, base + "/stacks"},
		{http.MethodGet, base + "/stacks/web"},
		{http.MethodPut, base + "/stacks/web"},
		{http.MethodDelete, base + "/stacks/web"},
		{http.MethodGet, base + "/stacks/web/config"},
		{http.MethodPost, base + "/stacks/web/actions"},
		{http.MethodGet, base + "/stacks/web/logs"},
		{http.MethodGet, base + "/stacks/web/ps"},
		{http.MethodGet, base + "/images"},
		{http.MethodDelete, base + "/images/x"},
		{http.MethodGet, base + "/networks"},
		{http.MethodDelete, base + "/networks/x"},
		{http.MethodGet, base + "/volumes"},
		{http.MethodDelete, base + "/volumes/x"},
	}
	for _, p := range paths {
		t.Run(p.method+" "+p.path, func(t *testing.T) {
			rec, parsed := env.do(t, p.method, p.path, "", nil)
			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("未登录应返回 401，实际 %d，响应 %s", rec.Code, rec.Body.String())
			}
			if parsed.Error.Code != "unauthorized" {
				t.Errorf("错误码应为 unauthorized，实际 %q", parsed.Error.Code)
			}
		})
	}
}

// TestStackEndpointsNonAdmin 验证无该 Agent 权限的普通用户被拒绝。
//
// 普通用户没有任何组授权，因此对这些接口应当拿到 403 forbidden。
func TestStackEndpointsNonAdmin(t *testing.T) {
	env := newTestEnv(t)
	adminCookies := env.login(t)
	agentID := "agent-1"
	env.seedAgent(t, agentID)

	env.createUser(t, adminCookies, "bob", "UserPass123!", string(user.RoleUser))
	bobCookies := env.loginAs(t, "bob", "UserPass123!")

	base := "/api/agents/" + agentID
	paths := []struct{ method, path string }{
		{http.MethodGet, base + "/stacks"},
		{http.MethodPost, base + "/stacks"},
		{http.MethodGet, base + "/images"},
		{http.MethodGet, base + "/networks"},
		{http.MethodGet, base + "/volumes"},
	}
	for _, p := range paths {
		t.Run(p.method+" "+p.path, func(t *testing.T) {
			rec, parsed := env.do(t, p.method, p.path, "", bobCookies)
			if rec.Code != http.StatusForbidden {
				t.Fatalf("无权限用户应返回 403，实际 %d，响应 %s", rec.Code, rec.Body.String())
			}
			if parsed.Error.Code != "forbidden" {
				t.Errorf("错误码应为 forbidden，实际 %q", parsed.Error.Code)
			}
		})
	}
}
