package httpx

import (
	"arkade/hub/internal/store"
	"arkade/protocol/envelope"
	"context"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestComposePullTaskStartAndCursor(t *testing.T) {
	e := newTestEnv(t)
	a := startImportAgent(t, e, func(req envelope.Envelope, _ int) any {
		out := envelope.ComposePullResult{TaskID: "pull-task-0001", Name: "demo", Command: "docker compose --project-name demo pull", Status: "running", Output: []envelope.ComposePullOutput{}}
		switch req.Method {
		case envelope.MethodComposePullStart:
			var p envelope.ComposePullStartPayload
			_ = envelope.DecodePayload(&req, &p)
			if p.Name != "demo" || p.RequestID != "request-0001" {
				t.Errorf("invalid start %#v", p)
			}
		case envelope.MethodComposePullGet:
			var p envelope.ComposePullGetPayload
			_ = envelope.DecodePayload(&req, &p)
			if p.TaskID != "pull-task-0001" || p.After != 3 {
				t.Errorf("invalid cursor %#v", p)
			}
			out.Output = []envelope.ComposePullOutput{{Seq: 4, Stream: "stderr", Text: "web Pulling"}}
			out.NextCursor = 4
		default:
			t.Errorf("unexpected RPC %s", req.Method)
		}
		return out
	})
	now := time.Now()
	if err := e.db.CreateComposeStack(context.Background(), store.ComposeStack{ID: "pull-stack", AgentID: a.id, Name: "demo", Content: sampleCompose, ConfigHash: "test", Version: 1, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	cookies := e.loginAsFor(t, a.url, "admin", testPassword)
	base := a.url + "/api/agents/" + a.id + "/stacks/demo/pull-tasks"
	resp, body := doHTTP(t, http.MethodPost, base, `{"request_id":"request-0001"}`, cookies)
	if resp.StatusCode != 202 || !strings.Contains(string(body), `"status":"running"`) {
		t.Fatalf("%d %s", resp.StatusCode, body)
	}
	if !strings.Contains(resp.Header.Get("Cache-Control"), "no-store") {
		t.Fatal("task output must not cache")
	}
	resp, body = doHTTP(t, http.MethodGet, base+"/pull-task-0001?after=3", "", cookies)
	if resp.StatusCode != 200 || !strings.Contains(string(body), `"seq":4`) {
		t.Fatalf("%d %s", resp.StatusCode, body)
	}
	resp, body = doHTTP(t, http.MethodGet, strings.Replace(base, "/demo/", "/another/", 1)+"/pull-task-0001?after=3", "", cookies)
	if resp.StatusCode != 404 {
		t.Fatalf("crossproject %d %s", resp.StatusCode, body)
	}
	a.assertMethods(t, envelope.MethodComposePullStart, envelope.MethodComposePullGet, envelope.MethodComposePullGet)
}

func TestComposePullRequiresOperateAndValidInput(t *testing.T) {
	e := newTestEnv(t)
	admin := e.login(t)
	e.seedAgent(t, "pull-offline")
	uid := e.createUser(t, admin, "pullviewer", "ViewerPass123", "user")
	g := e.createGroup(t, admin, "pull-readers")
	for _, v := range []struct{ path, body string }{{"/api/groups/" + g.ID + "/members", `{"user_ids":["` + uid + `"]}`}, {"/api/groups/" + g.ID + "/grants", `{"grants":[{"agent_id":"pull-offline","permission":"view"}]}`}} {
		r, _ := e.do(t, http.MethodPut, v.path, v.body, admin)
		if r.Code != 200 {
			t.Fatal(r.Body.String())
		}
	}
	viewer := e.loginAs(t, "pullviewer", "ViewerPass123")
	base := "/api/agents/pull-offline/stacks/demo/pull-tasks"
	for _, v := range []struct{ method, path, body string }{{"POST", base, `{"request_id":"request-0001"}`}, {"GET", base + "/pull-task-0001", ""}} {
		r, _ := e.do(t, v.method, v.path, v.body, viewer)
		if r.Code != 403 {
			t.Fatalf("permission %d %s", r.Code, r.Body.String())
		}
	}
	for _, v := range []struct{ method, path, body string }{{"POST", base, `{"request_id":"bad"}`}, {"GET", base + "/pull-task-0001?after=-1", ""}, {"GET", base + "/pull-task-0001?after=no", ""}} {
		r, _ := e.do(t, v.method, v.path, v.body, admin)
		if r.Code != 400 {
			t.Fatalf("validation %d %s", r.Code, r.Body.String())
		}
	}
}
