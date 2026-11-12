package settings

import (
	"arkade/hub/internal/testdb"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	api "arkade/hub/api/settings/v1"
	"arkade/hub/internal/controller"
	"arkade/hub/internal/httpwire"
	service "arkade/hub/internal/service/settings"
	"arkade/hub/internal/store"
	"arkade/hub/internal/user"
	"github.com/gogf/gf/v2/net/ghttp"
)

type session struct{ role user.Role }

func (s *session) CurrentUser(context.Context) (user.User, bool) {
	return user.User{ID: "admin", Role: s.role}, true
}
func (*session) Token(context.Context) string                 { return "" }
func (*session) SetCookie(context.Context, string, time.Time) {}
func (*session) ClearCookie(context.Context)                  {}
func body(text string) context.Context {
	r := httptest.NewRequest("POST", "https://attacker.invalid/", strings.NewReader(text))
	gf := &ghttp.Request{Request: httpwire.HideRawBody(r)}
	return gf.Context()
}
func TestSettingsControllerSecretAndPublicBoundary(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, testdb.New(t))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	svc, err := service.New(db, []byte(strings.Repeat("k", 32)), "https://trusted.example")
	if err != nil {
		t.Fatal(err)
	}
	actor := &session{role: user.RoleAdmin}
	c := New(svc, controller.Access{DB: db, Session: actor, Log: slog.New(slog.NewTextHandler(io.Discard, nil))})
	created, err := c.CreateProvider(body(`{"display_name":"login","issuer":"https://id.example","client_id":"id","client_secret":"never-expose","enabled":true}`), &api.CreateProviderReq{})
	if err != nil {
		t.Fatal(err)
	}
	wire, err := json.Marshal(created)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(wire), "never-expose") || strings.Contains(string(wire), "cipher") || !strings.Contains(string(wire), `"has_client_secret":true`) {
		t.Fatalf("credential boundary %s", wire)
	}
	if created.Provider.CallbackURL != "https://trusted.example/api/auth/oidc/"+created.Provider.ID+"/callback" {
		t.Fatal(created.Provider.CallbackURL)
	}
	opts, err := NewPublic(svc).Options(ctx, &api.OptionsReq{})
	if err != nil || len(opts.Providers) != 1 || !opts.LocalLoginEnabled {
		t.Fatal(opts, err)
	}
	wire, _ = json.Marshal(opts)
	for _, forbidden := range []string{"client_id", "issuer", "auto_register", "default_group_id", "secret"} {
		if strings.Contains(string(wire), forbidden) {
			t.Fatalf("public leaked %s: %s", forbidden, wire)
		}
	}
	actor.role = user.RoleUser
	if _, err := c.Get(ctx, &api.GetReq{}); err == nil {
		t.Fatal("nonadmin read settings")
	}
	if _, err := c.CreateProvider(body("invalid"), &api.CreateProviderReq{}); err == nil {
		t.Fatal("nonadmin created provider")
	}
}
