// Package groups implements native GoFrame group-management controllers.
package groups

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/http"

	api "arkade/hub/api/groups/v1"
	"arkade/hub/internal/controller"
	"arkade/hub/internal/store"
	"arkade/hub/internal/user"
)

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

func (c *Controller) requireAdmin(ctx context.Context) (user.User, error) {
	if c.session == nil {
		return user.User{}, &controller.Error{Status: http.StatusUnauthorized, Code: "unauthorized", Message: "登录已失效，请重新登录"}
	}
	u, ok := c.session.CurrentUser(ctx)
	if !ok {
		return user.User{}, &controller.Error{Status: http.StatusUnauthorized, Code: "unauthorized", Message: "登录已失效，请重新登录"}
	}
	if u.Role != user.RoleAdmin {
		return user.User{}, &controller.Error{Status: http.StatusForbidden, Code: "forbidden", Message: "该操作仅管理员可用"}
	}
	return u, nil
}

func badRequest(format string, args ...any) error {
	return &controller.Error{Status: http.StatusBadRequest, Code: "bad_request", Message: fmt.Sprintf(format, args...)}
}

func (c *Controller) storeError(err error, notFound string) error {
	switch {
	case errors.Is(err, store.ErrNotFound):
		return &controller.Error{Status: http.StatusNotFound, Code: "not_found", Message: notFound}
	case errors.Is(err, store.ErrConflict):
		return &controller.Error{Status: http.StatusConflict, Code: "conflict", Message: "名称已被占用，请换一个"}
	default:
		c.log.Error("管理接口数据库操作失败", "err", err)
		return (&controller.Error{Status: http.StatusInternalServerError, Code: "internal", Message: "数据库操作失败"}).WithErr(err)
	}
}

func groupView(rec store.GroupRecord) api.GroupView {
	return api.GroupView{ID: rec.ID, Name: rec.Name, Description: rec.Description,
		MemberCount: rec.MemberCount, GrantCount: rec.GrantCount, CreatedAt: rec.CreatedAt.Unix()}
}

func randomSuffix() string {
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		panic("生成随机 ID 失败: " + err.Error())
	}
	return hex.EncodeToString(buf)
}
