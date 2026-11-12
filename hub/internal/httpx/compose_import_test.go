package httpx

// Import tests use a real HTTP server, PostgreSQL and WebSocket Agent. In particular,
// observing every RPC guards against accidentally starting/stopping production services.
import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"arkade/hub/internal/agentlink"
	"arkade/hub/internal/store"
	"arkade/protocol/envelope"
)

const importTestEnv = "PASSWORD=import-secret-never-return\nPORT=18081\n"

type importAgent struct {
	url, id string
	mu      sync.Mutex
	calls   []envelope.Envelope
}

// A non-nil reply overrides preview/adopt responses, including protocol errors.
func startImportAgent(t *testing.T, e *testEnv, reply func(envelope.Envelope, int) any) *importAgent {
	t.Helper()
	reg := agentlink.NewRegistry()
	log := discardLogger()
	server := agentlink.NewServer(reg, composeTestAgentToken, "test", &agentSink{db: e.db, log: log}, log)
	// Agent 连接必须包 tracker：WebSocket 是 hijack 的连接，
	// http.Server.Shutdown 不会等它，handler 可能仍在跑并持有 DB。
	//
	// 不靠 registry：握手完成前连接还没登记进 registry。
	// 也不靠下面的 done channel：那只 join 了**客户端**的读循环，
	// 不等于服务端 HandleConnect 整体（含 OnDisconnected 写库）已结束。
	tracker := &connectionTracker{}
	router := NewRouter(Deps{
		Log: log, DB: e.db, Auth: e.auth, Registry: reg,
		Ops:          agentlink.NewOps(reg),
		AgentConn:    tracker.wrap(http.HandlerFunc(server.HandleConnect)),
		HubVersion:   "test",
		StackMigrate: e.migrateMgr,
	})
	srv := newTestServer(t, router)
	client, ack := dialAsAgent(t, srv, composeTestAgentToken)
	_ = client.conn.SetReadDeadline(time.Time{})
	a := &importAgent{url: srv, id: ack.AgentID}
	done := make(chan struct{})
	// 收尾顺序（LIFO）：
	//   tracker.close → newTestServer 的 Shutdown → e.db 的 Close
	// 先关客户端读循环（done），再让 tracker 关掉服务端 socket 并
	// join 服务端 handler，避免 handler 仍在写库时库被关闭。
	t.Cleanup(func() {
		_ = client.conn.Close()
		<-done
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := tracker.close(ctx, reg); err != nil {
			t.Errorf("等待 Agent handler 退出超时（服务端 handler 未结束，DB 仍被持有）: %v", err)
		}
	})
	_ = client.conn.SetReadDeadline(time.Time{})
	go func() {
		defer close(done)
		counts := map[string]int{}
		for {
			var req envelope.Envelope
			if err := client.conn.ReadJSON(&req); err != nil {
				return
			}
			if req.Type != envelope.TypeRequest {
				continue
			}
			a.mu.Lock()
			a.calls = append(a.calls, req)
			a.mu.Unlock()
			counts[req.Method]++
			var result any
			if reply != nil {
				result = reply(req, counts[req.Method])
			}
			if result == nil {
				switch req.Method {
				case envelope.MethodComposePreview:
					var p envelope.ComposePreviewPayload
					_ = envelope.DecodePayload(&req, &p)
					result = envelope.ComposePreviewResult{Name: p.Name, CanImport: true, ConfigHash: composeProjectHash(p.Content, p.EnvContent), Blockers: []string{}, Warnings: []string{"configuration only"}}
				case envelope.MethodComposeAdopt:
					var p envelope.ComposeAdoptPayload
					_ = envelope.DecodePayload(&req, &p)
					result = envelope.ComposeAdoptResult{Name: p.Name, Adopted: true, ConfigHash: composeProjectHash(p.Content, p.EnvContent), EnvWritten: p.EnvContent != ""}
				case envelope.MethodComposeList:
					result = envelope.ComposeListResult{Projects: []envelope.ComposeProjectSummary{}, External: []envelope.ComposeProjectSummary{}}
				default:
					result = envelope.NewError(envelope.CodeUnsupported, "unexpected RPC during import")
				}
			}
			var resp *envelope.Envelope
			if rpcErr, ok := result.(*envelope.Error); ok {
				resp = envelope.NewErrorResponse(&req, rpcErr)
			} else {
				var err error
				resp, err = envelope.NewResponse(&req, result)
				if err != nil {
					return
				}
			}
			_ = client.conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
			if err := client.conn.WriteJSON(resp); err != nil {
				return
			}
		}
	}()
	waitFor(t, 3*time.Second, func() bool { return reg.IsOnline(a.id) }, "import Agent online")
	return a
}

