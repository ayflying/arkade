package stacks

import (
	api "arkade/hub/api/stacks/v1"
	"arkade/hub/internal/controller"
	"arkade/hub/internal/httpwire"
	"arkade/hub/internal/stackmigrate"
	"arkade/hub/internal/user"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"time"
)

func (c *Controller) Migrate(ctx context.Context, r *api.MigrateReq) (*api.MigrateRes, error) {
	if e := c.check(ctx, r.AgentID, r.Name, user.PermManage); e != nil {
		return nil, e
	}
	var p api.MigrateInput
	raw := httpwire.RestoreRawBody(ctx)
	if raw == nil || raw.Body == nil {
		return nil, controller.Failure("bad_request", "请求内容不是合法的 JSON")
	}
	if e := json.NewDecoder(io.LimitReader(raw.Body, 64<<10)).Decode(&p); e != nil {
		return nil, controller.Failure("bad_request", "请求内容不是合法的 JSON")
	}
	target := strings.TrimSpace(p.TargetAgentID)
	if target == "" {
		return nil, controller.Failure("bad_request", "必须指定目标服务器")
	}
	if target == r.AgentID {
		return nil, controller.Failure("bad_request", "目标服务器不能与源服务器相同")
	}
	if strings.TrimSpace(p.Confirmation) != r.Name {
		return nil, controller.Failure("bad_request", "请确认要迁移的编排名称，确认值必须与编排名一致")
	}
	if e := c.access.Require(ctx, target, user.PermManage); e != nil {
		return nil, e
	}
	if c.d.Migrate == nil {
		return nil, controller.Failure("internal", "迁移功能未启用（服务端未装配迁移编排器）")
	}
	owner := ""
	if u, ok := c.d.Session.CurrentUser(ctx); ok {
		owner = u.ID
	}
	snap, e := c.d.Migrate.Submit(ctx, stackmigrate.Options{StackName: r.Name, Source: r.AgentID, Target: target, IncludeVolumes: p.IncludeVolumes, StartAfter: p.StartAfter, RequestID: p.RequestID, OwnerID: owner})
	if e != nil {
		if errors.Is(e, stackmigrate.ErrTooManyTasks) || errors.Is(e, stackmigrate.ErrPairBusy) || errors.Is(e, stackmigrate.ErrDuplicateRequest) {
			return nil, controller.Failure("conflict", e.Error())
		}
		return nil, controller.Failure("internal", "启动迁移失败")
	}
	secret(ctx)
	controller.Status(ctx, 202)
	return &api.MigrateRes{Task: migrateView(r.AgentID, r.Name, snap)}, nil
}

// MigrateTaskView maps the Hub snapshot to the existing operation-task wire schema.
func MigrateTaskView(agent, name string, s stackmigrate.Snapshot) api.MigrateTask {
	return migrateView(agent, name, s)
}

func migrateView(agent, name string, s stackmigrate.Snapshot) api.MigrateTask {
	out := api.MigrateTask{TaskID: s.TaskID, AgentID: agent, Name: name, Action: "migrate", Status: s.Status, NextCursor: s.NextCursor, Output: make([]api.MigrateEvent, 0, len(s.Events)), Truncated: s.Truncated, Error: s.Error, Migrate: api.MigrationInfo{Source: s.Source, Target: s.Target, IncludeVolumes: s.IncludeVolumes, StartAfter: s.StartAfter, Stage: s.Stage, StageTitle: s.StageTitle, ImageCount: s.ImageCount, VolumeCount: s.VolumeCount, SkippedVolumes: s.SkippedVolumes, BindDirs: s.BindDirs, Residual: api.MigrationResidual{ImagesImported: s.Residual.ImagesImported, VolumesRestored: s.Residual.VolumesRestored, ComposeWritten: s.Residual.ComposeWritten, Started: s.Residual.Started, Uncertain: s.Residual.Uncertain}}}
	for _, e := range s.Events {
		out.Output = append(out.Output, api.MigrateEvent{Seq: e.Seq, Stream: e.Stream, Text: e.Text})
	}
	if !s.StartedAt.IsZero() {
		out.StartedAt = s.StartedAt.UTC().Format(time.RFC3339)
	}
	if !s.FinishedAt.IsZero() {
		out.FinishedAt = s.FinishedAt.UTC().Format(time.RFC3339)
	}
	return out
}
func (c *Controller) migrateFallback(ctx context.Context, agent, name, id string, after int64) (*api.OperationRes, bool, error) {
	if c.d.Migrate == nil {
		return nil, false, nil
	}
	snap, e := c.d.Migrate.Get(id, after)
	if errors.Is(e, stackmigrate.ErrTaskNotFound) {
		return nil, false, nil
	}
	if e != nil {
		return nil, true, controller.Failure("internal", "读取迁移任务失败")
	}
	if snap.Name != name || snap.Source != agent {
		return nil, false, nil
	}
	u, ok := c.d.Session.CurrentUser(ctx)
	if !ok || (snap.OwnerID != "" && snap.OwnerID != u.ID && u.Role != user.RoleAdmin) {
		return nil, true, controller.Failure("not_found", "迁移任务不存在或已过期")
	}
	// 迁移日志跨越两台服务器，不能只凭源端 operate 权限读取目标信息。
	if e := c.access.Require(ctx, snap.Source, user.PermManage); e != nil {
		return nil, true, e
	}
	if e := c.access.Require(ctx, snap.Target, user.PermManage); e != nil {
		return nil, true, e
	}
	secret(ctx)
	v := migrateView(agent, name, snap)
	return &api.OperationRes{Migration: &v}, true, nil
}
