package audit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	api "arkade/hub/api/audit/v1"
	auditlog "arkade/hub/internal/audit"
	"arkade/hub/internal/controller"
	"arkade/hub/internal/store"
	"arkade/hub/internal/user"
)

type testSession struct {
	u  user.User
	ok bool
}

func (s testSession) CurrentUser(context.Context) (user.User, bool) { return s.u, s.ok }
func (testSession) Token(context.Context) string                    { return "" }
func (testSession) SetCookie(context.Context, string, time.Time)    {}
func (testSession) ClearCookie(context.Context)                     {}

func testDB(t *testing.T) *store.DB {
	t.Helper()
	db, err := store.Open(t.Context(), filepath.Join(t.TempDir(), "audit.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func assertError(t *testing.T, err error, status int, code, message string) {
	t.Helper()
	var got *controller.Error
	if !errors.As(err, &got) || got.Status != status || got.Code != code || got.Message != message {
		t.Fatalf("error=%#v; want %d %s %q", err, status, code, message)
	}
}

func TestFiltersExactSemantics(t *testing.T) {
	req := &api.ListReq{UserID: "other", AgentID: " a ", Action: "unknown.action", Result: "success", Limit: "-1", Offset: "+2", From: "-3", To: "2026-01-01T08:00:00+08:00"}
	f, err := parseFilters(req, user.User{ID: "self", Role: user.RoleUser})
	if err != nil {
		t.Fatal(err)
	}
	want := store.AuditFilters{UserID: "self", AgentID: " a ", Action: "unknown.action", Result: "success", Limit: -1, Offset: 2, From: -3, To: 1767225600}
	if f != want {
		t.Fatalf("filters=%+v want %+v", f, want)
	}
	f, err = parseFilters(req, user.User{Role: user.RoleAdmin})
	if err != nil || f.UserID != "other" {
		t.Fatalf("admin filters=%+v err=%v", f, err)
	}
	f, err = parseFilters(&api.ListReq{}, user.User{Role: user.RoleAdmin})
	if err != nil || f != (store.AuditFilters{}) {
		t.Fatalf("defaults=%+v err=%v", f, err)
	}
}

func TestFiltersValidationMessagesAndOrder(t *testing.T) {
	cases := []struct {
		req     api.ListReq
		message string
	}{
		{api.ListReq{Limit: "abc"}, "limit 必须是整数"},
		{api.ListReq{Limit: " 1"}, "limit 必须是整数"},
		{api.ListReq{Limit: "1.5"}, "limit 必须是整数"},
		{api.ListReq{Limit: "999999999999999999999999999999999"}, "limit 必须是整数"},
		{api.ListReq{Offset: "abc"}, "offset 必须是整数"},
		{api.ListReq{Offset: "999999999999999999999999999999999"}, "offset 必须是整数"},
		{api.ListReq{Result: "SUCCESS"}, "result 只能是 success 或 failed"},
		{api.ListReq{From: "bad"}, "from 必须是 Unix 秒或将时间格式"},
		{api.ListReq{To: "bad"}, "to 必须是 Unix 秒或将时间格式"},
		{api.ListReq{From: "9223372036854775808"}, "from 必须是 Unix 秒或将时间格式"},
		{api.ListReq{From: "2026-01-01"}, "from 必须是 Unix 秒或将时间格式"},
		{api.ListReq{Limit: "bad", Offset: "bad", Result: "bad", From: "bad", To: "bad"}, "limit 必须是整数"},
		{api.ListReq{Offset: "bad", Result: "bad", From: "bad", To: "bad"}, "offset 必须是整数"},
		{api.ListReq{Result: "bad", From: "bad", To: "bad"}, "result 只能是 success 或 failed"},
		{api.ListReq{From: "bad", To: "bad"}, "from 必须是 Unix 秒或将时间格式"},
	}
	for _, tc := range cases {
		t.Run(tc.message+fmt.Sprint(tc.req), func(t *testing.T) {
			_, err := parseFilters(&tc.req, user.User{})
			assertError(t, err, 400, "bad_request", tc.message)
		})
	}
}

func TestControllerLoginBeforeParsing(t *testing.T) {
	c := New(nil, testSession{}, nil)
	_, err := c.List(t.Context(), &api.ListReq{Limit: "bad"})
	assertError(t, err, 401, "unauthorized", "请先登录")
	_, err = c.Actions(t.Context(), &api.ActionsReq{})
	assertError(t, err, 401, "unauthorized", "请先登录")
}

func TestControllerListViewsAndScope(t *testing.T) {
	db := testDB(t)
	now := time.Now().UTC().Truncate(time.Second)
	for i, owner := range []string{"self", "other"} {
		if err := db.WriteAudit(auditlog.Record{ID: fmt.Sprint(i), UserID: owner, Username: "name", AgentID: "a", AgentName: "server", Action: "unknown.action", Target: "target", Detail: "detail", Result: "failed", Error: "safe error", DurationMs: 42, CreatedAt: now}); err != nil {
			t.Fatal(err)
		}
	}
	c := New(db, testSession{u: user.User{ID: "self", Role: user.RoleUser}, ok: true}, nil)
	out, err := c.List(t.Context(), &api.ListReq{UserID: "other", AgentID: "a", Action: "unknown.action", Result: "failed", From: fmt.Sprint(now.Unix()), To: now.Format(time.RFC3339)})
	if err != nil {
		t.Fatal(err)
	}
	want := api.Item{ID: "0", UserID: "self", Username: "name", AgentID: "a", AgentName: "server", Action: "unknown.action", ActionLabel: "unknown.action", Target: "target", Detail: "detail", Result: "failed", Error: "safe error", DurationMs: 42, CreatedAt: now.Unix()}
	if out.Total != 1 || len(out.Items) != 1 || out.Items[0] != want {
		t.Fatalf("response=%+v", out)
	}
	admin := New(db, testSession{u: user.User{Role: user.RoleAdmin}, ok: true}, nil)
	out, err = admin.List(t.Context(), &api.ListReq{UserID: "other"})
	if err != nil || out.Total != 1 || out.Items[0].UserID != "other" {
		t.Fatalf("admin=%+v err=%v", out, err)
	}
	out, err = c.List(t.Context(), &api.ListReq{Action: "missing"})
	if err != nil || out.Items == nil || len(out.Items) != 0 {
		t.Fatalf("empty=%+v err=%v", out, err)
	}
	encoded, _ := json.Marshal(out)
	if string(encoded) != `{"total":0,"items":[]}` {
		t.Fatalf("empty JSON=%s", encoded)
	}
	// A canceled query exercises the internal error boundary without mocks.
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err = c.List(ctx, &api.ListReq{})
	assertError(t, err, 500, "internal", "查询操作记录失败")
}

func TestControllerPaginationAndActions(t *testing.T) {
	db := testDB(t)
	now := time.Now().UTC()
	for i := 0; i < 55; i++ {
		if err := db.WriteAudit(auditlog.Record{ID: fmt.Sprintf("%03d", i), UserID: "self", Action: auditlog.ActionUserCreate, Result: auditlog.ResultSuccess, CreatedAt: now}); err != nil {
			t.Fatal(err)
		}
	}
	c := New(db, testSession{u: user.User{Role: user.RoleAdmin}, ok: true}, nil)
	for _, tc := range []struct {
		limit, offset string
		n             int
	}{{"", "", 50}, {"0", "-1", 50}, {"-3", "", 50}, {"9999", "", 55}, {"3", "3", 3}, {"1", "99", 0}} {
		out, err := c.List(t.Context(), &api.ListReq{Limit: tc.limit, Offset: tc.offset})
		if err != nil || out.Total != 55 || len(out.Items) != tc.n {
			t.Fatalf("page %+v response=%+v err=%v", tc, out, err)
		}
		if len(out.Items) > 0 && out.Items[0].ActionLabel != "创建用户" {
			t.Fatal("known action label missing")
		}
	}
	out, err := c.Actions(t.Context(), &api.ActionsReq{})
	if err != nil || out.Actions == nil {
		t.Fatalf("actions=%+v err=%v", out, err)
	}
	seen := map[string]string{}
	for _, a := range out.Actions {
		if _, exists := seen[a.Action]; exists {
			t.Fatal("duplicate action")
		}
		seen[a.Action] = a.Label
	}
	want := map[string]string{}
	for _, a := range auditlog.All() {
		want[a] = auditlog.ActionLabel(a)
	}
	if !reflect.DeepEqual(seen, want) {
		t.Fatalf("actions=%v want %v", seen, want)
	}
}