func (a *importAgent) requests() []envelope.Envelope {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]envelope.Envelope(nil), a.calls...)
}
func (a *importAgent) assertMethods(t *testing.T, want ...string) {
	t.Helper()
	var got []string
	for _, r := range a.requests() {
		got = append(got, r.Method)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("RPC methods = %v, want %v (no up/down or other lifecycle RPC allowed)", got, want)
	}
}
func importBody(t *testing.T, name, content, env, hash string, confirm bool) string {
	t.Helper()
	b, err := json.Marshal(map[string]any{"name": name, "content": content, "env_content": env, "expected_content_hash": hash, "confirmation": confirm})
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
func assertImportMissing(t *testing.T, e *testEnv, id, name string) {
	t.Helper()
	if _, err := e.db.GetComposeStack(t.Context(), id, name); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("rejected import must not write DB: %v", err)
	}
}
func importPost(t *testing.T, e *testEnv, a *importAgent, suffix, body string) []byte {
	t.Helper()
	cookies := e.loginAsFor(t, a.url, "admin", testPassword)
	resp, raw := doHTTP(t, http.MethodPost, a.url+"/api/agents/"+a.id+"/stacks/"+suffix, body, cookies)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("%s status=%d body=%s", suffix, resp.StatusCode, raw)
	}
	return raw
}

func TestComposeImportHTTPGuards(t *testing.T) {
	e := newTestEnv(t)
	admin := e.login(t)
	e.seedAgent(t, "import-offline")
	uid := e.createUser(t, admin, "importviewer", "ViewerPass123", "user")
	group := e.createGroup(t, admin, "import-readers")
	for _, r := range []struct{ path, body string }{
		{"/api/groups/" + group.ID + "/members", `{"user_ids":["` + uid + `"]}`},
		{"/api/groups/" + group.ID + "/grants", `{"grants":[{"agent_id":"import-offline","permission":"view"}]}`},
	} {
		rec, _ := e.do(t, http.MethodPut, r.path, r.body, admin)
		if rec.Code != 200 {
			t.Fatalf("grant: %s", rec.Body.String())
		}
	}
	viewer := e.loginAs(t, "importviewer", "ViewerPass123")
	valid := importBody(t, "web", sampleCompose, importTestEnv, composeProjectHash(sampleCompose, importTestEnv), true)
	for _, endpoint := range []string{"import-preview", "import"} {
		path := "/api/agents/import-offline/stacks/" + endpoint
		for _, c := range []struct {
			name, body string
			cookies    []*http.Cookie
			status     int
		}{
			{"anonymous", valid, nil, 401}, {"view is not manage", valid, viewer, 403}, {"invalid name", `{"name":"../bad","content":"services: {}"}`, admin, 400}, {"empty content", `{"name":"web","content":""}`, admin, 400}, {"offline", valid, admin, 503},
		} {
			t.Run(endpoint+"/"+c.name, func(t *testing.T) {
				rec, _ := e.do(t, http.MethodPost, path, c.body, c.cookies)
				if rec.Code != c.status {
					t.Fatalf("status=%d want=%d body=%s", rec.Code, c.status, rec.Body.String())
				}
				assertImportMissing(t, e, "import-offline", "web")
			})
		}
	}
	// Manage, not merely operational control, is required even for preview.
	for _, permission := range []string{"operate", "manage"} {
		grant := `{"grants":[{"agent_id":"import-offline","permission":"` + permission + `"}]}`
		rec, _ := e.do(t, http.MethodPut, "/api/groups/"+group.ID+"/grants", grant, admin)
		if rec.Code != http.StatusOK {
			t.Fatalf("grant %s: %s", permission, rec.Body.String())
		}
		want := http.StatusForbidden
		if permission == "manage" {
			want = http.StatusServiceUnavailable
		}
		for _, endpoint := range []string{"import-preview", "import"} {
			rec, _ = e.do(t, http.MethodPost, "/api/agents/import-offline/stacks/"+endpoint, valid, viewer)
			if rec.Code != want {
				t.Fatalf("%s %s status=%d want=%d body=%s", permission, endpoint, rec.Code, want, rec.Body.String())
			}
		}
	}
}

