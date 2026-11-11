package httpx

import (
	"arkade/hub/internal/user"
	"fmt"
	"net/http"
	"testing"

	"arkade/hub/internal/store"
)

func TestUsersNativePresenceAndPath(t *testing.T) {
	env := newTestEnv(t)
	cookies := env.login(t)
	id := env.createUser(t, cookies, "native-user", testPassword, "user")
	for _, tc := range []struct{ body, message string }{
		{`{}`, "没有需要修改的字段"},
		{`{"display_name":null}`, "显示名不能为空"},
	} {
		rec, body := env.do(t, http.MethodPatch, "/api/users/"+id+"?display_name=injected", tc.body, cookies)
		if rec.Code != 400 || body.Error.Message != tc.message {
			t.Fatalf("presence: %d %s", rec.Code, rec.Body.String())
		}
	}
	disabled := true
	if err := env.db.UpdateUser(t.Context(), id, store.UserUpdate{Disabled: &disabled}, 1); err != nil {
		t.Fatal(err)
	}
	rec, _ := env.do(t, http.MethodPatch, "/api/users/"+id+"?userID=other", `{"disabled":null}`, cookies)
	if rec.Code != 200 {
		t.Fatalf("null bool: %d %s", rec.Code, rec.Body.String())
	}
	record, err := env.db.GetUserByID(t.Context(), id)
	if err != nil || record.Disabled {
		t.Fatalf("null bool/path not preserved: %+v %v", record, err)
	}
}

func TestUsersNativeStrictFirstJSONAndCaseKeys(t *testing.T) {
	env := newTestEnv(t)
	cookies := env.login(t)
	for i, suffix := range []string{` {"username":"ignored"}`, ` trailing garbage`} {
		name := fmt.Sprintf("strict-case-%d", i)
		payload := fmt.Sprintf(`{"username":"wrong-first","Username":%q,"password":%q,"role":"user"}%s`, name, testPassword, suffix)
		rec, _ := env.do(t, http.MethodPost, "/api/users?username=injected", payload, cookies)
		if rec.Code != 200 {
			t.Fatalf("first create %d: %d %s", i, rec.Code, rec.Body.String())
		}
		u, err := env.db.GetUserByUsername(t.Context(), name)
		if err != nil {
			t.Fatal(err)
		}
		password := fmt.Sprintf("changed-password-%d", i)
		payload = fmt.Sprintf(`{"password":"wrong-first","Password":%q}%s`, password, suffix)
		rec, _ = env.do(t, http.MethodPut, "/api/users/"+u.ID+"/password?userID=ignored", payload, cookies)
		if rec.Code != 204 || rec.Body.Len() != 0 {
			t.Fatalf("first password: %d %s", rec.Code, rec.Body.String())
		}
		u, err = env.db.GetUserByID(t.Context(), u.ID)
		if err != nil {
			t.Fatal(err)
		}
		if err := user.VerifyPassword(u.PasswordHash, password); err != nil {
			t.Fatalf("strict password not used: %v", err)
		}
		payload = `{"display_name":"wrong-first","Display_Name":"last-wins"}` + suffix
		rec, _ = env.do(t, http.MethodPatch, "/api/users/"+u.ID, payload, cookies)
		if rec.Code != 200 {
			t.Fatalf("first patch: %d %s", rec.Code, rec.Body.String())
		}
		u, err = env.db.GetUserByID(t.Context(), u.ID)
		if err != nil || u.DisplayName != "last-wins" {
			t.Fatalf("strict patch: %+v %v", u, err)
		}
	}
}

func TestNativeAdminGatePrecedesBodyValidation(t *testing.T) {
	env := newTestEnv(t)
	admin := env.login(t)
	env.createUser(t, admin, "native-reader", testPassword, "user")
	viewer := env.loginAs(t, "native-reader", testPassword)
	for _, path := range []string{"/api/users", "/api/groups"} {
		rec, body := env.do(t, http.MethodPost, path, `not json`, viewer)
		if rec.Code != 403 || body.Error.Code != "forbidden" {
			t.Fatalf("admin gate %s: %d %s", path, rec.Code, rec.Body.String())
		}
	}
}
