package httpx

// resources_container_exec_test.go：容器内执行命令的 HTTP 契约测试。
//
// 覆盖四件事：
//  1. 成功路径把请求体与路径正确拼成 RPC 载荷（命令向量不能被拆开）；
//  2. 参数非法是 400 且**不发出** RPC（不然一次无效输入也占控制通道槽位）；
//  3. 普通用户（无授权）是 403 —— 执行命令要求 manage；
//  4. 审计只记"谁对哪个容器执行过命令"，**不记命令正文**。

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"arkade/hub/internal/audit"
	"arkade/protocol/envelope"
)

// TestContainerExecHTTPSuccess 验证成功路径与载荷传递。
func TestContainerExecHTTPSuccess(t *testing.T) {
	e := newTestEnv(t)
	var got envelope.ContainerExecPayload
	a := startImportAgent(t, e, func(req envelope.Envelope, _ int) any {
		if req.Method != envelope.MethodContainerExec {
			return envelope.NewError(envelope.CodeUnsupported, "unexpected RPC")
		}
		if err := envelope.DecodePayload(&req, &got); err != nil {
			t.Error(err)
		}
		return envelope.ContainerExecResult{
			ExitCode: 7, Stdout: "hi\n", Stderr: "warn\n", Truncated: true, DurationMs: 42,
		}
	})
	cookies := e.loginAsFor(t, a.url, "admin", testPassword)

	resp, body := doHTTP(t, http.MethodPost, a.url+"/api/agents/"+a.id+"/containers/web/exec",
		`{"command":["/bin/sh","-c","echo hi; echo warn >&2"],"user":"1000","workdir":"/srv","timeout_sec":5}`,
		cookies)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("应成功，实际 %d %s", resp.StatusCode, body)
	}
	var out struct {
		Result envelope.ContainerExecResult `json:"result"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatal(err)
	}
	if out.Result.ExitCode != 7 || out.Result.Stdout != "hi\n" || out.Result.Stderr != "warn\n" {
		t.Fatalf("结果未原样返回: %+v", out.Result)
	}
	if !out.Result.Truncated || out.Result.DurationMs != 42 {
		t.Fatalf("截断/耗时字段丢失: %+v", out.Result)
	}
	// 容器身份只认路径；命令向量必须原样传递（不能被拼接成一个字符串）。
	if got.ContainerID != "web" {
		t.Fatalf("容器 ID 应取自路径，实际 %q", got.ContainerID)
	}
	if len(got.Command) != 3 || got.Command[2] != "echo hi; echo warn >&2" {
		t.Fatalf("命令向量被改动: %+v", got.Command)
	}
	if got.User != "1000" || got.WorkDir != "/srv" || got.TimeoutSec != 5 {
		t.Fatalf("执行参数未传递: %+v", got)
	}
}

// TestContainerExecHTTPRejectsBadInput 验证非法参数在 Hub 侧就被挡下。
func TestContainerExecHTTPRejectsBadInput(t *testing.T) {
	e := newTestEnv(t)
	a := startImportAgent(t, e, func(req envelope.Envelope, _ int) any {
		return envelope.NewError(envelope.CodeUnsupported, "不应到达 Agent")
	})
	cookies := e.loginAsFor(t, a.url, "admin", testPassword)
	base := a.url + "/api/agents/" + a.id + "/containers/web/exec"
	for _, tc := range []struct {
		name, body string
	}{
		{"空命令", `{"command":[]}`},
		{"缺命令字段", `{}`},
		{"未知字段", `{"command":["sh"],"shell":true}`},
		{"相对工作目录", `{"command":["sh"],"workdir":"srv"}`},
		{"用户含控制字符", `{"command":["sh"],"user":"roo\u0000t"}`},
		{"不是 JSON", `not JSON`},
		{"多个对象", `{"command":["sh"]} {}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resp, body := doHTTP(t, http.MethodPost, base, tc.body, cookies)
			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("期望 400，实际 %d %s", resp.StatusCode, body)
			}
		})
	}
	if n := len(a.requests()); n != 0 {
		t.Fatalf("非法请求不应发出任何 RPC，实际 %d 条", n)
	}
}

// TestContainerExecHTTPRequiresManage 验证普通用户（无授权）被拒绝。
//
// 这是安全边界：执行命令能改容器内数据，权限必须高于"启停"（operate）。
func TestContainerExecHTTPRequiresManage(t *testing.T) {
	e := newTestEnv(t)
	a := startImportAgent(t, e, func(req envelope.Envelope, _ int) any {
		return envelope.NewError(envelope.CodeUnsupported, "不应到达 Agent")
	})
	admin := e.loginAsFor(t, a.url, "admin", testPassword)
	e.createUser(t, admin, "plainuser", "PlainPass123", "user")
	userCookies := e.loginAsFor(t, a.url, "plainuser", "PlainPass123")

	resp, body := doHTTP(t, http.MethodPost, a.url+"/api/agents/"+a.id+"/containers/web/exec",
		`{"command":["sh"]}`, userCookies)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("普通用户必须 403，实际 %d %s", resp.StatusCode, body)
	}
	if n := len(a.requests()); n != 0 {
		t.Fatalf("被拒请求不应发出 RPC，实际 %d 条", n)
	}
	// 未登录同理。
	resp, _ = doHTTP(t, http.MethodPost, a.url+"/api/agents/"+a.id+"/containers/web/exec",
		`{"command":["sh"]}`, nil)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("未登录必须 401，实际 %d", resp.StatusCode)
	}
}

// TestContainerExecHTTPOfflineAgent 验证 Agent 离线时是 503 而不是 500。
func TestContainerExecHTTPOfflineAgent(t *testing.T) {
	e := newTestEnv(t)
	cookies := e.login(t)
	e.seedAgent(t, "exec-offline")
	rec, _ := e.do(t, http.MethodPost, "/api/agents/exec-offline/containers/web/exec",
		`{"command":["sh"]}`, cookies)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("期望 503，实际 %d %s", rec.Code, rec.Body.String())
	}
}

// TestAuditContainerExecRoute 验证执行命令映射到独立审计动作，且 target 是容器 ID。
func TestAuditContainerExecRoute(t *testing.T) {
	r := resolveAuditRoute("/api/agents/{agentID}/containers/{containerID}/exec", http.MethodPost, "")
	if r.Action != audit.ActionContainerExec {
		t.Fatalf("动作应为 %q，实际 %q（未登记会被记成 unknown.POST）", audit.ActionContainerExec, r.Action)
	}
	if r.TargetParam != "containerID" {
		t.Fatalf("target 参数应为 containerID，实际 %q", r.TargetParam)
	}
	// 命令正文绝不能出现在动作标识或子动作里。
	if strings.Contains(strings.ToLower(r.Action), "sh") || r.SubAction {
		t.Fatalf("审计不应把命令写进动作: %+v", r)
	}
}
