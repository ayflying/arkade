package users

import (
	"context"
	"net/http"
	"time"

	api "arkade/hub/api/users/v1"
	"arkade/hub/internal/controller"
	"arkade/hub/internal/httpwire"
	accountservice "arkade/hub/internal/service/account"
	"arkade/hub/internal/store"
	"arkade/hub/internal/user"
)

func (c *Controller) Create(ctx context.Context, req *api.CreateReq) (*api.CreateRes, error) {
	// Keep encoding/json's first-value and case-insensitive field semantics.
	if decoded, ok := httpwire.DecodedBody[api.CreateReq](ctx); ok {
		body := *decoded
		req = &body
	}
	if _, err := c.requireAdmin(ctx); err != nil {
		return nil, err
	}
	username := user.NormalizeUsername(req.Username)
	if err := user.ValidateUsername(username); err != nil {
		return nil, badRequest(err)
	}
	displayName := req.DisplayName
	if displayName == "" {
		displayName = username
	}
	if err := user.ValidateDisplayName(displayName); err != nil {
		return nil, badRequest(err)
	}
	role, err := user.ValidRole(req.Role)
	if err != nil {
		return nil, badRequest(err)
	}
	settings, err := c.db.GetSystemSettings(ctx)
	if err != nil {
		return nil, c.storeError(err, "安全策略不可用")
	}
	if err := accountservice.PasswordPolicy(req.Password, settings.PasswordMinLength); err != nil {
		return nil, badRequest(err)
	}
	hash, err := user.HashPassword(req.Password)
	if err != nil {
		return nil, failure(http.StatusInternalServerError, "internal", "生成密码哈希失败").WithErr(err)
	}
	now := time.Now().UTC()
	u := user.User{
		ID: newUserID(), Username: username, DisplayName: displayName,
		PasswordHash: hash, Role: role, MustChangePassword: true, CreatedAt: now, UpdatedAt: now,
	}
	if err := c.db.CreateUser(ctx, u); err != nil {
		return nil, c.storeError(err, "创建用户失败")
	}
	c.log.Info("管理员创建了用户", "username", u.Username, "role", string(u.Role))
	return &api.CreateRes{User: viewOf(u, nil)}, nil
}

func (c *Controller) Update(ctx context.Context, req *api.UpdateReq) (*api.UpdateRes, error) {
	// GF's converter skips nil; retain the strict JSON decode's explicit null
	// fields, but trust only the framework-bound path for the target identity.
	if decoded, ok := httpwire.DecodedBody[api.UpdateReq](ctx); ok {
		body := *decoded
		body.UserID = req.UserID
		req = &body
	}
	operator, err := c.requireAdmin(ctx)
	if err != nil {
		return nil, err
	}
	if !req.DisplayName.Set && !req.Role.Set && !req.Disabled.Set {
		return nil, failure(http.StatusBadRequest, "bad_request", "没有需要修改的字段")
	}
	current, err := c.db.GetUserByID(ctx, req.UserID)
	if err != nil {
		return nil, c.storeError(err, "用户不存在")
	}
	upd := store.UserUpdate{}
	if req.DisplayName.Set {
		if err := user.ValidateDisplayName(req.DisplayName.Value); err != nil {
			return nil, badRequest(err)
		}
		upd.DisplayName = &req.DisplayName.Value
	}
	if req.Role.Set {
		role, err := user.ValidRole(req.Role.Value)
		if err != nil {
			return nil, badRequest(err)
		}
		upd.Role = &role
	}
	if req.Disabled.Set {
		disabled := req.Disabled.Value
		upd.Disabled = &disabled
	}
	if err := c.guardLastAdmin(ctx, current, upd); err != nil {
		return nil, err
	}
	if err := c.db.UpdateUser(ctx, req.UserID, upd, time.Now().UTC().Unix()); err != nil {
		return nil, c.storeError(err, "用户不存在")
	}
	c.log.Info("管理员修改了用户", "operator", operator.Username, "target", current.Username,
		"role_changed", req.Role.Set, "disabled_changed", req.Disabled.Set)
	updated, err := c.db.GetUserByID(ctx, req.UserID)
	if err != nil {
		return nil, c.storeError(err, "用户不存在")
	}
	groups, err := c.db.ListGroupsByUser(ctx, req.UserID)
	if err != nil {
		return nil, c.storeError(err, "查询用户所属组失败")
	}
	return &api.UpdateRes{User: viewOf(updated, groupIDsOf(groups))}, nil
}

func (c *Controller) guardLastAdmin(ctx context.Context, current user.User, upd store.UserUpdate) error {
	if current.Role != user.RoleAdmin || current.Disabled {
		return nil
	}
	removesAdmin := (upd.Role != nil && *upd.Role != user.RoleAdmin) || (upd.Disabled != nil && *upd.Disabled)
	if !removesAdmin {
		return nil
	}
	others, err := c.db.CountActiveAdmins(ctx, current.ID)
	if err != nil {
		return failure(http.StatusInternalServerError, "internal", "统计管理员数量失败").WithErr(err)
	}
	if others == 0 {
		return failure(http.StatusForbidden, "forbidden", "这是当前唯一有效的管理员，不能降级或禁用；请先指定另一位管理员")
	}
	return nil
}

func (c *Controller) ResetPassword(ctx context.Context, req *api.ResetPasswordReq) (*api.ResetPasswordRes, error) {
	if decoded, ok := httpwire.DecodedBody[api.ResetPasswordReq](ctx); ok {
		body := *decoded
		body.UserID = req.UserID
		req = &body
	}
	operator, err := c.requireAdmin(ctx)
	if err != nil {
		return nil, err
	}
	settings, err := c.db.GetSystemSettings(ctx)
	if err != nil {
		return nil, c.storeError(err, "安全策略不可用")
	}
	if err := accountservice.PasswordPolicy(req.Password, settings.PasswordMinLength); err != nil {
		return nil, badRequest(err)
	}
	hash, err := user.HashPassword(req.Password)
	if err != nil {
		return nil, failure(http.StatusInternalServerError, "internal", "生成密码哈希失败").WithErr(err)
	}
	if err := c.db.UpdateUserPassword(ctx, req.UserID, hash, time.Now().UTC().Unix()); err != nil {
		return nil, c.storeError(err, "用户不存在")
	}
	c.log.Info("管理员重置了用户密码", "operator", operator.Username, "target_id", req.UserID)
	return nil, controller.ErrNoContent
}

func (c *Controller) Delete(ctx context.Context, req *api.DeleteReq) (*api.DeleteRes, error) {
	operator, err := c.requireAdmin(ctx)
	if err != nil {
		return nil, err
	}
	if req.UserID == operator.ID {
		return nil, failure(http.StatusForbidden, "forbidden", "不能删除当前登录的账号")
	}
	current, err := c.db.GetUserByID(ctx, req.UserID)
	if err != nil {
		return nil, c.storeError(err, "用户不存在")
	}
	if current.Role == user.RoleAdmin && !current.Disabled {
		others, err := c.db.CountActiveAdmins(ctx, current.ID)
		if err != nil {
			return nil, c.storeError(err, "统计管理员数量失败")
		}
		if others == 0 {
			return nil, failure(http.StatusForbidden, "forbidden", "这是当前唯一有效的管理员，不能删除；请先指定另一位管理员")
		}
	}
	if err := c.db.DeleteUser(ctx, req.UserID); err != nil {
		return nil, c.storeError(err, "用户不存在")
	}
	c.log.Info("管理员删除了用户", "operator", operator.Username, "target", current.Username)
	return nil, controller.ErrNoContent
}
