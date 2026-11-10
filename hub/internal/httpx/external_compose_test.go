package httpx

import (
	"arkade/protocol/envelope"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func externalTestAgent(t *testing.T, e *testEnv) *importAgent {
	return startImportAgent(t, e, func(req envelope.Envelope, _ int) any {
		switch req.Method {
		case envelope.MethodComposeList:
			return envelope.ComposeListResult{External: []envelope.ComposeProjectSummary{{Name: "external-test", Status: "running", Running: 1, Total: 1, Services: []string{"app"}}}}
		case envelope.MethodContainerList:
			return envelope.ContainerListResult{Containers: []envelope.ContainerSummary{{ID: "mine", StackID: "external-test"}, {ID: "other", StackID: "another"}}}
		case envelope.MethodComposeSource:
			var p envelope.ComposeSourcePayload
			_ = envelope.DecodePayload(&req, &p)
			if !p.Raw {
				t.Error("external config must use raw source")
			}
			content := "services:\n  app:\n    build: .\n"
			return envelope.ComposeSourceResult{Name: "external-test", Content: content, ConfigHash: composeProjectHash(content, ""), Blockers: []string{"需要构建上下文"}}
		}
		return nil
	})
}
func TestExternalDetailsAndRawConfigDoNotAdopt(t *testing.T) {
	e := newTestEnv(t)
	a := externalTestAgent(t, e)
	cookies := e.loginAsFor(t, a.url, "admin", testPassword)
	base := a.url + "/api/agents/" + a.id + "/external-stacks/external-test"
	resp, body := doHTTP(t, http.MethodGet, base, "", cookies)
	if resp.StatusCode != 200 {
		t.Fatalf("detail: %s", body)
	}
	var detail struct {
		Containers []envelope.ContainerSummary `json:"containers"`
	}
	_ = json.Unmarshal(body, &detail)
	if len(detail.Containers) != 1 || detail.Containers[0].ID != "mine" {
		t.Fatalf("wrong containers %s", body)
	}
	resp, body = doHTTP(t, http.MethodGet, base+"/config", "", cookies)
	if resp.StatusCode != 200 || !strings.Contains(string(body), `"readable":true`) || !strings.Contains(string(body), "build") {
		t.Fatalf("raw blocked: %s", body)
	}
	assertImportMissing(t, e, a.id, "external-test")
	a.assertMethods(t, envelope.MethodComposeList, envelope.MethodContainerList, envelope.MethodComposeList, envelope.MethodComposeSource)
}
func TestExternalDraftCASAndCopyNoLifecycle(t *testing.T) {
	e := newTestEnv(t)
	a := externalTestAgent(t, e)
	cookies := e.loginAsFor(t, a.url, "admin", testPassword)
	base := a.url + "/api/agents/" + a.id + "/external-stacks/external-test"
	draft := `{"content":"services: {}","source_hash":"baseline","expected_version":0}`
	resp, body := doHTTP(t, http.MethodPut, base+"/draft", draft, cookies)
	if resp.StatusCode != 200 {
		t.Fatalf("save %s", body)
	}
	resp, _ = doHTTP(t, http.MethodPut, base+"/draft", draft, cookies)
	if resp.StatusCode != 409 {
		t.Fatalf("CAS status %d", resp.StatusCode)
	}
	got, err := e.db.GetExternalComposeDraft(t.Context(), a.id, "external-test")
	if err != nil || got.Version != 1 || got.SourceHash != "baseline" {
		t.Fatal("draft mismatch")
	}
	assertImportMissing(t, e, a.id, "external-test")
	// 使用同一在线引擎的不同项目名，验证只复制记录不下发。
	copy := `{"target_agent_id":"` + a.id + `","target_name":"copied","content":"services: {}","confirmation":true}`
	resp, body = doHTTP(t, http.MethodPost, base+"/copy", copy, cookies)
	if resp.StatusCode != 200 {
		t.Fatalf("copy %s", body)
	}
	if !strings.Contains(string(body), `"started":false`) || !strings.Contains(string(body), `"data_included":false`) {
		t.Fatalf("unsafe scope %s", body)
	}
	rec, err := e.db.GetComposeStack(t.Context(), a.id, "copied")
	if err != nil || !rec.SyncedAt.IsZero() {
		t.Fatal("copy deployed target")
	}
	resp, _ = doHTTP(t, http.MethodPost, base+"/copy", copy, cookies)
	if resp.StatusCode != 409 {
		t.Fatal("overwrote target")
	}
	// 即使Hub没有该目标记录，也不能覆盖引擎已有的同名外部项目。
	collision := strings.Replace(copy, `"target_name":"copied"`, `"target_name":"external-test"`, 1)
	resp, _ = doHTTP(t, http.MethodPost, base+"/copy", collision, cookies)
	if resp.StatusCode != 409 {
		t.Fatal("ignored target engine collision")
	}
	assertImportMissing(t, e, a.id, "external-test")
	for _, req := range a.requests() {
		if req.Method != envelope.MethodComposeList {
			t.Fatalf("unexpected lifecycle RPC %s", req.Method)
		}
	}
}
func TestExternalConfigRequiresManageBeforeRPC(t *testing.T) {
	e := newTestEnv(t)
	admin := e.login(t)
	e.seedAgent(t, "ext-offline")
	uid := e.createUser(t, admin, "externalviewer", "ViewerPass123", "user")
	g := e.createGroup(t, admin, "external-readers")
	for _, r := range []struct{ path, body string }{{"/api/groups/" + g.ID + "/members", `{"user_ids":["` + uid + `"]}`}, {"/api/groups/" + g.ID + "/grants", `{"grants":[{"agent_id":"ext-offline","permission":"view"}]}`}} {
		rec, _ := e.do(t, http.MethodPut, r.path, r.body, admin)
		if rec.Code != 200 {
			t.Fatal(rec.Body.String())
		}
	}
	viewer := e.loginAs(t, "externalviewer", "ViewerPass123")
	for _, op := range []struct{ method, path string }{{"GET", "config"}, {"PUT", "draft"}, {"POST", "copy"}, {"POST", "apply"}, {"POST", "apply-preview"}} {
		rec, _ := e.do(t, op.method, "/api/agents/ext-offline/external-stacks/proj/"+op.path, `{}`, viewer)
		if rec.Code != 403 {
			t.Fatalf("%s permission bypass %d", op.path, rec.Code)
		}
	}
}