func TestComposeImportConfirmationAndHashRejectBeforeRPC(t *testing.T) {
	e := newTestEnv(t)
	a := startImportAgent(t, e, nil)
	cookies := e.loginAsFor(t, a.url, "admin", testPassword)
	hash := composeProjectHash(sampleCompose, importTestEnv)
	for _, c := range []struct {
		name, content, env, hash string
		confirm                  bool
	}{
		{"unconfirmed", sampleCompose, importTestEnv, hash, false},
		{"missing hash", sampleCompose, importTestEnv, "", true},
		{"wrong hash", sampleCompose, importTestEnv, "wrong", true},
		{"compose-only hash", sampleCompose, importTestEnv, composeConfigHash(sampleCompose), true},
		{"changed env", sampleCompose, importTestEnv + "NEW=value\n", hash, true},
		{"changed content", sampleCompose + "\n# changed", importTestEnv, hash, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			resp, raw := doHTTP(t, http.MethodPost, a.url+"/api/agents/"+a.id+"/stacks/import", importBody(t, "web", c.content, c.env, c.hash, c.confirm), cookies)
			if resp.StatusCode != 400 {
				t.Fatalf("status=%d body=%s", resp.StatusCode, raw)
			}
			assertImportMissing(t, e, a.id, "web")
		})
	}
	a.assertMethods(t)
}

func TestComposeImportPreviewAndBlockers(t *testing.T) {
	for _, mode := range []string{"allowed", "blockers", "cannot import", "wrong hash", "empty hash"} {
		t.Run(mode, func(t *testing.T) {
			e := newTestEnv(t)
			hash := composeProjectHash(sampleCompose, importTestEnv)
			a := startImportAgent(t, e, func(req envelope.Envelope, _ int) any {
				if req.Method != envelope.MethodComposePreview {
					return nil
				}
				p := envelope.ComposePreviewResult{CanImport: true, ConfigHash: hash, Blockers: []string{}, Warnings: []string{"keep running"}}
				switch mode {
				case "blockers":
					p.Blockers = []string{"unsafe bind mount"}
				case "cannot import":
					p.CanImport = false
				case "wrong hash":
					p.ConfigHash = "wrong"
				case "empty hash":
					p.ConfigHash = ""
				}
				return p
			})
			body := importBody(t, "web", sampleCompose, importTestEnv, hash, true)
			raw := importPost(t, e, a, "import-preview", body)
			var preview struct {
				CanImport  bool   `json:"can_import"`
				ConfigHash string `json:"config_hash"`
			}
			if err := json.Unmarshal(raw, &preview); err != nil {
				t.Fatal(err)
			}
			if preview.CanImport != (mode == "allowed") || preview.ConfigHash != hash {
				t.Fatalf("preview=%s", raw)
			}
			calls := a.requests()
			var payload envelope.ComposePreviewPayload
			if err := envelope.DecodePayload(&calls[0], &payload); err != nil {
				t.Fatal(err)
			}
			if payload.Name != "web" || payload.Content != sampleCompose || payload.EnvContent != importTestEnv {
				t.Fatal("preview did not forward exact compose/env")
			}
			assertImportMissing(t, e, a.id, "web")
			if mode == "allowed" {
				a.assertMethods(t, envelope.MethodComposePreview)
				return
			}
			cookies := e.loginAsFor(t, a.url, "admin", testPassword)
			resp, raw := doHTTP(t, http.MethodPost, a.url+"/api/agents/"+a.id+"/stacks/import", body, cookies)
			if resp.StatusCode != 409 {
				t.Fatalf("blocked import status=%d body=%s", resp.StatusCode, raw)
			}
			assertImportMissing(t, e, a.id, "web")
			a.assertMethods(t, envelope.MethodComposePreview, envelope.MethodComposePreview)
		})
	}
}

