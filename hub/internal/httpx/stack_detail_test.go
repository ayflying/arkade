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

func TestUnifiedStackDetailExternalSharesRoute(t *testing.T) {
	e := newTestEnv(t)
	a := externalTestAgent(t, e)
	cookies := e.loginAsFor(t, a.url, "admin", testPassword)
	resp, body := doHTTP(t, http.MethodGet, a.url+"/api/agents/"+a.id+"/stacks/external-test", "", cookies)
	if resp.StatusCode != 200 || !strings.Contains(string(body), `"managed":false`) || !strings.Contains(string(body), `"id":"mine"`) || strings.Contains(string(body), `"id":"other"`) {
		t.Fatalf("%d %s", resp.StatusCode, body)
	}
	assertImportMissing(t, e, a.id, "external-test")
	a.assertMethods(t, envelope.MethodComposeList, envelope.MethodContainerList)
}

func TestUnifiedStackDetailManagedOfflinePreservesMetadata(t *testing.T) {
	e := newTestEnv(t)
	cookies := e.login(t)
	e.seedAgent(t, "offline-unified")
	err := e.db.CreateComposeStack(context.Background(), store.ComposeStack{ID: "unified-1", AgentID: "offline-unified", Name: "saved", Content: sampleCompose, ConfigHash: "hash", Version: 1, CreatedAt: time.Now(), UpdatedAt: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	resp, _ := e.do(t, http.MethodGet, "/api/agents/offline-unified/stacks/saved", "", cookies)
	text := resp.Body.String()
	if resp.Code != 200 || !strings.Contains(text, `"managed":true`) || !strings.Contains(text, `"containers_error":`) || !strings.Contains(text, `"containers":[]`) {
		t.Fatalf("%d %s", resp.Code, text)
	}
	if strings.Contains(text, "services:") {
		t.Fatal("view leaked config")
	}
}

func TestUnifiedStackDetailMissingOnline(t *testing.T) {
	e := newTestEnv(t)
	a := externalTestAgent(t, e)
	cookies := e.loginAsFor(t, a.url, "admin", testPassword)
	resp, body := doHTTP(t, http.MethodGet, a.url+"/api/agents/"+a.id+"/stacks/missing", "", cookies)
	if resp.StatusCode != 404 {
		t.Fatalf("%d %s", resp.StatusCode, body)
	}
	a.assertMethods(t, envelope.MethodComposeList)
}
