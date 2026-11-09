package httpx

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"arkade/protocol/envelope"
)

func TestImagePullHTTPAndTaskPermissions(t *testing.T) {
	e := newTestEnv(t)
	a := startImportAgent(t, e, func(req envelope.Envelope, n int) any {
		switch req.Method {
		case envelope.MethodImagePull, envelope.MethodImageTaskStatus:
			return envelope.ImageTask{TaskID: "pull-test", Status: "succeeded", Operation: "pull", Phase: "done", Message: "镜像拉取完成"}
		case envelope.MethodImageTaskRelease:
			return envelope.ImageTaskReleaseResult{TaskID: "pull-test", Released: true}
		}
		return nil
	})
	cookies := e.loginAsFor(t, a.url, "admin", testPassword)
	resp, raw := doHTTP(t, http.MethodPost, a.url+"/api/agents/"+a.id+"/images/pull", `{"reference":"alpine:3.20"}`, cookies)
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("pull %d %s", resp.StatusCode, raw)
	}
	var out struct {
		Task struct {
			ID string `json:"id"`
		} `json:"task"`
	}
	if err := json.Unmarshal(raw, &out); err != nil || out.Task.ID == "" {
		t.Fatalf("task: %s", raw)
	}
	path := a.url + "/api/image-tasks/" + out.Task.ID
	e.createUser(t, e.login(t), "image-ungranted", testPassword, "user")
	limited := e.loginAsFor(t, a.url, "image-ungranted", testPassword)
	resp, raw = doHTTP(t, http.MethodGet, path, "", limited)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("跨用户访问任务未被拒绝 %d %s", resp.StatusCode, raw)
	}
	resp, raw = doHTTP(t, http.MethodPost, a.url+"/api/agents/"+a.id+"/images/pull", `{"reference":"alpine:3.20"}`, limited)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("未授权拉取未被拒绝 %d %s", resp.StatusCode, raw)
	}
	resp, raw = doHTTP(t, http.MethodGet, path, "", nil)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauth %d %s", resp.StatusCode, raw)
	}
	for i := 0; i < 100; i++ {
		resp, raw = doHTTP(t, http.MethodGet, path, "", cookies)
		if resp.StatusCode != 200 {
			t.Fatalf("get %d %s", resp.StatusCode, raw)
		}
		var task struct {
			Task struct {
				Status string `json:"status"`
			} `json:"task"`
		}
		_ = json.Unmarshal(raw, &task)
		if task.Task.Status == "succeeded" {
			return
		}
		if task.Task.Status == "failed" {
			t.Fatalf("task failed %s", raw)
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("任务未完成")
}
func TestImageMigrateConfirmationGuards(t *testing.T) {
	e := newTestEnv(t)
	a := startImportAgent(t, e, nil)
	cookies := e.loginAsFor(t, a.url, "admin", testPassword)
	for _, body := range []string{`{"reference":"alpine:3.20","target_agent_id":"other"}`, `{"reference":"alpine:3.20","target_agent_id":"` + a.id + `","confirmation":true}`} {
		resp, raw := doHTTP(t, http.MethodPost, a.url+"/api/agents/"+a.id+"/images/migrate", body, cookies)
		if resp.StatusCode != 400 {
			t.Fatalf("guard %d %s", resp.StatusCode, raw)
		}
	}
	if len(a.requests()) != 0 {
		t.Fatal("无效确认发起了RPC")
	}
}
func TestImageUploadRejectsOversizeBeforeRPC(t *testing.T) {
	e := newTestEnv(t)
	a := startImportAgent(t, e, nil)
	cookies := e.loginAsFor(t, a.url, "admin", testPassword)
	resp, _ := doHTTP(t, http.MethodPost, a.url+"/api/agents/"+a.id+"/images/import", "", cookies)
	if resp.StatusCode != 400 && resp.StatusCode != 411 {
		t.Fatalf("empty upload=%d", resp.StatusCode)
	}
	if len(a.requests()) != 0 {
		t.Fatal("空上传发起了RPC")
	}
}
