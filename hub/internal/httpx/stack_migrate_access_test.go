package httpx

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"arkade/hub/internal/stackmigrate"
	"arkade/hub/internal/user"
)

func TestStackMigrationTaskAccessOwnerAndBothManageHTTP(t *testing.T) {
	e := newTestEnv(t)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := e.migrateMgr.Close(ctx); err != nil {
			t.Error(err)
		}
	})
	a := startImportAgent(t, e, nil)
	seedTargetAgent(t, e, "access-target", "target")
	admin := e.login(t)
	owner := e.createUser(t, admin, "migration-owner", testPassword, "user")
	other := e.createUser(t, admin, "migration-other", testPassword, "user")
	group := e.createGroup(t, admin, "migration-access")
	ctx := context.Background()
	if err := e.db.SetGroupMembers(ctx, group.ID, []string{owner, other}); err != nil {
		t.Fatal(err)
	}
	grant := func(agent string, p user.Permission) {
		t.Helper()
		if err := e.db.SetGroupGrant(ctx, group.ID, agent, p); err != nil {
			t.Fatal(err)
		}
	}
	grant(a.id, user.PermManage)
	grant("access-target", user.PermManage)
	snap, err := e.migrateMgr.Submit(ctx, stackmigrate.Options{Source: a.id, Target: "access-target", StackName: "demo", OwnerID: owner})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, 3*time.Second, func() bool {
		cur, err := e.migrateMgr.Get(snap.TaskID, 0)
		return err == nil && cur.Status == stackmigrate.StatusSucceeded
	}, "migration task succeeded")
	path := a.url + "/api/agents/" + a.id + "/stacks/demo/operation-tasks/" + snap.TaskID
	ownerCookies := e.loginAs(t, "migration-owner", testPassword)
	otherCookies := e.loginAs(t, "migration-other", testPassword)
	adminCookies := e.loginAs(t, "admin", testPassword)
	grant(a.id, user.PermOperate)
	resp, raw := doHTTP(t, http.MethodGet, path, "", ownerCookies)
	if resp.StatusCode != 403 {
		t.Fatalf("source operate status %d %s", resp.StatusCode, raw)
	}
	grant(a.id, user.PermManage)
	grant("access-target", user.PermOperate)
	resp, raw = doHTTP(t, http.MethodGet, path, "", ownerCookies)
	if resp.StatusCode != 403 {
		t.Fatalf("target operate status %d %s", resp.StatusCode, raw)
	}
	grant("access-target", user.PermManage)
	resp, raw = doHTTP(t, http.MethodGet, path, "", otherCookies)
	if resp.StatusCode != 404 {
		t.Fatalf("non-owner status %d %s", resp.StatusCode, raw)
	}
	resp, raw = doHTTP(t, http.MethodGet, path, "", adminCookies)
	var out migrateTaskDTO
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 200 || out.Task.TaskID != snap.TaskID || out.Task.Status != stackmigrate.StatusSucceeded {
		t.Fatalf("admin %d %s", resp.StatusCode, raw)
	}
}

func TestStackMigrationResidualUncertainHTTP(t *testing.T) {
	e := newTestEnv(t)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := e.migrateMgr.Close(ctx); err != nil {
			t.Error(err)
		}
	})
	a := startImportAgent(t, e, nil)
	seedTargetAgent(t, e, "uncertain-target", "target")
	e.migrateBackend.mu.Lock()
	e.migrateBackend.copyErr = &stackmigrate.ResidualError{Err: errors.New("mutation interrupted"), Residual: stackmigrate.Residual{Uncertain: true}}
	e.migrateBackend.mu.Unlock()
	cookies := e.loginAs(t, "admin", testPassword)
	snap, err := e.migrateMgr.Submit(context.Background(), stackmigrate.Options{Source: a.id, Target: "uncertain-target", StackName: "demo"})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, 3*time.Second, func() bool {
		cur, err := e.migrateMgr.Get(snap.TaskID, 0)
		return err == nil && cur.Status == stackmigrate.StatusFailed
	}, "uncertain migration failed")
	resp, raw := doHTTP(t, http.MethodGet, a.url+"/api/agents/"+a.id+"/stacks/demo/operation-tasks/"+snap.TaskID, "", cookies)
	if resp.StatusCode != 200 || !strings.Contains(string(raw), `"uncertain":true`) {
		t.Fatalf("residual not transmitted %d %s", resp.StatusCode, raw)
	}
}
