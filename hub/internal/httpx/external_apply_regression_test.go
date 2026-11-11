package httpx

import (
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"arkade/protocol/envelope"
)

func TestExternalApplyFullOnlinePathAndHashGuards(t *testing.T) {
	e := newTestEnv(t)
	const name = "apply-path"
	const content = "services:\n  app:\n    image: alpine:3.20\n"
	hash := composeProjectHash(content, "")
	var adopts atomic.Int32
	var conflicting atomic.Bool
	a := startImportAgent(t, e, func(req envelope.Envelope, _ int) any {
		switch req.Method {
		case envelope.MethodComposeList:
			return envelope.ComposeListResult{External: []envelope.ComposeProjectSummary{{Name: name, Status: "running"}}}
		case envelope.MethodComposePreview:
			var p envelope.ComposePreviewPayload
			if err := envelope.DecodePayload(&req, &p); err != nil {
				t.Error(err)
			}
			if p.Name != name || p.Content != content {
				t.Errorf("preview identity/body %+v", p)
			}
			returned := hash
			if conflicting.Load() {
				returned = "different-hash"
			}
			return envelope.ComposePreviewResult{Name: name, CanImport: true, ConfigHash: returned, Blockers: []string{}, Warnings: []string{}}
		case envelope.MethodComposeAdopt:
			var p envelope.ComposeAdoptPayload
			if err := envelope.DecodePayload(&req, &p); err != nil {
				t.Error(err)
			}
			if p.Name != name || p.ExpectedContentHash != hash || !p.Confirmation {
				t.Errorf("adopt %+v", p)
			}
			adopts.Add(1)
			return envelope.ComposeAdoptResult{Name: name, Adopted: true, ConfigHash: hash}
		}
		return nil
	})
	cookies := e.loginAsFor(t, a.url, "admin", testPassword)
	path := a.url + "/api/agents/" + a.id + "/external-stacks/" + name + "/apply?name=query-override&agent_id=missing"
	body := `{"name":"body-override","content":"services:\n  app:\n    image: alpine:3.20\n","expected_content_hash":"` + hash + `","confirmation":true}`
	// Invalid client hash must fail before preview/adopt or any DB write.
	resp, raw := doHTTP(t, http.MethodPost, path, strings.Replace(body, hash, "wrong", 1), cookies)
	if resp.StatusCode != 400 {
		t.Fatalf("client hash %d %s", resp.StatusCode, raw)
	}
	assertImportMissing(t, e, a.id, name)
	if adopts.Load() != 0 {
		t.Fatal("invalid client hash adopted")
	}
	conflicting.Store(true)
	resp, raw = doHTTP(t, http.MethodPost, path, body, cookies)
	if resp.StatusCode != 409 {
		t.Fatalf("remote hash %d %s", resp.StatusCode, raw)
	}
	assertImportMissing(t, e, a.id, name)
	if adopts.Load() != 0 {
		t.Fatal("conflicting preview adopted")
	}
	conflicting.Store(false)
	resp, raw = doHTTP(t, http.MethodPost, path, body, cookies)
	if resp.StatusCode != 200 || !strings.Contains(string(raw), `"adopted":true`) || !strings.Contains(string(raw), `"sync_ok":true`) {
		t.Fatalf("apply %d %s", resp.StatusCode, raw)
	}
	rec, err := e.db.GetComposeStack(t.Context(), a.id, name)
	if err != nil || rec.ConfigHash != hash || rec.SyncedAt.IsZero() {
		t.Fatalf("stored %+v %v", rec, err)
	}
	assertImportMissing(t, e, a.id, "body-override")
	assertImportMissing(t, e, a.id, "query-override")
	if adopts.Load() != 1 {
		t.Fatalf("adopts %d", adopts.Load())
	}
	for _, call := range a.requests() {
		switch call.Method {
		case envelope.MethodComposeList, envelope.MethodComposePreview, envelope.MethodComposeAdopt:
		default:
			t.Fatalf("unsafe RPC %s", call.Method)
		}
	}
}
