package httpx

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"arkade/hub/internal/user"
)

func TestMCPPersonalKeyFollowsLiveGrants(t *testing.T) {
	e := newTestEnv(t)
	admin := e.login(t)
	uid := e.createUser(t, admin, "mcp-reader", testPassword, "user")
	viewer := e.loginAs(t, "mcp-reader", testPassword)
	group := e.createGroup(t, admin, "mcp-grants")
	ctx := t.Context()
	if err := e.db.SetGroupMembers(ctx, group.ID, []string{uid}); err != nil {
		t.Fatal(err)
	}
	seedTargetAgent(t, e, "mcp-allowed", "已授权服务器")
	seedTargetAgent(t, e, "mcp-hidden", "未授权服务器")
	grant := func(agent string, p user.Permission) {
		t.Helper()
		if err := e.db.SetGroupGrant(ctx, group.ID, agent, p); err != nil {
			t.Fatal(err)
		}
	}
	grant("mcp-allowed", user.PermView)
	const keys = "/api/mcp/keys"
	r := mcpTestRequest(e.router, http.MethodPost, keys, "", `{"name":"个人客户端"}`, viewer...)
	var created struct {
		Key   struct{ ID string }
		Token string
	}
	if r.Code != 200 || json.Unmarshal(r.Body.Bytes(), &created) != nil || created.Token == "" || created.Key.ID == "" {
		t.Fatalf("普通用户创建个人密钥失败：%d", r.Code)
	}
	if r := mcpTestRequest(e.router, http.MethodDelete, keys+"/"+created.Key.ID, "", "", admin...); r.Code != 404 {
		t.Fatalf("不能通过个人密钥接口撤销他人的密钥：%d", r.Code)
	}
	call := func(tool string, args map[string]any) (bool, string) {
		t.Helper()
		payload, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": tool, "arguments": args}})
		if err != nil {
			t.Fatal(err)
		}
		r := mcpTestRequest(e.router, http.MethodPost, "/mcp", created.Token, string(payload))
		var out struct {
			Result *struct {
				IsError bool
				Content []struct{ Text string }
			}
		}
		if r.Code != 200 || json.Unmarshal(r.Body.Bytes(), &out) != nil || out.Result == nil || len(out.Result.Content) != 1 {
			t.Fatalf("无有效工具结果：%d %s", r.Code, r.Body)
		}
		return out.Result.IsError, out.Result.Content[0].Text
	}
	checkDenied := func(tool string, args map[string]any) {
		t.Helper()
		failed, text := call(tool, args)
		if !failed || !strings.Contains(text, `"forbidden"`) {
			t.Fatalf("%s 未被权限检查拒绝：%t %s", tool, failed, text)
		}
	}
	failed, text := call("arkade_list_agents", map[string]any{})
	if failed || !strings.Contains(text, "mcp-allowed") || strings.Contains(text, "mcp-hidden") {
		t.Fatalf("服务器列表未正确按授权过滤：%t %s", failed, text)
	}
	checkDenied("arkade_list_containers", map[string]any{"agentID": "mcp-hidden"})
	checkDenied("arkade_delete_container", map[string]any{"agentID": "mcp-allowed", "containerID": "example"})
	grant("mcp-allowed", user.PermManage)
	migrate := map[string]any{"agentID": "mcp-allowed", "name": "demo", "target_agent_id": "mcp-hidden", "confirmation": "demo", "request_id": "mcp-personal-migrate"}
	checkDenied("arkade_migrate_stack", migrate)
	grant("mcp-hidden", user.PermManage)
	if failed, text := call("arkade_migrate_stack", migrate); failed || !strings.Contains(text, "task_id") {
		t.Fatalf("同一密钥未继承实时授权：%t %s", failed, text)
	}
	if err := e.db.SetGroupMembers(ctx, group.ID, nil); err != nil {
		t.Fatal(err)
	}
	checkDenied("arkade_migrate_stack", migrate)
	checkDenied("arkade_list_containers", map[string]any{"agentID": "mcp-allowed"})
	if r := mcpTestRequest(e.router, http.MethodDelete, keys+"/"+created.Key.ID, "", "", viewer...); r.Code != 204 {
		t.Fatalf("本人撤销失败：%d", r.Code)
	}
}
