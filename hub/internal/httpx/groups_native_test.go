package httpx

import (
	"encoding/json"
	"net/http"
	"testing"
)

// Native-binding regressions: path identity and JSON PATCH presence must survive
// GF binding without accepting query/form parameters as body fields.
func TestGroupsNativePatchPresenceAndSources(t *testing.T) {
	env := newTestEnv(t)
	cookies := env.login(t)
	group := env.createGroup(t, cookies, "native-path")
	other := env.createGroup(t, cookies, "native-other")

	for _, tc := range []struct{ path, body, message string }{
		{"?name=injected", `{}`, "没有需要修改的字段"},
		{"", `{"name":null}`, "用户组名不能为空"},
	} {
		rec, body := env.do(t, http.MethodPatch, "/api/groups/"+group.ID+tc.path, tc.body, cookies)
		if rec.Code != http.StatusBadRequest || body.Error.Message != tc.message {
			t.Fatalf("PATCH %s %s = %d %s", tc.path, tc.body, rec.Code, rec.Body.String())
		}
	}
	// Query groupID must not redirect writes to another group.
	rec, _ := env.do(t, http.MethodPatch, "/api/groups/"+group.ID+"?groupID="+other.ID,
		`{"description":null}`, cookies)
	if rec.Code != http.StatusOK {
		t.Fatalf("null description: %d %s", rec.Code, rec.Body.String())
	}
	var updated struct {
		Group struct {
			ID          string `json:"id"`
			Description string `json:"description"`
		} `json:"group"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &updated); err != nil {
		t.Fatal(err)
	}
	if updated.Group.ID != group.ID || updated.Group.Description != "" {
		t.Fatalf("wrong target or null semantics: %s", rec.Body.String())
	}

	// Membership and grants missing from JSON are clear operations, even when
	// a query parameter suggests a value. They must remain [] rather than null.
	env.do(t, http.MethodPut, "/api/groups/"+group.ID+"/grants",
		`{"grants":[{"agent_id":"offline","permission":"manage"}]}`, cookies)
	rec, _ = env.do(t, http.MethodPut, "/api/groups/"+group.ID+"/grants?grants[0][agent_id]=injected&grants[0][permission]=manage", `{}`, cookies)
	if rec.Code != http.StatusOK {
		t.Fatalf("clear grants: %d %s", rec.Code, rec.Body.String())
	}
	var grants struct {
		Grants []json.RawMessage `json:"grants"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &grants); err != nil {
		t.Fatal(err)
	}
	if grants.Grants == nil || len(grants.Grants) != 0 {
		t.Fatalf("query grants were bound: %s", rec.Body.String())
	}
}

func TestGroupsNativeStrictJSONAndExactErrors(t *testing.T) {
	env := newTestEnv(t)
	cookies := env.login(t)
	group := env.createGroup(t, cookies, "native-json")
	for _, tc := range []struct{ method, suffix, body string }{
		{http.MethodPost, "", `{"name":123}`},
		{http.MethodPost, "", `{"name":"x","unknown":true}`},
		{http.MethodPatch, "/" + group.ID, `{"name":false}`},
		{http.MethodPut, "/" + group.ID + "/members", `{"user_ids":[1]}`},
		{http.MethodPut, "/" + group.ID + "/grants", `{"grants":[{"agent_id":123,"permission":"view"}]}`},
	} {
		rec, body := env.do(t, tc.method, "/api/groups"+tc.suffix, tc.body, cookies)
		if rec.Code != http.StatusBadRequest || body.Error.Code != "bad_request" || !containsSub(body.Error.Message, "请求格式错误: ") {
			t.Fatalf("strict JSON %s %s = %d %s", tc.method, tc.body, rec.Code, rec.Body.String())
		}
	}
	for _, tc := range []struct{ suffix, body, message string }{
		{"/members", `{"user_ids":["missing","missing"]}`, "以下用户不存在: [missing]"},
		{"/grants", `{"grants":[{"permission":"view"}]}`, "第 1 条授权缺少 agent_id"},
		{"/grants", `{"grants":[{"agent_id":"x","permission":"View"}]}`, "第 1 条授权的 permission 必须是 view、operate 或 manage，实际为 \"View\""},
	} {
		rec, body := env.do(t, http.MethodPut, "/api/groups/"+group.ID+tc.suffix, tc.body, cookies)
		if rec.Code != http.StatusBadRequest || body.Error.Message != tc.message {
			t.Fatalf("exact error: %d %s", rec.Code, rec.Body.String())
		}
	}
}
