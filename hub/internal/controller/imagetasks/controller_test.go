package imagetasks

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	api "arkade/hub/api/imagetasks/v1"
	"arkade/hub/internal/controller"
	"arkade/hub/internal/httpwire"
	"arkade/hub/internal/imagetask"
	"arkade/hub/internal/store"
	"arkade/hub/internal/user"
)

type sessionStub struct {
	actor user.User
	ok    bool
}

func (s *sessionStub) CurrentUser(context.Context) (user.User, bool) { return s.actor, s.ok }
func (*sessionStub) Token(context.Context) string                    { return "" }
func (*sessionStub) SetCookie(context.Context, string, time.Time)    {}
func (*sessionStub) ClearCookie(context.Context)                     {}

type backend struct{}

func (backend) StartPull(context.Context, string, string) (imagetask.RemoteTask, error) {
	return imagetask.RemoteTask{ID: "remote", Status: "succeeded"}, nil
}
func (backend) StartSave(context.Context, string, string) (imagetask.RemoteTask, error) {
	return imagetask.RemoteTask{}, imagetask.ErrBackend
}
func (backend) BeginLoad(context.Context, string, int64, string) (imagetask.RemoteTask, error) {
	return imagetask.RemoteTask{}, imagetask.ErrBackend
}
func (backend) PutChunk(context.Context, string, string, int64, []byte) error { return nil }
func (backend) GetChunk(context.Context, string, string, int64, int) ([]byte, bool, error) {
	return nil, true, nil
}
func (backend) CommitLoad(context.Context, string, string, string) (imagetask.RemoteTask, error) {
	return imagetask.RemoteTask{}, nil
}
func (backend) Status(context.Context, string, string) (imagetask.RemoteTask, error) {
	return imagetask.RemoteTask{ID: "remote", Status: "succeeded"}, nil
}
func (backend) Cancel(context.Context, string, string) error  { return nil }
func (backend) Release(context.Context, string, string) error { return nil }

func setup(t *testing.T) (*Controller, *sessionStub) {
	t.Helper()
	db, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "images.db"))
	if err != nil {
		t.Fatal(err)
	}
	tasks := imagetask.New(backend{})
	t.Cleanup(func() { _ = tasks.Close(); _ = db.Close() })
	for _, id := range []string{"source", "target"} {
		if err := db.UpsertAgent(context.Background(), store.AgentRecord{ID: id, Name: id}); err != nil {
			t.Fatal(err)
		}
	}
	s := &sessionStub{actor: user.User{ID: "owner", Role: user.RoleAdmin}, ok: true}
	return New(Deps{DB: db, Tasks: tasks, Session: s, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}), s
}
func status(t *testing.T, err error, want int) {
	t.Helper()
	var e *controller.Error
	if !errors.As(err, &e) || e.Status != want {
		t.Fatalf("error %v; want %d", err, want)
	}
}

