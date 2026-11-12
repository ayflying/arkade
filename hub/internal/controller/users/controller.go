// Package users implements native GoFrame user administration controllers.
package users

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"log/slog"
	"net/http"

	api "arkade/hub/api/users/v1"
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
	return &Controller{db: db, session: session, log: log}
}

func (c *Controller) requireAdmin(ctx context.Context) (user.User, error) {
	u, ok := c.session.CurrentUser(ctx)
	if !ok {
		return user.User{}, failure(http.StatusUnauthorized, "unauthorized", "登录已失效，请重新登录")
	}
	if u.Role != user.RoleAdmin {
		return user.User{}, failure(http.StatusForbidden, "forbidden", "该操作仅管理员可用")
	}
	return u, nil
}

func failure(status int, code, message string) *controller.Error {
	return &controller.Error{Status: status, Code: code, Message: message}
}

func badRequest(err error) error {
	return failure(http.StatusBadRequest, "bad_request", err.Error())
}

func (c *Controller) storeError(err error, notFoundMessage string) error {
	switch {
	case errors.Is(err, store.ErrLastLocalAdmin):
		return failure(http.StatusForbidden, "forbidden", "必须保留至少一个可用的本地密码管理员")
	case errors.Is(err, store.ErrNotFound):
		return failure(http.StatusNotFound, "not_found", notFoundMessage)
	case errors.Is(err, store.ErrConflict):
		return failure(http.StatusConflict, "conflict", "名称已被占用，请换一个")
	default:
		c.log.Error("管理接口数据库操作失败", "err", err)
		return failure(http.StatusInternalServerError, "internal", "数据库操作失败").WithErr(err)
	}
}

func viewOf(u user.User, groupIDs []string) api.UserView {
	if groupIDs == nil {
		groupIDs = []string{}
	}
	return api.UserView{
		ID: u.ID, Username: u.Username, DisplayName: u.DisplayName,
		Role: string(u.Role), Disabled: u.Disabled, MustChangePassword: u.MustChangePassword,
		CreatedAt: u.CreatedAt.Unix(), UpdatedAt: u.UpdatedAt.Unix(), GroupIDs: groupIDs,
	}
}

func groupIDsOf(groups []user.Group) []string {
	ids := make([]string, 0, len(groups))
	for _, group := range groups {
		ids = append(ids, group.ID)
	}
	return ids
}

func newUserID() string {
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		panic("生成随机 ID 失败: " + err.Error())
	}
	return "u-" + hex.EncodeToString(buf)
}
