package httpx

import (
	"context"
	"net/http"
	"testing"
)

func TestNormalizeDirectAddress(t *testing.T) {
	// 2026-11 起必须是带 http:// / https:// 前缀的完整地址（协议和端口由用户配置）
	for _, s := range []string{
		"https://ac8b255bfcf6-0.aynastv.5ddd.com", "http://10.0.0.12:9000",
		"https://example.com/", "HTTPS://Example.COM", "",
		"https://[2001:db8::1]", "http://192.168.50.243",
	} {
		if _, err := normalizeDirectAddress(s); err != nil {
			t.Errorf("valid %q: %v", s, err)
		}
	}
	for _, s := range []string{
		// 不带协议前缀：不再是完整地址
		"192.168.50.243", "example.com", "LAN-HOST", "[2001:db8::1]", "2001:db8::1",
		"host:8080", "a/b", "user@host", "javascript:alert(1)", "a#b", "a?b",
		"-bad.com", "a..com", "::", "0.0.0.0", "fe80::1%eth0", "a b",
		"https://", "https://user@host", "https://host/path", "https://host?x=1",
		"https://host:70000", "https://host:abc", "ftp://host", "https://[bad",
	} {
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
	// 2026-11 起必须是带协议的完整地址；IPv6 用方括号
	rec, _ := e.do(t, http.MethodPatch, path, `{"direct_address":" HTTPS://[2001:0DB8:0:0::1] "}`, cookies)
	if rec.Code != 200 {
		t.Fatal(rec.Body.String())
	}
	a, err := e.db.GetAgent(context.Background(), "direct-test")
	if err != nil || a.DirectAddress != "https://[2001:db8::1]" {
		t.Fatalf("%+v %v", a, err)
	}
	rec, _ = e.do(t, http.MethodPatch, path, `{"note":"leave address"}`, cookies)
	if rec.Code != 200 {
		t.Fatal(rec.Body.String())
	}
	a, _ = e.db.GetAgent(context.Background(), "direct-test")
	if a.DirectAddress != "https://[2001:db8::1]" {
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
