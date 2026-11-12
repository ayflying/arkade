package oidclogin

import (
	api "arkade/hub/api/oidclogin/v1"
	base "arkade/hub/internal/controller"
	"arkade/hub/internal/user"
	"context"
	"reflect"
	"testing"
	"time"
)

type noSession struct{}

func (noSession) CurrentUser(context.Context) (user.User, bool) { return user.User{}, false }
func (noSession) Token(context.Context) string                  { return "" }
func (noSession) SetCookie(context.Context, string, time.Time)  {}
func (noSession) ClearCookie(context.Context)                   {}
func TestControllerRouteSurfacesAreSeparated(t *testing.T) {
	for _, tc := range []struct {
		object any
		want   []string
	}{{NewPublic(nil, noSession{}, true), []string{"Callback", "Start"}}, {NewPrivate(nil, noSession{}, true), []string{"Bind", "Delete", "List"}}} {
		ty := reflect.TypeOf(tc.object)
		if ty.NumMethod() != len(tc.want) {
			t.Fatalf("%v exported routes=%d", ty, ty.NumMethod())
		}
		for i, want := range tc.want {
			if ty.Method(i).Name != want {
				t.Fatalf("%v exposes unexpected %s", ty, ty.Method(i).Name)
			}
		}
	}
}
func TestPrivateOperationsRequireUserBeforeService(t *testing.T) {
	c := NewPrivate(nil, noSession{}, true)
	_, err := c.Bind(t.Context(), &api.BindReq{ProviderID: "p"})
	assertUnauthorized(t, err)
	_, err = c.List(t.Context(), &api.ListReq{})
	assertUnauthorized(t, err)
	_, err = c.Delete(t.Context(), &api.DeleteReq{IdentityID: "i"})
	assertUnauthorized(t, err)
}
func assertUnauthorized(t *testing.T, err error) {
	t.Helper()
	actual, ok := err.(*base.Error)
	if !ok || actual.Code != "unauthorized" {
		t.Fatalf("unexpected auth result %v", err)
	}
}
func TestBrowserCookieNamesAreBoundAndOpaque(t *testing.T) {
	if cookieName("state-a") == cookieName("state-b") || cookieName("state-a") != "arkade_oidc_"+cookieName("state-a")[12:] {
		t.Fatal("invalid cookie identity")
	}
}