func TestQueryOwnerPermissionsAndCancellation(t *testing.T) {
	c, s := setup(t)
	ctx := context.Background()
	task, err := c.deps.Tasks.Start("pull", "source", "target", "alpine", "owner")
	if err != nil {
		t.Fatal(err)
	}
	list, err := c.List(ctx, &api.ListReq{})
	if err != nil || len(list.Tasks) != 1 {
		t.Fatalf("list %+v %v", list, err)
	}
	list, err = c.List(ctx, &api.ListReq{AgentID: "target"})
	if err != nil || len(list.Tasks) != 1 {
		t.Fatal(list, err)
	}
	list, err = c.List(ctx, &api.ListReq{AgentID: "missing"})
	if err != nil || list.Tasks == nil || len(list.Tasks) != 0 {
		t.Fatal(list, err)
	}
	s.actor = user.User{ID: "other", Role: user.RoleUser}
	_, err = c.Get(ctx, &api.GetReq{TaskID: task.ID})
	status(t, err, 403)
	_, err = c.Cancel(ctx, &api.CancelReq{TaskID: task.ID})
	status(t, err, 403)
	_, err = c.Download(ctx, &api.DownloadReq{TaskID: task.ID})
	status(t, err, 403)
	list, err = c.List(ctx, &api.ListReq{})
	if err != nil || len(list.Tasks) != 0 {
		t.Fatal(list, err)
	}
	// Matching ownership alone is not authorization, and target grants are checked too.
	s.actor.ID = "owner"
	_, err = c.Get(ctx, &api.GetReq{TaskID: task.ID})
	status(t, err, 403)
	// Grant source only: migrated task access still needs target manage.
	if err := c.deps.DB.CreateUser(ctx, s.actor); err != nil {
		t.Fatal(err)
	}
	if err := c.deps.DB.CreateGroup(ctx, user.Group{ID: "g", Name: "images", CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if err := c.deps.DB.SetGroupMembers(ctx, "g", []string{"owner"}); err != nil {
		t.Fatal(err)
	}
	if err := c.deps.DB.SetGroupGrant(ctx, "g", "source", user.PermManage); err != nil {
		t.Fatal(err)
	}
	_, err = c.Get(ctx, &api.GetReq{TaskID: task.ID})
	status(t, err, 403)
	if err := c.deps.DB.SetGroupGrant(ctx, "g", "target", user.PermManage); err != nil {
		t.Fatal(err)
	}
	if _, err = c.Get(ctx, &api.GetReq{TaskID: task.ID}); err != nil {
		t.Fatal(err)
	}
	// Revocation applies to every subsequent poll/cancel/download, not just creation.
	if err := c.deps.DB.DeleteGroupGrant(ctx, "g", "target"); err != nil {
		t.Fatal(err)
	}
	_, err = c.Cancel(ctx, &api.CancelReq{TaskID: task.ID})
	status(t, err, 403)
	_, err = c.Download(ctx, &api.DownloadReq{TaskID: task.ID})
	status(t, err, 403)
	s.actor.Role = user.RoleAdmin
	got, err := c.Get(ctx, &api.GetReq{TaskID: task.ID})
	if err != nil || got.Task.ID != task.ID {
		t.Fatal(got, err)
	}
	_, err = c.Download(ctx, &api.DownloadReq{TaskID: task.ID})
	status(t, err, 400)
	_, err = c.Cancel(ctx, &api.CancelReq{TaskID: task.ID})
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.Get(ctx, &api.GetReq{TaskID: "missing"})
	status(t, err, 404)
	s.ok = false
	_, err = c.List(ctx, &api.ListReq{})
	status(t, err, 401)
}

func TestStartValidationAndStrictDecodedBody(t *testing.T) {
	c, _ := setup(t)
	ctx := context.Background()
	for _, reference := range []string{"", " ", strings.Repeat("a", 513), "bad\r\nref", "bad\x00"} {
		_, err := c.Pull(ctx, &api.PullReq{AgentID: "source", Reference: reference})
		status(t, err, 400)
	}
	for _, req := range []*api.MigrateReq{{AgentID: "source", Reference: "alpine", TargetAgentID: "target"}, {AgentID: "source", Reference: "alpine", TargetAgentID: "source", Confirmation: true}} {
		_, err := c.Migrate(ctx, req)
		status(t, err, 400)
	}
	decoded := &api.PullReq{AgentID: "bad-path", Reference: ""}
	_, err := c.Pull(httpwire.WithDecodedBody(ctx, decoded), &api.PullReq{AgentID: "source", Reference: "valid"})
	status(t, err, 400)
	_, err = c.Export(ctx, &api.ExportReq{AgentID: "source", Reference: ""})
	status(t, err, 400)
}

func TestArchiveSizeAndDeferredHeaders(t *testing.T) {
	for _, tc := range []struct {
		length int64
		header string
		want   int64
		code   int
	}{{4, "4", 4, 0}, {-1, "4", 4, 0}, {0, "", 0, 411}, {4, "3", 0, 400}, {4, "oops", 0, 400}, {-1, "0", 0, 400}, {imagetask.MaxArchiveSize + 1, "", 0, 400}} {
		r := httptest.NewRequest(http.MethodPost, "/", nil)
		r.ContentLength = tc.length
		r.Header.Set("X-Archive-Size", tc.header)
		got, err := archiveSize(r)
		if tc.code != 0 {
			status(t, err, tc.code)
		} else if err != nil || got != tc.want {
			t.Fatal(got, err)
		}
	}
	w := httptest.NewRecorder()
	stream := &archiveWriter{w: w, size: 3}
	if stream.started || w.Header().Get("Content-Type") != "" {
		t.Fatal("premature commitment")
	}
	n, err := stream.Write([]byte("tar"))
	if err != nil || n != 3 {
		t.Fatal(n, err)
	}
	if !stream.started || w.Header().Get("Content-Type") != "application/x-tar" || w.Header().Get("Content-Length") != "3" || w.Header().Get("Cache-Control") != "no-store" || w.Header().Get("Content-Disposition") != `attachment; filename="arkade-image.tar"` {
		t.Fatal(w.Header())
	}
}
