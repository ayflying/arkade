package httpx

import (
	"net/http"
	"strings"
	"testing"

	"arkade/protocol/envelope"
)

func TestExternalNativeStrictJSONAfterInspectAndPathIdentity(t *testing.T) {
	e := newTestEnv(t)
	a := externalTestAgent(t, e)
	cookies := e.loginAsFor(t, a.url, "admin", testPassword)
	base := a.url + "/api/agents/" + a.id + "/external-stacks/"
	// A nonexistent external project wins over a malformed request body.
	resp, raw := doHTTP(t, http.MethodPut, base+"missing/draft", `not json`, cookies)
	if resp.StatusCode != http.StatusNotFound || !strings.Contains(string(raw), "引擎中未找到该编排项目") {
		t.Fatalf("inspection/decode order: %d %s", resp.StatusCode, raw)
	}
	// Strict encoding/json rejects scalar coercion after successful inspection.
	resp, raw = doHTTP(t, http.MethodPut, base+"external-test/draft", `{"content":1}`, cookies)
	if resp.StatusCode != http.StatusBadRequest || !strings.Contains(string(raw), "请求格式错误: ") {
		t.Fatalf("strict body: %d %s", resp.StatusCode, raw)
	}
	// Only the first JSON value is decoded, retaining the old DecodeJSON contract.
	resp, raw = doHTTP(t, http.MethodPut, base+"external-test/draft?name=missing", `{"content":"services: {}","source_hash":"first","expected_version":0} ignored suffix`, cookies)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("suffix/path: %d %s", resp.StatusCode, raw)
	}
	draft, err := e.db.GetExternalComposeDraft(t.Context(), a.id, "external-test")
	if err != nil || draft.SourceHash != "first" {
		t.Fatalf("draft path changed: %+v %v", draft, err)
	}
}

func TestExternalNativeApplyPreviewDoesNotAcceptEnvOrDeploy(t *testing.T) {
	e := newTestEnv(t)
	a := externalTestAgent(t, e)
	cookies := e.loginAsFor(t, a.url, "admin", testPassword)
	base := a.url + "/api/agents/" + a.id + "/external-stacks/external-test/apply-preview"
	resp, raw := doHTTP(t, http.MethodPost, base, `{"content":"services: {}","env_content":"PASSWORD=do-not-bind"}`, cookies)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("unknown env accepted: %d %s", resp.StatusCode, raw)
	}
	resp, raw = doHTTP(t, http.MethodPost, base, `{"content":"services: {}"}`, cookies)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("preview: %d %s", resp.StatusCode, raw)
	}
	if resp.Header.Get("Pragma") != "no-cache" || resp.Header.Get("Expires") != "0" {
		t.Fatalf("sensitive cache headers missing: %v", resp.Header)
	}
	assertImportMissing(t, e, a.id, "external-test")
	for _, req := range a.requests() {
		if req.Method != envelope.MethodComposeList && req.Method != envelope.MethodComposePreview {
			t.Fatalf("unexpected lifecycle call %s", req.Method)
		}
	}
}
