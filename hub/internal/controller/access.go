package controller

import (
	"context"
	"log/slog"

	"arkade/hub/internal/store"
	"arkade/hub/internal/user"
)

// Access centralizes the legacy grant decision and its deliberately fail-closed behavior.
type Access struct {
	DB      *store.DB
	Session SessionAccessor
	Log     *slog.Logger
}

func (a Access) Grants(ctx context.Context, actor user.User) []user.GroupGrant {
	if actor.Role == user.RoleAdmin {
		return nil
	}
	grants, err := a.DB.ListUserGrants(ctx, actor.ID)
	if err != nil {
		a.Log.Error("查询用户组授权失败，按无权限处理", "err", err, "user", actor.Username)
		return nil
	}
	return grants
}

func CanAccess(actor user.User, grants []user.GroupGrant, agentID string, need user.Permission) bool {
	if actor.Role == user.RoleAdmin {
		return true
	}
	best := user.PermNone
	for _, grant := range grants {
		if grant.AgentID == agentID && grant.Permission > best {
			best = grant.Permission
		}
	}
	return best.Satisfies(need)
}

func (a Access) Require(ctx context.Context, agentID string, need user.Permission) error {
	if _, err := a.DB.GetAgent(ctx, agentID); err != nil {
		return Failure("not_found", "服务器不存在")
	}
	actor, ok := a.Session.CurrentUser(ctx)
	if !ok {
		return Failure("unauthorized", "登录已失效")
	}
	if !CanAccess(actor, a.Grants(ctx, actor), agentID, need) {
		return Failure("forbidden", "没有该服务器的操作权限")
	}
	return nil
}

func (a Access) Admin(ctx context.Context) (user.User, error) {
	actor, ok := a.Session.CurrentUser(ctx)
	if !ok {
		return user.User{}, Failure("unauthorized", "登录已失效，请重新登录")
	}
	if actor.Role != user.RoleAdmin {
		return user.User{}, Failure("forbidden", "该操作仅管理员可用")
	}
	return actor, nil
}
