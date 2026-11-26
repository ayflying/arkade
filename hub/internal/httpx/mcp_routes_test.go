package httpx

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"arkade/hub/internal/store"
)

func mcpTestRequest(router http.Handler, method, path, token, body string, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	for _, cookie := range cookies {
		req.AddCookie(cookie)
	}
	out := httptest.NewRecorder()
	router.ServeHTTP(out, req)
	return out
}

func TestMCPAdminKeyAndTools(t *testing.T) {
	e := newTestEnv(t)
	cookies := e.login(t)
	const keys = "/api/mcp/keys"
	const init = `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"test","version":"1"}}}`
	const list = `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`
	const call = `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"arkade_list_agents","arguments":{}}}`
	if r := mcpTestRequest(e.router, "POST", "/mcp", "", init, cookies...); r.Code != 401 {
		t.Fatalf("会话 Cookie 不能用于 MCP：%d %s", r.Code, r.Body)
	}
	r := mcpTestRequest(e.router, "POST", keys, "", `{"name":"测试客户端"}`, cookies...)
	var created struct {
		Key   struct{ ID string }
		Token string
	}
	if r.Code != 200 || r.Header().Get("Cache-Control") != "no-store" || json.Unmarshal(r.Body.Bytes(), &created) != nil || created.Key.ID == "" || created.Token == "" {
		t.Fatalf("创建密钥失败：%d", r.Code)
	}
	if r := mcpTestRequest(e.router, "GET", keys, "", "", cookies...); r.Code != 200 || bytes.Contains(r.Body.Bytes(), []byte(created.Token)) {
		t.Fatalf("列表不应返回密钥明文：%d", r.Code)
	}
	for _, tc := range []struct{ payload, want string }{{init, "serverInfo"}, {list, "arkade_migrate_stack"}, {call, `"content"`}} {
		if r := mcpTestRequest(e.router, "POST", "/mcp", created.Token, tc.payload); r.Code != 200 || !strings.Contains(r.Body.String(), tc.want) || strings.Contains(r.Body.String(), `"isError":true`) {
			t.Fatalf("MCP 调用失败：%d %s", r.Code, r.Body)
		}
	}
	const refusedWrite = `{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"arkade_delete_container","arguments":{"agentID":"missing-agent","containerID":"example","force":true}}}`
	if r := mcpTestRequest(e.router, "POST", "/mcp", created.Token, refusedWrite); r.Code != 200 || !strings.Contains(r.Body.String(), `"isError":true`) {
		t.Fatalf("应返回业务失败：%d %s", r.Code, r.Body)
	}
	rows, _, err := e.db.ListAuditLogs(t.Context(), store.AuditFilters{AgentID: "missing-agent"})
	if err != nil || len(rows) != 1 || !strings.Contains(rows[0].Detail, created.Key.ID) || rows[0].UserID != "u-test" {
		t.Fatalf("缺少关联 MCP 密钥的操作审计：%+v %v", rows, err)
	}
	seedTargetAgent(t, e, "mcp-source", "测试源端")
	seedTargetAgent(t, e, "mcp-target", "测试目标端")
	now := time.Now()
	const marker = "privacy-fixture-not-a-real-secret"
	if err := e.db.CreateComposeStack(t.Context(), store.ComposeStack{
		ID: "mcp-privacy-stack", AgentID: "mcp-source", Name: "private-demo", Version: 7,
		Content: "services: {} # ${TAG}", EnvContent: "PASSWORD=" + marker + "\nLOG_LEVEL=info\n",
		ConfigHash: "0123456789abcdef", CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	const configCall = `{"jsonrpc":"2.0","id":6,"method":"tools/call","params":{"name":"arkade_get_stack_config","arguments":{"agentID":"mcp-source","name":"private-demo"}}}`
	if r := mcpTestRequest(e.router, "POST", "/mcp", created.Token, configCall); r.Code != 200 || strings.Contains(r.Body.String(), marker) || strings.Contains(r.Body.String(), `"isError":true`) || !strings.Contains(r.Body.String(), `\"version\":7`) || !strings.Contains(r.Body.String(), "services: {} # ${TAG}") {
		t.Fatalf("MCP 未原样返回 YAML 或泄露了环境文件：HTTP %d", r.Code)
	}
	const patch = `{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"arkade_patch_stack_env","arguments":{"agentID":"mcp-source","name":"private-demo","version":7,"updates":{"LOG_LEVEL":"debug-fixture","TZ":"Asia/Shanghai"}}}}`
	if r := mcpTestRequest(e.router, "POST", "/mcp", created.Token, patch); r.Code != 200 || strings.Contains(r.Body.String(), `"isError":true`) || !strings.Contains(r.Body.String(), `\"version\":8`) || !strings.Contains(r.Body.String(), `changed_keys`) || strings.Contains(r.Body.String(), "debug-fixture") || strings.Contains(r.Body.String(), marker) {
		t.Fatalf("MCP 未按键保存环境变量或回显了值：HTTP %d", r.Code)
	}
	if r := mcpTestRequest(e.router, "POST", "/mcp", created.Token, patch); !strings.Contains(r.Body.String(), `\"code\":\"conflict\"`) {
		t.Fatalf("旧版本更新应被拒绝：HTTP %d", r.Code)
	}
	rec, err := e.db.GetComposeStack(t.Context(), "mcp-source", "private-demo")
	if err != nil || rec.Version != 8 || rec.Content != "services: {} # ${TAG}" || !strings.HasPrefix(rec.EnvContent, "PASSWORD="+marker+"\n") || !strings.Contains(rec.EnvContent, "debug-fixture") || !strings.Contains(rec.EnvContent, "Asia/Shanghai") || !rec.SyncedAt.IsZero() {
		t.Fatal("仅保存环境变量失败或变更了其他配置")
	}
	const migrate = `{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"arkade_migrate_stack","arguments":{"agentID":"mcp-source","name":"demo","target_agent_id":"mcp-target","confirmation":"demo","request_id":"mcp-migrate-test"}}}`
	if r := mcpTestRequest(e.router, "POST", "/mcp", created.Token, migrate); r.Code != 200 || strings.Contains(r.Body.String(), `"isError":true`) || !strings.Contains(r.Body.String(), `task_id`) {
		t.Fatalf("有效密钥应能直接提交迁移任务：%d %s", r.Code, r.Body)
	}
	if r := mcpTestRequest(e.router, "GET", "/api/agents", created.Token, ""); r.Code != 401 {
		t.Fatalf("密钥不应授权普通 API：%d", r.Code)
	}
	if r := mcpTestRequest(e.router, "GET", keys, created.Token, "", cookies...); r.Code != 403 {
		t.Fatalf("请求带 Bearer 不可管理密钥：%d %s", r.Code, r.Body)
	}
	if r := mcpTestRequest(e.router, "DELETE", keys+"/"+created.Key.ID, "", "", cookies...); r.Code != 204 {
		t.Fatalf("撤销失败：%d %s", r.Code, r.Body)
	}
	if r := mcpTestRequest(e.router, "POST", "/mcp", created.Token, list); r.Code != 401 {
		t.Fatalf("已撤销密钥不能使用：%d", r.Code)
	}
}

// TestMCPOrigin 记录"MCP 端点不再自行判断来源"这一取舍。
//
// 原因同 csrf_test.go：Hub 在反向代理后面看到的协议是代理那一跳的明文 http，
// 浏览器发来的是 https，服务端永远判不出同源——放在 HTTPS 域名下的部署会连 MCP 都连不上。
// MCP 的安全边界不依赖这里：它只认 Authorization Bearer，不认 Cookie。
func TestMCPOrigin(t *testing.T) {
	req := httptest.NewRequest("POST", "https://arkade.example/mcp", nil)
	for _, tc := range []struct {
		value string
		want  bool
	}{{"", true}, {"https://arkade.example", true}, {"http://arkade.example", true}, {"https://other.example", true}, {"null", true}, {"https://arkade.example/a", true}} {
		req.Header.Del("Origin")
		if tc.value != "" {
			req.Header.Set("Origin", tc.value)
		}
		if got := mcpOriginAllowed(req); got != tc.want {
			t.Errorf("来源 %q：%t，预期 %t", tc.value, got, tc.want)
		}
	}
}
