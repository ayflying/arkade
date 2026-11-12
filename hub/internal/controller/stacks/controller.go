package stacks

import (
	"arkade/hub/internal/agentlink"
	"arkade/hub/internal/controller"
	"arkade/hub/internal/httpwire"
	comp "arkade/hub/internal/service/compose"
	"arkade/hub/internal/stackmigrate"
	"arkade/hub/internal/store"
	"arkade/hub/internal/user"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"github.com/gogf/gf/v2/net/ghttp"
	"log/slog"
	"strconv"
	"time"
)

type Deps struct {
	SuppressMonitor func(agentID, containerID string, duration time.Duration)
	DB              *store.DB
	Ops             *agentlink.Ops
	Session         controller.SessionAccessor
	Log             *slog.Logger
	Migrate         *stackmigrate.Manager
	AutoState       func(agent, name string) (status, reason string, recorded bool)
}
type Controller struct {
	d        Deps
	access   controller.Access
	watchers operationWatchers
}

func New(d Deps) *Controller {
	return &Controller{d: d, access: controller.Access{DB: d.DB, Session: d.Session, Log: d.Log}}
}
func (c *Controller) check(ctx context.Context, agent, name string, need user.Permission) error {
	if err := c.access.Require(ctx, agent, need); err != nil {
		return err
	}
	if name != "" {
		if err := comp.ValidateName(name); err != nil {
			return controller.Failure("bad_request", err.Error())
		}
	}
	return nil
}
func (c *Controller) storeError(err error, message string) error {
	switch {
	case errors.Is(err, store.ErrNotFound):
		return controller.Failure("not_found", message)
	case errors.Is(err, store.ErrConflict):
		return controller.Failure("conflict", "名称已被占用，请换一个")
	default:
		c.d.Log.Error("管理接口数据库操作失败", "err", err)
		return controller.Failure("internal", "数据库操作失败").WithErr(err)
	}
}
func decode[T any](ctx context.Context, out *T) error {
	req := httpwire.RestoreRawBody(ctx)
	if req == nil {
		return controller.Failure("bad_request", "请求格式错误: EOF")
	}
	if err := httpwire.DecodeJSON(req, out); err != nil {
		return controller.Failure("bad_request", "请求格式错误: "+err.Error())
	}
	return nil
}
func secret(ctx context.Context) {
	if r := ghttp.RequestFromCtx(ctx); r != nil {
		if r.Response == nil {
			return
		}
		r.Response.Header().Set("Cache-Control", "no-store")
		r.Response.Header().Set("Pragma", "no-cache")
		r.Response.Header().Set("Expires", "0")
	}
}
func randomID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		panic("生成随机 ID 失败: " + err.Error())
	}
	return "stk-" + hex.EncodeToString(b)
}
func cursor(raw string) (int64, error) {
	if raw == "" {
		return 0, nil
	}
	n, e := strconv.ParseInt(raw, 10, 64)
	if e != nil || n < 0 {
		return 0, controller.Failure("bad_request", "输出游标不合法")
	}
	return n, nil
}
