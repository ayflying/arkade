// Package audit implements the native GoFrame audit query controller.
package audit

import (
	"context"
	"log/slog"
	"net/http"

	api "arkade/hub/api/audit/v1"
	"arkade/hub/internal/controller"
	"arkade/hub/internal/store"
)

// Controller exposes only typed read methods for group.Bind.
type Controller struct {
	db      *store.DB
	session controller.SessionAccessor
	log     *slog.Logger
}

func New(db *store.DB, session controller.SessionAccessor, log *slog.Logger) *Controller {
	if log == nil {
		log = slog.Default()
	}
	return &Controller{db: db, session: session, log: log}
}

func (c *Controller) List(ctx context.Context, req *api.ListReq) (*api.ListRes, error) {
	u, ok := c.session.CurrentUser(ctx)
	if !ok {
		return nil, loginRequired()
	}
	filters, err := parseFilters(req, u)
	if err != nil {
		return nil, err
	}
	records, total, err := c.db.ListAuditLogs(ctx, filters)
	if err != nil {
		c.log.Error("查询审计日志失败", "err", err)
		return nil, (&controller.Error{Status: http.StatusInternalServerError, Code: "internal", Message: "查询操作记录失败"}).WithErr(err)
	}
	items := make([]api.Item, 0, len(records))
	for _, rec := range records {
		items = append(items, itemView(rec))
	}
	return &api.ListRes{Total: total, Items: items}, nil
}

func (c *Controller) Actions(ctx context.Context, _ *api.ActionsReq) (*api.ActionsRes, error) {
	if _, ok := c.session.CurrentUser(ctx); !ok {
		return nil, loginRequired()
	}
	return &api.ActionsRes{Actions: actionViews()}, nil
}

func loginRequired() *controller.Error {
	return &controller.Error{Status: http.StatusUnauthorized, Code: "unauthorized", Message: "请先登录"}
}
