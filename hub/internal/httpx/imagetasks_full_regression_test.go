package httpx

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"arkade/hub/internal/agentlink"
	"arkade/hub/internal/imagetask"
	"arkade/hub/internal/user"
	"arkade/protocol/envelope"
)

func TestImageTasksFullListCancelOwnerGrantsAndPath(t *testing.T) {
	e := newTestEnv(t)
	a := startImportAgent(t, e, func(req envelope.Envelope, _ int) any {
		switch req.Method {
		case envelope.MethodImagePull, envelope.MethodImageTaskStatus:
			return envelope.ImageTask{TaskID: "remote-pull", Status: "running"}
		case envelope.MethodImageTaskCancel:
			return envelope.ImageTaskCancelResult{TaskID: "remote-pull", Canceled: true}
		case envelope.MethodImageTaskRelease:
			return envelope.ImageTaskReleaseResult{TaskID: "remote-pull", Released: true}
		}
		return nil
	})
	admin := e.login(t)
	owner := e.createUser(t, admin, "image-full-owner", testPassword, "user")
	other := e.createUser(t, admin, "image-full-other", testPassword, "user")
	group := e.createGroup(t, admin, "image-full")
	if err := e.db.SetGroupMembers(t.Context(), group.ID, []string{owner, other}); err != nil {
		t.Fatal(err)
	}
	grant := func(id string, p user.Permission) {
		t.Helper()
		if err := e.db.SetGroupGrant(t.Context(), group.ID, id, p); err != nil {
			t.Fatal(err)
		}
	}
	grant(a.id, user.PermManage)
	ownerCookies := e.loginAs(t, "image-full-owner", testPassword)
	otherCookies := e.loginAs(t, "image-full-other", testPassword)
	resp, raw := doHTTP(t, http.MethodPost, a.url+"/api/agents/"+a.id+"/images/pull", `{"reference":"alpine:3.20"}`, ownerCookies)
	if resp.StatusCode != 202 {
		t.Fatalf("start %d %s", resp.StatusCode, raw)
	}
	var created struct {
		Task struct {
			ID string `json:"id"`
		} `json:"task"`
	}
	if err := json.Unmarshal(raw, &created); err != nil || created.Task.ID == "" {
		t.Fatalf("task %s %v", raw, err)
	}
	id := created.Task.ID
	waitFor(t, 3*time.Second, func() bool {
		for _, call := range a.requests() {
			if call.Method == envelope.MethodImagePull {
				return true
			}
		}
		return false
	}, "image pull began")
	list := func(cookies []*http.Cookie, suffix string, want int) {
		t.Helper()
		r, b := doHTTP(t, http.MethodGet, a.url+"/api/image-tasks"+suffix, "", cookies)
		var out struct {
			Tasks []json.RawMessage `json:"tasks"`
		}
		if err := json.Unmarshal(b, &out); err != nil || r.StatusCode != 200 || out.Tasks == nil || len(out.Tasks) != want {
			t.Fatalf("list %d %s err=%v", r.StatusCode, b, err)
		}
	}
	list(ownerCookies, "", 1)
	list(ownerCookies, "?agent_id="+a.id, 1)
	list(ownerCookies, "?agent_id=no-match", 0)
	list(otherCookies, "", 0)
	resp, raw = doHTTP(t, http.MethodPost, a.url+"/api/image-tasks/"+id+"/cancel", "", otherCookies)
	if resp.StatusCode != 403 {
		t.Fatalf("non-owner cancel %d %s", resp.StatusCode, raw)
	}
	grant(a.id, user.PermOperate)
	list(ownerCookies, "", 0)
	resp, raw = doHTTP(t, http.MethodPost, a.url+"/api/image-tasks/"+id+"/cancel", "", ownerCookies)
	if resp.StatusCode != 403 {
		t.Fatalf("revoked cancel %d %s", resp.StatusCode, raw)
	}
	grant(a.id, user.PermManage)
	// A real path task cannot be replaced by any of GF's normalized query aliases.
	for _, alias := range []string{"taskID", "task_id", "TASK-ID"} {
		resp, raw = doHTTP(t, http.MethodPost, a.url+"/api/image-tasks/"+id+"/cancel?"+alias+"=missing", "", ownerCookies)
		if resp.StatusCode != 200 {
			t.Fatalf("path alias %s %d %s", alias, resp.StatusCode, raw)
		}
		var canceled struct {
			Task struct {
				ID     string `json:"id"`
				Status string `json:"status"`
			} `json:"task"`
		}
		if err := json.Unmarshal(raw, &canceled); err != nil || canceled.Task.ID != id || canceled.Task.Status != "canceled" {
			t.Fatalf("cancel %s %v", raw, err)
		}
	}
	waitFor(t, 3*time.Second, func() bool {
		for _, call := range a.requests() {
			if call.Method == envelope.MethodImageTaskCancel {
				return true
			}
		}
		return false
	}, "remote cancel")
	list(ownerCookies, "", 1)
	// Target grants are independently required even before migration RPC begins.
	// Register a real cross-agent Manager task and read it through native HTTP.
	// The target is intentionally offline: failure history still requires both grants.
	e.seedAgent(t, "image-full-source")
	e.seedAgent(t, "image-full-target")
	manager := imagetask.New(agentlink.NewImageBackend(agentlink.NewOps(e.registry)))
	t.Cleanup(func() { _ = manager.Close() })
	managed := NewRouter(Deps{Log: e.log, DB: e.db, Auth: e.auth, Registry: e.registry, Ops: agentlink.NewOps(e.registry), ImageTasks: manager})
	managedURL := newTestServer(t, managed)
	managedCookies := e.loginAsFor(t, managedURL, "image-full-owner", testPassword)
	grant("image-full-source", user.PermManage)
	grant("image-full-target", user.PermManage)
	migration, err := manager.Start("migrate", "image-full-source", "image-full-target", "alpine", owner)
	if err != nil {
		t.Fatal(err)
	}
	crossList := func(want int) {
		t.Helper()
		r, b := doHTTP(t, http.MethodGet, managedURL+"/api/image-tasks?agent_id=image-full-target", "", managedCookies)
		var out struct {
			Tasks []json.RawMessage `json:"tasks"`
		}
		if err := json.Unmarshal(b, &out); err != nil || r.StatusCode != 200 || out.Tasks == nil || len(out.Tasks) != want {
			t.Fatalf("cross list %d %s %v", r.StatusCode, b, err)
		}
	}
	crossList(1)
	grant("image-full-target", user.PermOperate)
	crossList(0)
	resp, raw = doHTTP(t, http.MethodPost, managedURL+"/api/image-tasks/"+migration.ID+"/cancel", "", managedCookies)
	if resp.StatusCode != 403 {
		t.Fatalf("cross target revoked %d %s", resp.StatusCode, raw)
	}
	grant("image-full-target", user.PermManage)
	grant("image-full-source", user.PermOperate)
	crossList(0)
	resp, raw = doHTTP(t, http.MethodPost, managedURL+"/api/image-tasks/"+migration.ID+"/cancel", "", managedCookies)
	if resp.StatusCode != 403 {
		t.Fatalf("cross source revoked %d %s", resp.StatusCode, raw)
	}
	grant("image-full-source", user.PermManage)
	crossList(1)
	resp, raw = doHTTP(t, http.MethodPost, managedURL+"/api/image-tasks/"+migration.ID+"/cancel", "", managedCookies)
	if resp.StatusCode != 200 {
		t.Fatalf("cross cancel %d %s", resp.StatusCode, raw)
	}
	grant("image-full-target", user.PermManage)
	resp, raw = doHTTP(t, http.MethodPost, a.url+"/api/agents/"+a.id+"/images/migrate", `{"reference":"alpine","target_agent_id":"image-full-target","confirmation":true}`, ownerCookies)
	if resp.StatusCode != 503 {
		t.Fatalf("offline target %d %s", resp.StatusCode, raw)
	}
	grant("image-full-target", user.PermOperate)
	resp, raw = doHTTP(t, http.MethodPost, a.url+"/api/agents/"+a.id+"/images/migrate", `{"reference":"alpine","target_agent_id":"image-full-target","confirmation":true}`, ownerCookies)
	if resp.StatusCode != 403 {
		t.Fatalf("target revoked %d %s", resp.StatusCode, raw)
	}
}
