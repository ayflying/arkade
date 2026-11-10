package httpx

import (
	"context"
	"net/http"
	"testing"
)

func TestNormalizeDirectAddress(t *testing.T) {
	for _, s := range []string{"192.168.50.243", "example.com", "LAN-HOST", "[2001:db8::1]", "2001:db8::1", ""} {
		if _, err := normalizeDirectAddress(s); err != nil {
			t.Errorf("valid %q: %v", s, err)
		}
	}
	for _, s := range []string{"http://host", "host:8080", "a/b", "user@host", "javascript:alert(1)", "a#b", "a?b", "-bad.com", "a..com", "::", "0.0.0.0", "fe80::1%eth0", "a b"} {
		if _, err := normalizeDirectAddress(s); err == nil {
			t.Errorf("invalid accepted %q", s)
		}
	}
}
func TestDirectAddressPatchPersistsAndClears(t *testing.T) {
	e := newTestEnv(t)
	cookies := e.login(t)
	e.seedAgent(t, "direct-test")
	path := "/api/agents/direct-test"
	rec, _ := e.do(t, http.MethodPatch, path, `{"direct_address":" [2001:db8::1] "}`, cookies)
	if rec.Code != 200 {
		t.Fatal(rec.Body.String())
	}
	a, err := e.db.GetAgent(context.Background(), "direct-test")
	if err != nil || a.DirectAddress != "2001:db8::1" {
		t.Fatalf("%+v %v", a, err)
	}
	rec, _ = e.do(t, http.MethodPatch, path, `{"note":"leave address"}`, cookies)
	if rec.Code != 200 {
		t.Fatal(rec.Body.String())
	}
	a, _ = e.db.GetAgent(context.Background(), "direct-test")
	if a.DirectAddress != "2001:db8::1" {
		t.Fatal("omitted field reset")
	}
	rec, _ = e.do(t, http.MethodPatch, path, `{"direct_address":""}`, cookies)
	if rec.Code != 200 {
		t.Fatal(rec.Body.String())
	}
	a, _ = e.db.GetAgent(context.Background(), "direct-test")
	if a.DirectAddress != "" {
		t.Fatal("cannot clear")
	}
}