func TestComposeImportSuccessPersistsEnvWithoutLifecycleRPC(t *testing.T) {
	e := newTestEnv(t)
	a := startImportAgent(t, e, nil)
	hash := composeProjectHash(sampleCompose, importTestEnv)
	body := importBody(t, "web", sampleCompose, importTestEnv, hash, true)
	raw := importPost(t, e, a, "import", body)
	var result struct {
		SyncOK  bool `json:"sync_ok"`
		Adopted bool `json:"adopted"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatal(err)
	}
	if !result.SyncOK || !result.Adopted {
		t.Fatalf("import=%s", raw)
	}
	a.assertMethods(t, envelope.MethodComposePreview, envelope.MethodComposeAdopt)
	var adopted envelope.ComposeAdoptPayload
	if err := envelope.DecodePayload(&a.requests()[1], &adopted); err != nil {
		t.Fatal(err)
	}
	if adopted.Name != "web" || adopted.Content != sampleCompose || adopted.EnvContent != importTestEnv || !adopted.Confirmation || adopted.ExpectedContentHash != hash {
		t.Fatal("adopt payload not forwarded exactly")
	}
	saved, err := e.db.GetComposeStack(t.Context(), a.id, "web")
	if err != nil || saved.Content != sampleCompose || saved.EnvContent != importTestEnv || saved.ConfigHash != hash || saved.SyncedAt.IsZero() {
		t.Fatalf("saved=%+v err=%v", saved, err)
	}
	cookies := e.loginAsFor(t, a.url, "admin", testPassword)
	for _, suffix := range []string{"", "/web"} {
		resp, raw := doHTTP(t, http.MethodGet, a.url+"/api/agents/"+a.id+"/stacks"+suffix, "", cookies)
		if resp.StatusCode != 200 {
			t.Fatalf("read status=%d body=%s", resp.StatusCode, raw)
		}
		if strings.Contains(string(raw), "env_content") || strings.Contains(string(raw), "import-secret-never-return") {
			t.Fatalf("runtime response leaked .env: %s", raw)
		}
	}
	// Already-synced projects must not be adopted again, even with identical input.
	before := saved
	previewRaw := importPost(t, e, a, "import-preview", body)
	var duplicatePreview struct {
		CanImport bool `json:"can_import"`
	}
	if err := json.Unmarshal(previewRaw, &duplicatePreview); err != nil {
		t.Fatal(err)
	}
	if duplicatePreview.CanImport {
		t.Fatalf("synced record must not be offered for import again: %s", previewRaw)
	}
	resp, raw := doHTTP(t, http.MethodPost, a.url+"/api/agents/"+a.id+"/stacks/import", body, cookies)
	if resp.StatusCode != 409 {
		t.Fatalf("duplicate synced status=%d body=%s", resp.StatusCode, raw)
	}
	after, err := e.db.GetComposeStack(t.Context(), a.id, "web")
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("synced record changed: before=%+v after=%+v err=%v", before, after, err)
	}
	count := 0
	for _, req := range a.requests() {
		if req.Method == envelope.MethodComposeAdopt {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("synced duplicate called adopt %d times", count)
	}
}

func TestComposeImportAdoptFailurePreservesRecordAndRetries(t *testing.T) {
	e := newTestEnv(t)
	a := startImportAgent(t, e, func(req envelope.Envelope, n int) any {
		if req.Method == envelope.MethodComposeAdopt && n == 1 {
			return envelope.NewError(envelope.CodeConflict, "workspace temporarily unavailable")
		}
		return nil
	})
	hash := composeProjectHash(sampleCompose, importTestEnv)
	body := importBody(t, "web", sampleCompose, importTestEnv, hash, true)
	raw := importPost(t, e, a, "import", body)
	var first struct {
		SyncOK bool   `json:"sync_ok"`
		Hint   string `json:"sync_hint"`
	}
	if err := json.Unmarshal(raw, &first); err != nil {
		t.Fatal(err)
	}
	if first.SyncOK || first.Hint == "" {
		t.Fatalf("failed adopt must report unsynced: %s", raw)
	}
	saved, err := e.db.GetComposeStack(t.Context(), a.id, "web")
	if err != nil || saved.EnvContent != importTestEnv || saved.Content != sampleCompose || !saved.SyncedAt.IsZero() {
		t.Fatalf("failed import not preserved: %+v err=%v", saved, err)
	}
	// A failed import is retryable, not an invitation to replace saved input.
	cookies := e.loginAsFor(t, a.url, "admin", testPassword)
	changedEnv := importTestEnv + "NEW=value\n"
	changed := importBody(t, "web", sampleCompose, changedEnv, composeProjectHash(sampleCompose, changedEnv), true)
	resp, rejected := doHTTP(t, http.MethodPost, a.url+"/api/agents/"+a.id+"/stacks/import", changed, cookies)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("different unsynced input status=%d body=%s", resp.StatusCode, rejected)
	}
	unchanged, err := e.db.GetComposeStack(t.Context(), a.id, "web")
	if err != nil || !reflect.DeepEqual(saved, unchanged) {
		t.Fatalf("failed record overwritten: %+v err=%v", unchanged, err)
	}
	raw = importPost(t, e, a, "import-preview", body)
	var preview struct {
		CanImport bool `json:"can_import"`
	}
	if err := json.Unmarshal(raw, &preview); err != nil {
		t.Fatal(err)
	}
	if !preview.CanImport {
		t.Fatalf("same-content unsynced import must be retryable: %s", raw)
	}
	raw = importPost(t, e, a, "import", body)
	var retry struct {
		SyncOK bool `json:"sync_ok"`
	}
	if err := json.Unmarshal(raw, &retry); err != nil {
		t.Fatal(err)
	}
	if !retry.SyncOK {
		t.Fatalf("retry failed: %s", raw)
	}
	after, err := e.db.GetComposeStack(t.Context(), a.id, "web")
	if err != nil || after.ID != saved.ID || after.Version != saved.Version || after.EnvContent != importTestEnv || after.SyncedAt.IsZero() {
		t.Fatalf("retry did not sync original record: %+v err=%v", after, err)
	}
	a.assertMethods(t, envelope.MethodComposePreview, envelope.MethodComposeAdopt, envelope.MethodComposePreview, envelope.MethodComposePreview, envelope.MethodComposePreview, envelope.MethodComposeAdopt)
}

// A successful transport does not prove the Agent adopted the confirmed bytes.
func TestComposeImportAdoptResultHashMustMatch(t *testing.T) {
	for _, tc := range []struct {
		name, hash string
		adopted    bool
	}{
		{"missing hash", "", true},
		{"wrong hash", "different-project-hash", true},
		{"not adopted", composeProjectHash(sampleCompose, importTestEnv), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newTestEnv(t)
			a := startImportAgent(t, e, func(req envelope.Envelope, _ int) any {
				if req.Method == envelope.MethodComposeAdopt {
					return envelope.ComposeAdoptResult{Name: "web", Adopted: tc.adopted, ConfigHash: tc.hash}
				}
				return nil
			})
			raw := importPost(t, e, a, "import", importBody(t, "web", sampleCompose, importTestEnv, composeProjectHash(sampleCompose, importTestEnv), true))
			var result struct {
				SyncOK  bool `json:"sync_ok"`
				Adopted bool `json:"adopted"`
			}
			if err := json.Unmarshal(raw, &result); err != nil {
				t.Fatal(err)
			}
			if result.SyncOK || result.Adopted {
				t.Errorf("unverified adopt result must not report synced/adopted: %s", raw)
			}
			saved, err := e.db.GetComposeStack(t.Context(), a.id, "web")
			if err != nil || saved.EnvContent != importTestEnv || !saved.SyncedAt.IsZero() {
				t.Errorf("hash mismatch must preserve unsynced record: %+v err=%v", saved, err)
			}
			a.assertMethods(t, envelope.MethodComposePreview, envelope.MethodComposeAdopt)
		})
	}
}
