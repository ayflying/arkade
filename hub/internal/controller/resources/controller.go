// Package resources implements native GoFrame resource controllers.
package resources

import (
	"log/slog"

	"arkade/hub/internal/agentlink"
	"arkade/hub/internal/controller"
	"arkade/hub/internal/store"
)

type Deps struct {
	DB      *store.DB
	Ops     *agentlink.Ops
	Session controller.SessionAccessor
	Log     *slog.Logger
}
type Controller struct {
	deps   Deps
	access controller.Access
}

func New(d Deps) *Controller {
	return &Controller{deps: d, access: controller.Access{DB: d.DB, Session: d.Session, Log: d.Log}}
}
