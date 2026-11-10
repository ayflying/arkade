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
