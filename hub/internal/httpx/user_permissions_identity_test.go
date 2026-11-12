package httpx

import (
	userapi "arkade/hub/api/users/v1"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestUserPermissionsIdentityHTTP(t *testing.T) {
	e := newTestEnv(t)
	admin := e.login(t)
	id := e.createUser(t, admin, "permission-user", "PermissionPass123!", "user")
	// Empty inventory is represented by [], never null.
	rec, _ := e.do(t, http.MethodGet, "/api/users/"+id+"/permissions", "", admin)
	if rec.Code != 200 {
		t.Fatal(rec.Code, rec.Body.String())
	}
	var empty userapi.PermissionsRes
	if err := json.Unmarshal(rec.Body.Bytes(), &empty); err != nil {
		t.Fatal(err)
	}
	if empty.Agents == nil || len(empty.Agents) != 0 {
		t.Fatalf("empty agents must be []: %s", rec.Body.String())
	}
	e.seedAgent(t, "permissions-a")
	e.seedAgent(t, "permissions-b")
	first := e.createGroup(t, admin, "permission-readers")
	second := e.createGroup(t, admin, "permission-managers")
	for _, group := range []struct{ id, permission string }{{first.ID, "view"}, {second.ID, "manage"}} {
		for _, request := range []struct{ path, body string }{
			{"/api/groups/" + group.id + "/members", `{"user_ids":["` + id + `","u-test"]}`},
			{"/api/groups/" + group.id + "/grants", `{"grants":[{"agent_id":"permissions-a","permission":"` + group.permission + `"}]}`},
		} {
			r, _ := e.do(t, http.MethodPut, request.path, request.body, admin)
			if r.Code != 200 {
				t.Fatal(r.Code, r.Body.String())
			}
		}
	}
	fetch := func(path string) userapi.PermissionsRes {
		t.Helper()
		r, _ := e.do(t, http.MethodGet, path, "", admin)
		if r.Code != 200 {
			t.Fatal(r.Code, r.Body.String())
		}
		for _, secret := range []string{"password_hash", "PermissionPass123!", "agent_token", "$2a$", "$2b$"} {
			if strings.Contains(r.Body.String(), secret) {
				t.Fatalf("secret leaked: %s", r.Body.String())
			}
		}
		var out userapi.PermissionsRes
		if err := json.Unmarshal(r.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	for _, alias := range []string{"userID", "UserID", "userid", "USERID", "user_id", "USER_ID", "userId"} {
		out := fetch("/api/users/" + id + "/permissions?" + alias + "=u-test")
		if out.UserID != id || out.Role != "user" || len(out.Agents) != 2 {
			t.Fatalf("path identity shadowed by %s: %+v", alias, out)
		}
	}
	out := fetch("/api/users/" + id + "/permissions?userID=u-test&user_id=u-test")
	check := func(out userapi.PermissionsRes, levelA, levelB string) {
		t.Helper()
		levels := map[string]userapi.AgentPermission{}
		for _, a := range out.Agents {
			levels[a.AgentID] = a
		}
		a := levels["permissions-a"]
		if a.Permission != levelA || levels["permissions-b"].Permission != levelB || len(a.Sources) != 2 {
			t.Fatalf("incorrect max/provenance: %+v", out)
		}
		sources := map[string]string{}
		for _, source := range a.Sources {
			sources[source.GroupID] = source.Permission
			if source.GroupName == "" {
				t.Fatal("missing group source name")
			}
		}
		if sources[first.ID] != "view" || sources[second.ID] != "manage" {
			t.Fatal(sources)
		}
	}
	check(out, "manage", "none")
	// Administrator effective manage does not erase actual group provenance.
	check(fetch("/api/users/u-test/permissions"), "manage", "manage")
	viewer := e.loginAs(t, "permission-user", "PermissionPass123!")
	denied, body := e.do(t, http.MethodGet, "/api/users/"+id+"/permissions", "", viewer)
	if denied.Code != 403 || body.Error.Code != "forbidden" {
		t.Fatalf("non-admin preview allowed: %d %s", denied.Code, denied.Body.String())
	}
	missing, _ := e.do(t, http.MethodGet, "/api/users/not-present/permissions", "", admin)
	if missing.Code != 404 {
		t.Fatal(missing.Code, missing.Body.String())
	}
	disabled, _ := e.do(t, http.MethodPatch, "/api/users/"+id, `{"disabled":true}`, admin)
	if disabled.Code != 200 {
		t.Fatal(disabled.Code, disabled.Body.String())
	}
	out = fetch("/api/users/" + id + "/permissions")
	if !out.Disabled {
		t.Fatal("disabled flag missing")
	}
	check(out, "none", "none")
}

func TestMandatoryPasswordChangeRawHTTP(t *testing.T) {
	e := newTestEnv(t)
	admin := e.login(t)
	e.createUser(t, admin, "temporary-user", "TemporaryPass123!", "user")
	// Deliberately bypass loginAs: that legacy fixture helper completes the
	// first-change workflow automatically and would conceal this restriction.
	login, _ := e.do(t, http.MethodPost, "/api/auth/login", `{"username":"temporary-user","password":"TemporaryPass123!"}`, nil)
	if login.Code != 200 {
		t.Fatal(login.Code, login.Body.String())
	}
	cookies := login.Result().Cookies()
	var state struct {
		User struct {
			MustChangePassword bool `json:"must_change_password"`
		} `json:"user"`
	}
	if err := json.Unmarshal(login.Body.Bytes(), &state); err != nil {
		t.Fatal(err)
	}
	if !state.User.MustChangePassword {
		t.Fatal("temporary password not marked")
	}
	blocked, body := e.do(t, http.MethodGet, "/api/agents", "", cookies)
	if blocked.Code != 403 || body.Error.Code != "password_change_required" {
		t.Fatalf("mandatory change not enforced: %d %s", blocked.Code, blocked.Body.String())
	}
	for _, path := range []string{"/api/auth/me", "/api/account"} {
		r, _ := e.do(t, http.MethodGet, path, "", cookies)
		if r.Code != 200 {
			t.Fatalf("required recovery route blocked %s: %d %s", path, r.Code, r.Body.String())
		}
	}
	change, _ := e.do(t, http.MethodPut, "/api/account/password", `{"old_password":"TemporaryPass123!","new_password":"UpdatedTemporaryPass123!"}`, cookies)
	if change.Code != 204 {
		t.Fatal(change.Code, change.Body.String())
	}
	old, _ := e.do(t, http.MethodGet, "/api/auth/me", "", cookies)
	if old.Code != 401 {
		t.Fatal("old session survived", old.Code, old.Body.String())
	}
	relogin, _ := e.do(t, http.MethodPost, "/api/auth/login", `{"username":"temporary-user","password":"UpdatedTemporaryPass123!"}`, nil)
	if relogin.Code != 200 {
		t.Fatal(relogin.Code, relogin.Body.String())
	}
	if err := json.Unmarshal(relogin.Body.Bytes(), &state); err != nil {
		t.Fatal(err)
	}
	if state.User.MustChangePassword {
		t.Fatal("first change marker not cleared")
	}
	allowed, _ := e.do(t, http.MethodGet, "/api/agents", "", relogin.Result().Cookies())
	if allowed.Code != 200 {
		t.Fatal("ordinary access not restored", allowed.Code, allowed.Body.String())
	}
}
