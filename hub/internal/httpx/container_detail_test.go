package httpx

import (
	"arkade/protocol/envelope"
	"net/http"
	"strings"
	"testing"
)

func TestContainerDetailReadOnlyRPC(t *testing.T) {
	e := newTestEnv(t)
	a := startImportAgent(t, e, func(req envelope.Envelope, _ int) any {
		if req.Method != envelope.MethodContainerInspect {
			t.Errorf("unexpected RPC %s", req.Method)
		}
		var p envelope.ContainerInspectPayload
		_ = envelope.DecodePayload(&req, &p)
		if p.ContainerID != "test-id" {
			t.Errorf("wrong id %s", p.ContainerID)
		}
		return envelope.ContainerInspectResult{ID: p.ContainerID, Name: "external-container", Image: "alpine:3.20", Created: "2026-11-10T00:00:00Z", PublishedPorts: []envelope.PublishedPort{{HostIP: "0.0.0.0", HostPort: 18080, ContainerPort: 8080, Protocol: "tcp"}}}
	})
	cookies := e.loginAsFor(t, a.url, "admin", testPassword)
	resp, body := doHTTP(t, http.MethodGet, a.url+"/api/agents/"+a.id+"/containers/test-id", "", cookies)
	if resp.StatusCode != 200 || !strings.Contains(string(body), `"host_port":18080`) {
		t.Fatalf("%d %s", resp.StatusCode, body)
	}
	if !strings.Contains(resp.Header.Get("Cache-Control"), "no-store") {
		t.Fatal("sensitive detail cached")
	}
	a.assertMethods(t, envelope.MethodContainerInspect)
}
func TestContainerDetailRequiresManageBeforeRPC(t *testing.T) {
	e := newTestEnv(t)
	admin := e.login(t)
	e.seedAgent(t, "inspect-offline")
	uid := e.createUser(t, admin, "inspectviewer", "ViewerPass123", "user")
	g := e.createGroup(t, admin, "inspect-readers")
	for _, r := range []struct{ path, body string }{{"/api/groups/" + g.ID + "/members", `{"user_ids":["` + uid + `"]}`}, {"/api/groups/" + g.ID + "/grants", `{"grants":[{"agent_id":"inspect-offline","permission":"view"}]}`}} {
		rec, _ := e.do(t, http.MethodPut, r.path, r.body, admin)
		if rec.Code != 200 {
			t.Fatal(rec.Body.String())
		}
	}
	viewer := e.loginAs(t, "inspectviewer", "ViewerPass123")
	rec, _ := e.do(t, http.MethodGet, "/api/agents/inspect-offline/containers/test-id", "", viewer)
	if rec.Code != 403 {
		t.Fatalf("permission bypass %d %s", rec.Code, rec.Body.String())
	}
}
