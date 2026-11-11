package httpx

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"arkade/hub/internal/audit"
)

// TestAuditNativeQueryContract exercises the real Bind route after registration.
// GET body/form parameters must never replace or supply query filters.
func TestAuditNativeQueryContract(t *testing.T) {
	e := newTestEnv(t)
	cookies := e.loginAs(t, "admin", testPassword)
	for _, tc := range []struct {
		query, body string
		status      int
		message     string
	}{
		{"?limit=bad", "", 400, "limit 必须是整数"},
		{"?offset=9999999999999999999999999999999", "", 400, "offset 必须是整数"},
		{"?from=bad", "", 400, "from 必须是 Unix 秒或将时间格式"},
		{"?to=bad", "", 400, "to 必须是 Unix 秒或将时间格式"},
		{"?limit=bad&limit=1", "", 400, "limit 必须是整数"},
		{"?limit=1&limit=bad", "", 200, ""},
		{"?result=bad&result=success", "", 400, "result 只能是 success 或 failed"},
		{"?limit=bad", `{"limit":"1"}`, 400, "limit 必须是整数"},
		{"?limit=1", `{"limit":"bad","result":"bad"}`, 200, ""},
		{"", `{"limit":"bad","result":"bad"}`, 200, ""},
		{"?limit=1", `{invalid-json`, 200, ""},
	} {
		t.Run(tc.query+tc.body, func(t *testing.T) {
			rec, _ := e.do(t, http.MethodGet, "/api/audit"+tc.query, tc.body, cookies)
			if rec.Code != tc.status {
				t.Fatalf("status=%d want %d body=%s", rec.Code, tc.status, rec.Body.String())
			}
			if tc.message != "" {
				var out struct {
					Error struct{ Code, Message string } `json:"error"`
				}
				if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
					t.Fatal(err)
				}
				if out.Error.Code != "bad_request" || out.Error.Message != tc.message {
					t.Fatalf("error=%+v", out.Error)
				}
			}
		})
	}
}

func TestAuditNativeEmptyAndViews(t *testing.T) {
	e := newTestEnv(t)
	cookies := e.loginAs(t, "admin", testPassword)
	rec, _ := e.do(t, http.MethodGet, "/api/audit?action=never.registered", "", cookies)
	if rec.Code != 200 || strings.TrimSpace(rec.Body.String()) != `{"total":0,"items":[]}` {
		t.Fatalf("empty response status=%d body=%s", rec.Code, rec.Body.String())
	}
	now := time.Now().UTC().Truncate(time.Second)
	if err := e.db.WriteAudit(audit.Record{ID: "native-view", UserID: "u-test", Username: "admin", AgentID: "native-agent", AgentName: "server-name", Action: "never.registered", Target: "target", Detail: "detail", Result: audit.ResultFailed, Error: "failure", DurationMs: 13, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	rec, _ = e.do(t, http.MethodGet, "/api/audit?action=never.registered", "", cookies)
	var out struct {
		Total int              `json:"total"`
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if rec.Code != 200 || out.Total != 1 || len(out.Items) != 1 {
		t.Fatalf("response=%s", rec.Body.String())
	}
	item := out.Items[0]
	for key, want := range map[string]any{"id": "native-view", "user_id": "u-test", "username": "admin", "agent_id": "native-agent", "agent_name": "server-name", "action": "never.registered", "action_label": "never.registered", "target": "target", "detail": "detail", "result": "failed", "error": "failure", "duration_ms": float64(13), "created_at": float64(now.Unix())} {
		if item[key] != want {
			t.Errorf("%s=%v want %v", key, item[key], want)
		}
	}
	if len(item) != 13 {
		t.Fatalf("unexpected public fields: %v", item)
	}
}

func TestAuditNativeActionsRegistry(t *testing.T) {
	e := newTestEnv(t)
	cookies := e.loginAs(t, "admin", testPassword)
	rec, _ := e.do(t, http.MethodGet, "/api/audit/actions", "", cookies)
	var out struct {
		Actions []struct{ Action, Label string } `json:"actions"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if rec.Code != 200 || out.Actions == nil || len(out.Actions) != len(audit.All()) {
		t.Fatalf("actions=%s", rec.Body.String())
	}
	seen := map[string]bool{}
	for _, option := range out.Actions {
		if seen[option.Action] || !audit.Known(option.Action) || option.Label != audit.ActionLabel(option.Action) {
			t.Errorf("unexpected option=%+v", option)
		}
		seen[option.Action] = true
	}
}
