package httpx

// authz.go 是 HTTP 层的权限校验适配。
//
// 业务规则（决策 D-04）：
//   - admin 角色对全部服务器拥有最高权限；
//   - 普通用户的权限完全来自所属用户组的 (agent, permission) 授权，取并集；
//   - 未授权一律 403 forbidden（不是 404，也不隐藏服务器存在性——
//     这是内网运维工具，可发现性对排障更重要）。

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"arkade/hub/internal/user"
	"arkade/protocol/envelope"
)

// loadGrants 加载当前用户通过用户组获得的授权列表。
// 查询失败时返回空列表：权限系统异常时应当「什么都做不了」而不是「什么都能做」。
func loadGrants(r *http.Request, d Deps, u user.User) []user.GroupGrant {
	if u.Role == user.RoleAdmin {
		return nil // admin 不需要逐条授权
	}
	grants, err := d.DB.ListGroupGrants(r.Context(), u.ID)
	if err != nil {
		d.Log.Error("查询用户组授权失败，按无权限处理", "err", err, "user", u.Username)
		return nil
	}
	return grants
}

// canAccess 判断用户对指定 Agent 是否满足所需权限档位。
func canAccess(u user.User, grants []user.GroupGrant, agentID string, need user.Permission) bool {
	// 全局管理员直接放行。
	if u.Role == user.RoleAdmin {
		return true
	}
	// 普通用户取所有匹配授权的最高档位。
	best := user.PermNone
	for _, g := range grants {
		if g.AgentID == agentID && g.Permission > best {
			best = g.Permission
		}
	}
	return best.Satisfies(need)
}

// requirePermission 是 handler 内的统一权限闸门。
// 返回 false 表示已写过响应，调用方应立即 return。
func requirePermission(w http.ResponseWriter, d Deps, r *http.Request, agentID string, need user.Permission) bool {
	// 先确认该服务器存在，避免对不存在的 ID 反复查询授权表。
	if _, err := d.DB.GetAgent(r.Context(), agentID); err != nil {
		respondError(w, d.Log, envelope.CodeNotFound, "服务器不存在")
		return false
	}

	u, ok := userFrom(r.Context())
	if !ok {
		respondError(w, d.Log, envelope.CodeUnauthorized, "登录已失效")
		return false
	}

	if !canAccess(u, loadGrants(r, d, u), agentID, need) {
		respondError(w, d.Log, envelope.CodeForbidden, "没有该服务器的操作权限")
		return false
	}
	return true
}

// agentIDFromPath 提取路径中的 Agent ID，供需要独立处理的场景复用。
func agentIDFromPath(r *http.Request) string {
	return chi.URLParam(r, "agentID")
}
