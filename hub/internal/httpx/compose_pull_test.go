package httpx

import (
	"arkade/hub/internal/store"
	"arkade/protocol/envelope"
	"context"
	"net/http"
	"strings"
	"testing"
	"time"
)

// pull 必须走统一任务入口，保留输出游标与任务隔离，不再依赖独立 HTTP 包装。
func TestComposePullViaOperationTaskStartAndCursor(t *testing.T) {
	e := newTestEnv(t)
	a := startImportAgent(t, e, func(req envelope.Envelope, _ int) any {
		out := envelope.ComposeOperationResult{TaskID: "pull-operation-0001", Name: "demo", Action: "pull", Command: "docker compose --project-name demo pull", Status: "running"}
		switch req.Method {
		case envelope.MethodComposeOperationStart:
			var p envelope.ComposeOperationStartPayload
			_ = envelope.DecodePayload(&req, &p)
			if p.Name != "demo" || p.RequestID != "request-0001" || p.Action != "pull" {
				t.Errorf("invalid pull operation %#v", p)
			}
		case envelope.MethodComposeOperationGet:
			var p envelope.ComposeOperationGetPayload
			_ = envelope.DecodePayload(&req, &p)
			if p.TaskID != "pull-operation-0001" {
				t.Errorf("invalid task %#v", p)
			}
			if p.After == 3 {
				out.Output = []envelope.ComposeOperationEvent{{Seq: 4, Stream: "stderr", Text: "web Pulling"}}
				out.NextCursor = 4
			}
		default:
			t.Errorf("unexpected RPC %s", req.Method)
		}
		return out
	})
	now := time.Now()
	if err := e.db.CreateComposeStack(context.Background(), store.ComposeStack{ID: "pull-stack", AgentID: a.id, Name: "demo", Content: sampleCompose, ConfigHash: "test", Version: 1, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	cookies := e.loginAsFor(t, a.url, "admin", testPassword)
	base := a.url + "/api/agents/" + a.id + "/stacks/demo/operation-tasks"
	resp, body := doHTTP(t, http.MethodPost, base, `{"request_id":"request-0001","action":"pull"}`, cookies)
	if resp.StatusCode != 202 || !strings.Contains(string(body), `"action":"pull"`) {
		t.Fatalf("%d %s", resp.StatusCode, body)
	}
	if !strings.Contains(resp.Header.Get("Cache-Control"), "no-store") {
		t.Fatal("task output must not cache")
	}
	resp, body = doHTTP(t, http.MethodGet, base+"/pull-operation-0001?after=3", "", cookies)
	if resp.StatusCode != 200 || !strings.Contains(string(body), `"seq":4`) {
		t.Fatalf("%d %s", resp.StatusCode, body)
	}
	resp, body = doHTTP(t, http.MethodGet, strings.Replace(base, "/demo/", "/another/", 1)+"/pull-operation-0001?after=3", "", cookies)
	if resp.StatusCode != 404 {
		t.Fatalf("crossproject %d %s", resp.StatusCode, body)
	}
}
