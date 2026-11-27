package httpx

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"arkade/hub/internal/audit"
	"arkade/hub/internal/store"
	"arkade/hub/internal/user"
	"arkade/protocol/envelope"
)

type mcpLogsExecutor func(context.Context, string, []byte) ([]byte, *envelope.Error)

func (fn mcpLogsExecutor) Call(ctx context.Context, method string, payload []byte) ([]byte, *envelope.Error) {
	return fn(ctx, method, payload)
}

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
	var logCalls, execCalls int
	if err := e.registry.RegisterLocal("mcp-allowed", mcpLogsExecutor(func(_ context.Context, method string, payload []byte) ([]byte, *envelope.Error) {
		if method == envelope.MethodContainerExec {
			execCalls++
			var p envelope.ContainerExecPayload
			if err := json.Unmarshal(payload, &p); err != nil || p.ContainerID != "example" || len(p.Command) != 2 || p.Command[0] != "echo" || p.Command[1] != "exec audit fixture" || p.User != "1000" || p.WorkDir != "/tmp" || p.TimeoutSec != 7 {
				t.Errorf("Agent 收到了错误的 exec 参数：%+v %v", p, err)
				return nil, envelope.NewError(envelope.CodeBadRequest, "unexpected exec")
			}
			return []byte(`{"exit_code":3,"stdout":"demo-out","stderr":"demo-err","truncated":false,"timed_out":false,"duration_ms":12}`), nil
		}
		logCalls++
		var p envelope.ContainerLogsPayload
		if err := json.Unmarshal(payload, &p); err != nil || method != envelope.MethodContainerLogs || p.ContainerID != "example" || p.TailLines != 17 {
			t.Errorf("unexpected Agent call: %s %+v %v", method, p, err)
			return nil, envelope.NewError(envelope.CodeBadRequest, "unexpected call")
		}
		return []byte(`{"lines":["service startup failed"],"truncated":false}`), nil
	})); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { e.registry.RemoveLocal("mcp-allowed") })
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
	checkDenied("arkade_get_container_logs", map[string]any{"agentID": "mcp-hidden", "containerID": "example"})
	logs := map[string]any{"agentID": "mcp-allowed", "containerID": "example", "tail": 17}
	if failed, text := call("arkade_get_container_logs", logs); failed || text != `{"lines":["service startup failed"],"truncated":false}` {
		t.Fatalf("查看权限应能读取容器日志：%t %s", failed, text)
	}
	checkDenied("arkade_delete_container", map[string]any{"agentID": "mcp-allowed", "containerID": "example"})
	execArgs := map[string]any{"agentID": "mcp-allowed", "containerID": "example", "command": []string{"echo", "exec audit fixture"}, "user": "1000", "workdir": "/tmp", "timeout_sec": 7}
	checkDenied("arkade_exec_container", execArgs)
	grant("mcp-allowed", user.PermOperate)
	checkDenied("arkade_exec_container", execArgs)
	grant("mcp-allowed", user.PermManage)
	if failed, text := call("arkade_exec_container", execArgs); failed || text != `{"result":{"duration_ms":12,"exit_code":3,"stderr":"demo-err","stdout":"demo-out","timed_out":false,"truncated":false}}` {
		t.Fatalf("管理权限应能执行命令并取得进程退出码：%t %s", failed, text)
	}
	rows, _, err := e.db.ListAuditLogs(ctx, store.AuditFilters{AgentID: "mcp-allowed", Action: audit.ActionContainerExec, Result: audit.ResultSuccess})
	if err != nil || len(rows) != 1 {
		t.Fatalf("未取得唯一一条已执行的命令审计：%d 条，%v", len(rows), err)
	}
	row := rows[0]
	if row.UserID != uid || row.Target != "example" || !strings.Contains(row.Detail, created.Key.ID) || !strings.Contains(row.Detail, "arkade_exec_container") || strings.Contains(row.Detail, "exec audit fixture") || strings.Contains(row.Detail, "demo-out") || strings.Contains(row.Detail, "demo-err") {
		t.Fatal("执行审计应记录身份和操作目标，不应记录命令正文或输出")
	}
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
	checkDenied("arkade_get_container_logs", logs)
	checkDenied("arkade_exec_container", execArgs)
	if execCalls != 1 {
		t.Fatalf("只有获管理授权的执行请求应到达 Agent，实际调用 %d 次", execCalls)
	}
	if logCalls != 1 {
		t.Fatalf("只有获授权的日志读取应到达 Agent，实际调用 %d 次", logCalls)
	}
	if r := mcpTestRequest(e.router, http.MethodDelete, keys+"/"+created.Key.ID, "", "", viewer...); r.Code != 204 {
		t.Fatalf("本人撤销失败：%d", r.Code)
	}
}
