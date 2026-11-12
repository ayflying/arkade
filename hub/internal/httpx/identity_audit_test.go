package httpx

import (
	"arkade/hub/internal/audit"
	"arkade/hub/internal/service/oidclogin"
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestCredentialAuditNeverCopiesReflectedError(t *testing.T) {
	writer := &fakeAuditWriter{}
	r := httptest.NewRequest("POST", "/api/settings/oidc-providers?code=CODE&state=STATE", strings.NewReader(`{"client_secret":"SECRET","password":"PASSWORD"}`))
	rec := &statusRecorder{ResponseWriter: httptest.NewRecorder(), status: 400, errorMessage: "SECRET PASSWORD CODE STATE token"}
	writeAuditRecord(writer, discardLogger(), r, rec, time.Millisecond, "")
	rows := writer.snapshot()
	if len(rows) != 1 || rows[0].Result != audit.ResultFailed || rows[0].Error != "操作失败" {
		t.Fatalf("unsafe record: %+v", rows)
	}
	raw, _ := json.Marshal(rows)
	for _, secret := range []string{"SECRET", "PASSWORD", "CODE", "STATE", "token"} {
		if strings.Contains(string(raw), secret) {
			t.Fatalf("secret %s persisted", secret)
		}
	}
}

func TestIdentityAuditHookOutcome(t *testing.T) {
	writer := &fakeAuditWriter{}
	hook := identityAuditHook(writer, discardLogger())
	hook(context.Background(), oidclogin.AuditEvent{Action: "oidc.bind", ProviderID: "p", UserID: "u", Outcome: "failure"})
	hook(context.Background(), oidclogin.AuditEvent{Action: "oidc.login", ProviderID: "p", UserID: "u", Outcome: "success"})
	hook(context.Background(), oidclogin.AuditEvent{Action: "unexpected", Outcome: "success"})
	rows := writer.snapshot()
	if len(rows) != 2 || rows[0].Action != audit.ActionIdentityBind || rows[0].Result != audit.ResultFailed || rows[1].Action != audit.ActionUserLogin || rows[1].Result != audit.ResultSuccess {
		t.Fatalf("events %+v", rows)
	}
}

func TestIdentityAuditRoutes(t *testing.T) {
	for _, c := range []struct{ path, method, action string }{
		{"/api/auth/register", "POST", audit.ActionUserRegister},
		{"/api/account", "PATCH", audit.ActionProfileUpdate},
		{"/api/account/password", "PUT", audit.ActionPasswordChange},
		{"/api/settings", "PUT", audit.ActionSettingsUpdate},
		{"/api/settings/oidc-providers", "POST", audit.ActionProviderCreate},
		{"/api/settings/oidc-providers/{providerID}", "PATCH", audit.ActionProviderUpdate},
		{"/api/settings/oidc-providers/{providerID}", "DELETE", audit.ActionProviderDelete},
		{"/api/settings/oidc-providers/{providerID}/test", "POST", audit.ActionProviderTest},
		{"/api/account/identities/{providerID}/bind", "POST", audit.ActionIdentityBind},
		{"/api/account/identities/{identityID}", "DELETE", audit.ActionIdentityDelete},
	} {
		route := resolveAuditRoute(c.path, c.method, c.path)
		if route.Action != c.action || !audit.Known(route.Action) {
			t.Fatalf("%s %s => %+v", c.method, c.path, route)
		}
	}
	// Never use URL.String or RequestURI when deciding what is safe to audit.
	r := httptest.NewRequest("POST", "/api/account/password?code=private&state=private", nil)
	if !credentialAuditPath(r.URL.Path) {
		t.Fatal("credential path not protected")
	}
	if credentialAuditPath("/api/agents/a/containers/c/start") {
		t.Fatal("unrelated operation suppressed")
	}
	if isMutatingMethod("GET") {
		t.Fatal("callback must use explicit event hook")
	}
}
