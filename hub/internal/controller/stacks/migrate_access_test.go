package stacks

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"arkade/hub/internal/stackmigrate"
	"arkade/hub/internal/store"
	"arkade/hub/internal/user"
)

type accessMigrationBackend struct{}

func (accessMigrationBackend) CanManage(context.Context, string, string) (bool, error) {
	return true, nil
}
func (accessMigrationBackend) AgentOnline(string) bool { return true }
func (accessMigrationBackend) StackExists(context.Context, string, string) (bool, error) {
	return false, nil
}
func (accessMigrationBackend) StackBusy(context.Context, string, string) (bool, error) {
	return false, nil
}
func (accessMigrationBackend) ReadCompose(context.Context, string, string) (string, string, string, error) {
	return "services: {}", "", "hash", nil
}
func (accessMigrationBackend) ImagesOf(context.Context, string, string) ([]string, error) {
	return nil, nil
}
func (accessMigrationBackend) CopyImage(context.Context, string, string, string, func(int64, int64)) error {
	return nil
}
func (accessMigrationBackend) VolumesOf(context.Context, string, string) (stackmigrate.VolumePlan, error) {
	return stackmigrate.VolumePlan{}, nil
}
func (accessMigrationBackend) CopyVolume(context.Context, string, string, string, func(int64, int64)) error {
	return nil
}
func (accessMigrationBackend) ImportCompose(context.Context, string, string, string, string, string) error {
	return nil
}
func (accessMigrationBackend) StartStack(context.Context, string, string) error { return nil }

func TestMigrationFallbackRequiresOwnerAndBothManage(t *testing.T) {
	c, db, session := setup(t)
	ctx := context.Background()
	if err := db.UpsertAgent(ctx, store.AgentRecord{ID: "target", Name: "target"}); err != nil {
		t.Fatal(err)
	}
	actor := user.User{ID: "owner", Username: "migrationowner", Role: user.RoleUser}
	if err := db.CreateUser(ctx, actor); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateGroup(ctx, user.Group{ID: "migration", Name: "migration", CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if err := db.SetGroupMembers(ctx, "migration", []string{actor.ID}); err != nil {
		t.Fatal(err)
	}
	grant := func(agent string, p user.Permission) {
		t.Helper()
		if err := db.SetGroupGrant(ctx, "migration", agent, p); err != nil {
			t.Fatal(err)
		}
	}
	manager := stackmigrate.New(accessMigrationBackend{})
	c.d.Migrate = manager
	t.Cleanup(func() {
		closeCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		defer cancel()
		if err := manager.Close(closeCtx); err != nil {
			t.Error(err)
		}
	})
	snap, err := manager.Submit(ctx, stackmigrate.Options{Source: "agent", Target: "target", StackName: "demo", OwnerID: actor.ID})
	if err != nil {
		t.Fatal(err)
	}
	// Wait on a real manager task: no fabricated snapshot or private-state mutation.
	deadline := time.After(3 * time.Second)
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		snap, err = manager.Get(snap.TaskID, 0)
		if err != nil {
			t.Fatal(err)
		}
		if snap.Status == stackmigrate.StatusSucceeded {
			break
		}
		if snap.Status == stackmigrate.StatusFailed {
			t.Fatal(snap.Error)
		}
		select {
		case <-deadline:
			t.Fatal("task did not complete")
		case <-ticker.C:
		}
	}
	session.u = actor
	grant("agent", user.PermOperate)
	grant("target", user.PermManage)
	_, found, err := c.migrateFallback(ctx, "agent", "demo", snap.TaskID, 0)
	if !found {
		t.Fatal("missing task")
	}
	wantError(t, err, 403, "没有该服务器的操作权限")
	grant("agent", user.PermManage)
	grant("target", user.PermOperate)
	_, _, err = c.migrateFallback(ctx, "agent", "demo", snap.TaskID, 0)
	wantError(t, err, 403, "没有该服务器的操作权限")
	grant("target", user.PermManage)
	session.u = user.User{ID: "other", Role: user.RoleUser}
	_, _, err = c.migrateFallback(ctx, "agent", "demo", snap.TaskID, 0)
	wantError(t, err, 404, "迁移任务不存在或已过期")
	session.u = user.User{ID: "admin", Role: user.RoleAdmin}
	result, found, err := c.migrateFallback(ctx, "agent", "demo", snap.TaskID, 0)
	if err != nil || !found || result.Migration == nil || result.Migration.Status != stackmigrate.StatusSucceeded {
		t.Fatalf("admin result %+v %v %v", result, found, err)
	}
}

func TestMigrationResidualUncertainWire(t *testing.T) {
	snapshot := stackmigrate.Snapshot{TaskID: "migration", Name: "demo", Source: "source", Target: "target", Residual: stackmigrate.Residual{Uncertain: true}}
	raw, err := json.Marshal(MigrateTaskView("source", "demo", snapshot))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"uncertain":true`) {
		t.Fatalf("uncertain residual omitted: %s", raw)
	}
}
