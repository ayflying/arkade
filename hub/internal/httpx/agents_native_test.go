package httpx

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestAgentsNativeContracts(t *testing.T) {
	e := newTestEnv(t)
	cookies := e.loginAs(t, "admin", testPassword)
	created, _ := e.do(t, http.MethodPost, "/api/agents", `{"name":"native-host","note":" note "}`, cookies)
	if created.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", created.Code, created.Body.String())
	}
	var enrollment struct {
		Agent struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		}
		Enrollment struct {
			Token string `json:"token"`
		}
	}
	if err := json.Unmarshal(created.Body.Bytes(), &enrollment); err != nil {
		t.Fatal(err)
	}
	if enrollment.Agent.ID == "" || enrollment.Agent.Status != "pending" || enrollment.Enrollment.Token == "" {
		t.Fatalf("create body: %s", created.Body.String())
	}
	for key, value := range map[string]string{"Cache-Control": "no-store", "Pragma": "no-cache", "Expires": "0"} {
		if got := created.Header().Get(key); got != value {
			t.Fatalf("%s=%q want %q", key, got, value)
		}
	}
	id := enrollment.Agent.ID
	for _, key := range []string{"agentID", "AgentID", "agent_id", "agentid"} {
		rec, _ := e.do(t, http.MethodPatch, "/api/agents/"+id+"?"+key+"=nonexistent", `{"note":"path wins"}`, cookies)
		if rec.Code != 200 {
			t.Fatalf("query path injection %s: %d %s", key, rec.Code, rec.Body.String())
		}
	}
	// Query fields may not supply or override strict JSON body values.
	patched, _ := e.do(t, http.MethodPatch, "/api/agents/"+id+`?display_name=query`, `{"display_name":"json","note":null}`, cookies)
	if patched.Code != 200 || !strings.Contains(patched.Body.String(), `"display_name":"json"`) {
		t.Fatalf("patch: %d %s", patched.Code, patched.Body.String())
	}
	malformed, _ := e.do(t, http.MethodPatch, "/api/agents/"+id, `{"labels":true}`, cookies)
	if malformed.Code != 400 || !strings.Contains(malformed.Body.String(), "请求格式错误: ") {
		t.Fatalf("patch decode: %d %s", malformed.Code, malformed.Body.String())
	}
	reset, _ := e.do(t, http.MethodPost, "/api/agents/"+id+"/token/reset", "", cookies)
	if reset.Code != 200 || reset.Header().Get("Pragma") != "no-cache" {
		t.Fatalf("reset: %d %s", reset.Code, reset.Body.String())
	}
	for _, suffix := range []string{"disable", "enable"} {
		rec, _ := e.do(t, http.MethodPost, "/api/agents/"+id+"/"+suffix, `not json`, cookies)
		if rec.Code != 200 {
			t.Fatalf("%s: %d %s", suffix, rec.Code, rec.Body.String())
		}
	}
	deleted, _ := e.do(t, http.MethodDelete, "/api/agents/"+id, "", cookies)
	if deleted.Code != 200 || !strings.Contains(deleted.Body.String(), `"deleted":true`) {
		t.Fatalf("delete: %d %s", deleted.Code, deleted.Body.String())
	}
}
func TestAgentsNativeDecodePolicies(t *testing.T) {
	e := newTestEnv(t)
	cookies := e.loginAs(t, "admin", testPassword)
	for _, body := range []string{`{"name":true}`, strings.Repeat(" ", 16<<10) + `{"name":"x"}`} {
		rec, _ := e.do(t, http.MethodPost, "/api/agents", body, cookies)
		if rec.Code != 400 || !strings.Contains(rec.Body.String(), "请求体格式错误") {
			t.Fatalf("decode: %d %s", rec.Code, rec.Body.String())
		}
	}
	// A JSON null request remains a zero-value typed request, as in encoding/json.
	rec, _ := e.do(t, http.MethodPost, "/api/agents", `null`, cookies)
	if rec.Code != 400 || !strings.Contains(rec.Body.String(), "服务器名称不能为空") {
		t.Fatalf("null: %d %s", rec.Code, rec.Body.String())
	}
}
