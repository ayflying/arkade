package httpx

import (
	"arkade/hub/internal/store"
	"arkade/protocol/envelope"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestComposeOperationSaveIsIdempotentAndStreamed(t *testing.T) {
	e := newTestEnv(t)
	var finish atomic.Bool
	a := startImportAgent(t, e, func(req envelope.Envelope, _ int) any {
		if req.Method == envelope.MethodComposeOperationStart {
			var p envelope.ComposeOperationStartPayload
			_ = envelope.DecodePayload(&req, &p)
			if p.Action != "up" || !strings.Contains(p.Config, "new:1") || p.Env != "SECRET=keptsecret" {
				t.Errorf("invalid spec action/config/env")
			}
			return envelope.ComposeOperationResult{TaskID: "operation-task-01", Name: "demo", Action: "up", Status: "running"}
		}
		if finish.Load() {
			zero := 0
			return envelope.ComposeOperationResult{TaskID: "operation-task-01", Name: "demo", Action: "up", Status: "succeeded", ExitCode: &zero}
		}
		return envelope.ComposeOperationResult{TaskID: "operation-task-01", Name: "demo", Action: "up", Status: "running"}
	})
	now := time.Now()
	if err := e.db.CreateComposeStack(context.Background(), store.ComposeStack{ID: "op-stack", AgentID: a.id, Name: "demo", Content: sampleCompose, EnvContent: "SECRET=keptsecret", ConfigHash: "old", Version: 1, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	cookies := e.loginAsFor(t, a.url, "admin", testPassword)
	payload, _ := json.Marshal(map[string]any{"request_id": "operation-request-01", "action": "up", "content": "services:\n  web:\n    image: new:1\n", "version": 1})
	base := a.url + "/api/agents/" + a.id + "/stacks/demo/operation-tasks"
	for i := 0; i < 2; i++ {
		resp, body := doHTTP(t, "POST", base, string(payload), cookies)
		if resp.StatusCode != 202 {
			t.Fatalf("%d %s", resp.StatusCode, body)
		}
		if strings.Contains(string(body), "keptsecret") {
			t.Fatal("secret response")
		}
	}
	rec, err := e.db.GetComposeStack(context.Background(), a.id, "demo")
	if err != nil || rec.Version != 2 || rec.EnvContent != "SECRET=keptsecret" {
		t.Fatalf("resaved version=%d err=%v", rec.Version, err)
	}
	for _, suffix := range []string{"/operation-task-01?after=-1", "/operation-task-01?after=wat"} {
		resp, _ := doHTTP(t, "GET", base+suffix, "", cookies)
		if resp.StatusCode != 400 {
			t.Fatalf("bad cursor: %d", resp.StatusCode)
		}
	}
	respOther, _ := doHTTP(t, "GET", strings.Replace(base, "/demo/", "/other/", 1)+"/operation-task-01", "", cookies)
	if respOther.StatusCode != 404 {
		t.Fatalf("cross project read: %d", respOther.StatusCode)
	}
	respTask, bodyTask := doHTTP(t, "GET", base+"/operation-task-01?after=0", "", cookies)
	if respTask.StatusCode != 200 || !strings.Contains(respTask.Header.Get("Cache-Control"), "no-store") || strings.Contains(string(bodyTask), "keptsecret") {
		t.Fatalf("task response safety: %d", respTask.StatusCode)
	}
	finish.Store(true)
	// 不打开任何GET页面，Hub后台仍应完成配置同步标记。
	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		current, e := e.db.GetComposeStack(context.Background(), a.id, "demo")
		if e == nil && !current.SyncedAt.IsZero() {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	synced, _ := e.db.GetComposeStack(context.Background(), a.id, "demo")
	if synced.SyncedAt.IsZero() {
		t.Fatal("closed browser left task unfinalized")
	}
	// 大于审计预读上限的配置必须完整到达handler，不能被8KB截断。
	huge, _ := json.Marshal(map[string]any{"request_id": "operation-request-huge", "action": "up", "content": "#" + strings.Repeat("x", 10000) + "\nservices:\n  web:\n    image: new:1\n", "version": 2})
	resp, body := doHTTP(t, http.MethodPost, base, string(huge), cookies)
	if resp.StatusCode != 409 { // fake returns same taskid and binding must reject; importantly JSON wasn't truncated to400.
		t.Fatalf("large body expected task-binding conflict, got %d %s", resp.StatusCode, body)
	}
}

// TestComposeOperationStaleFinalizeStaysSucceeded 验证「部署期间配置又被改过」时
// 任务仍然是 succeeded + 明确警告，而不是伪装成执行失败。
//
// 回归背景：以前这里会把任务改写成 failed + 退出码 -1，
// 日志里容器明明 Started，界面却报"执行失败"，用户只能以为保存没生效、
// 配置被回滚了。真相是执行成功，只是库里的配置已经不是它部署的那份。
func TestComposeOperationStaleFinalizeStaysSucceeded(t *testing.T) {
	e := newTestEnv(t)
	var finish atomic.Bool
	a := startImportAgent(t, e, func(req envelope.Envelope, _ int) any {
		zero := 0
		if req.Method == envelope.MethodComposeOperationStart {
			return envelope.ComposeOperationResult{TaskID: "stale-task-01", Name: "demo", Action: "up", Status: "running"}
		}
		if finish.Load() {
			return envelope.ComposeOperationResult{TaskID: "stale-task-01", Name: "demo", Action: "up", Status: "succeeded", ExitCode: &zero}
		}
		return envelope.ComposeOperationResult{TaskID: "stale-task-01", Name: "demo", Action: "up", Status: "running"}
	})
	now := time.Now()
	if err := e.db.CreateComposeStack(context.Background(), store.ComposeStack{ID: "stale-stack", AgentID: a.id, Name: "demo", Content: sampleCompose, EnvContent: "SECRET=keptsecret", ConfigHash: "old", Version: 1, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	cookies := e.loginAsFor(t, a.url, "admin", testPassword)
	body, _ := json.Marshal(map[string]any{"request_id": "stale-request-01", "action": "up", "content": "services:\n  web:\n    image: new:1\n", "env_content": "SECRET=fresh", "version": 1})
	base := a.url + "/api/agents/" + a.id + "/stacks/demo/operation-tasks"
	if resp, started := doHTTP(t, http.MethodPost, base, string(body), cookies); resp.StatusCode != 202 {
		t.Fatalf("start: %d %s", resp.StatusCode, started)
	}
	// 部署期间另一个会话把配置改成完全不同的内容：旧任务不能再把它标记为已同步。
	if _, ok, err := e.db.UpdateComposeProject(context.Background(), a.id, "demo", "services:\n  web:\n    image: other:2\n", "SECRET=other", "other-hash", 2, now); err != nil || !ok {
		t.Fatalf("concurrent save: %v %v", ok, err)
	}
	finish.Store(true)
	deadline := time.Now().Add(4 * time.Second)
	var raw string
	for time.Now().Before(deadline) {
		resp, taskBody := doHTTP(t, http.MethodGet, base+"/stale-task-01?after=0", "", cookies)
		raw = string(taskBody)
		if resp.StatusCode == 200 && strings.Contains(raw, "sync_warning") {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !strings.Contains(raw, `"status":"succeeded"`) {
		t.Fatalf("部署成功就必须如实报告成功：%s", raw)
	}
	if strings.Contains(raw, `"status":"failed"`) || strings.Contains(raw, `"exit_code":-1`) {
		t.Fatalf("不能把成功执行改写成失败：%s", raw)
	}
	if !strings.Contains(raw, "sync_warning") || !strings.Contains(raw, "没有把它标记为") {
		t.Fatalf("缺少未同步警告：%s", raw)
	}
	saved, err := e.db.GetComposeStack(context.Background(), a.id, "demo")
	if err != nil || !saved.SyncedAt.IsZero() || saved.Content != "services:\n  web:\n    image: other:2\n" {
		t.Fatalf("别人的新配置被覆盖或误标记：%+v %v", saved, err)
	}
}

func TestComposeOperationRequiresManageForSaveAndRemove(t *testing.T) {
	e := newTestEnv(t)
	admin := e.login(t)
	e.seedAgent(t, "op-offline")
	uid := e.createUser(t, admin, "opuser", "ViewerPass123", "user")
	g := e.createGroup(t, admin, "op-users")
	for _, v := range []struct{ path, body string }{{"/api/groups/" + g.ID + "/members", `{"user_ids":["` + uid + `"]}`}, {"/api/groups/" + g.ID + "/grants", `{"grants":[{"agent_id":"op-offline","permission":"operate"}]}`}} {
		r, _ := e.do(t, "PUT", v.path, v.body, admin)
		if r.Code != 200 {
			t.Fatal(r.Body.String())
		}
	}
	cookies := e.loginAs(t, "opuser", "ViewerPass123")
	for _, body := range []string{`{"request_id":"operation-key","action":"remove"}`, `{"request_id":"operation-key","action":"up","content":"services: {}","version":1}`} {
		r, _ := e.do(t, "POST", "/api/agents/op-offline/stacks/demo/operation-tasks", body, cookies)
		if r.Code != 403 {
			t.Fatalf("permission %d %s", r.Code, r.Body.String())
		}
	}
}
