package httpx

// handlers_users.go 实现用户管理接口（仅 admin）。
//
// 职责边界：
//   - 只做 参数解析 → 规整/校验 → 调仓储 → 序列化视图；
//   - 安全规则（不能删自己、不能动最后一个管理员）在这里判断，
//     因为它们需要「当前操作者是谁」这个 HTTP 层才有的上下文；
//   - 一切响应都经 userAdminView，绝不能直接序列化内部 user.User。
//
// 接口清单：
//   GET    /api/users                     列出用户（含所属组）
//   POST   /api/users                     创建用户
//   PATCH  /api/users/{userID}            改显示名 / 角色 / 禁用
//   PUT    /api/users/{userID}/password   重置密码
//   DELETE /api/users/{userID}            删除用户

import (
	"errors"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"arkade/hub/internal/store"
	"arkade/hub/internal/user"
	"arkade/protocol/envelope"
)

// userAdminView 是管理接口返回的用户视图。
//
// 为什么不复用 handlers_auth.go 的 userView：登录视图刻意只给最小信息，
// 管理视图还需要禁用状态、时间戳与所属组。两者字段集合不同、演进节奏不同，
// 合并成一个结构会让登录接口在不经意间多暴露字段。
//
// PasswordHash 在这里根本没有对应字段——这是最强的一道保险：
// 即使有人误把 user.User 传进来也序列化不出哈希。
type userAdminView struct {
	ID          string   `json:"id"`
	Username    string   `json:"username"`
	DisplayName string   `json:"display_name"`
	Role        string   `json:"role"`
	Disabled    bool     `json:"disabled"`
	CreatedAt   int64    `json:"created_at"`
	UpdatedAt   int64    `json:"updated_at"`
	GroupIDs    []string `json:"group_ids"`
}

// toUserAdminView 转换视图。groupIDs 为空时返回空数组而不是 null，
// 前端可以无条件 .length / .map，不必判空。
func toUserAdminView(u user.User, groupIDs []string) userAdminView {
	if groupIDs == nil {
		groupIDs = []string{}
	}
	return userAdminView{
		ID:          u.ID,
		Username:    u.Username,
		DisplayName: u.DisplayName,
		Role:        string(u.Role),
		Disabled:    u.Disabled,
		CreatedAt:   u.CreatedAt.Unix(),
		UpdatedAt:   u.UpdatedAt.Unix(),
		GroupIDs:    groupIDs,
	}
}

// handleListUsers 处理 GET /api/users。
//
// 所属组用一次 ListUserGroupIDs 批量查出再按用户索引，避免每个用户查一次（N+1）。
func handleListUsers(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if _, ok := requireAdmin(w, d, r); !ok {
			return
		}

		users, err := d.DB.ListUsers(r.Context())
		if err != nil {
			respondStoreError(w, d, err, "查询用户列表失败")
			return
		}
		groupMap, err := d.DB.ListUserGroupIDs(r.Context())
		if err != nil {
			respondStoreError(w, d, err, "查询用户所属组失败")
			return
		}

		views := make([]userAdminView, 0, len(users))
		for _, u := range users {
			views = append(views, toUserAdminView(u, groupMap[u.ID]))
		}
		respondJSON(w, d.Log, http.StatusOK, map[string]any{"users": views})
	}
}

// createUserRequest 是创建用户的请求体。
type createUserRequest struct {
	Username    string `json:"username"`
	DisplayName string `json:"display_name"`
	Password    string `json:"password"`
	Role        string `json:"role"`
}

// handleCreateUser 处理 POST /api/users。
func handleCreateUser(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if _, ok := requireAdmin(w, d, r); !ok {
			return
		}

		var req createUserRequest
		if !decodeAndValidate(w, d, r, &req) {
			return
		}

		// 用户名规整复用 user 包的唯一实现，禁止在这里另写一套大小写规则。
		username := user.NormalizeUsername(req.Username)
		if err := user.ValidateUsername(username); err != nil {
			badRequest(w, d, "%s", err.Error())
			return
		}
		displayName := req.DisplayName
		if displayName == "" {
			// 显示名允许省略，默认与用户名一致：少一个必填项，创建流程更顺。
			displayName = username
		}
		if err := user.ValidateDisplayName(displayName); err != nil {
			badRequest(w, d, "%s", err.Error())
			return
		}
		// 角色必须显式合法：把 admin 误写为 Admin 时应当 400，
		// 而不是像读取脏数据那样静默降级成普通用户。
		role, err := user.ValidRole(req.Role)
		if err != nil {
			badRequest(w, d, "%s", err.Error())
			return
		}
		// 先按策略预检密码（长度/上限），再哈希：能把「参数非法」与「内部故障」
		// 区分开，前者返回 400 而不是 500。
		if err := user.ValidatePassword(req.Password); err != nil {
			badRequest(w, d, "%s", err.Error())
			return
		}
		hash, err := user.HashPassword(req.Password)
		if err != nil {
			respondError(w, d.Log, envelope.CodeInternal, "生成密码哈希失败")
			return
		}

		now := time.Now().UTC()
		u := user.User{
			ID:           "u-" + randomSuffix(),
			Username:     username,
			DisplayName:  displayName,
			PasswordHash: hash,
			Role:         role,
			CreatedAt:    now,
			UpdatedAt:    now,
		}
		if err := d.DB.CreateUser(r.Context(), u); err != nil {
			// 重名由仓储层归一化成 store.ErrConflict → 409。
			respondStoreError(w, d, err, "创建用户失败")
			return
		}
		d.Log.Info("管理员创建了用户", "username", u.Username, "role", string(u.Role))
		respondJSON(w, d.Log, http.StatusOK, map[string]any{"user": toUserAdminView(u, nil)})
	}
}

// updateUserRequest 是修改用户的请求体，字段全部可选（PATCH 语义）。
//
// 注意：这里**不接受 username**。改名会同时影响登录凭据、审计日志与用户记忆，
// 是一项独立且影响面很大的操作；用 DisallowUnknownFields 让传 username 的
// 请求直接 400，比静默忽略更能暴露调用方的误解。
type updateUserRequest struct {
	DisplayName optional[string] `json:"display_name"`
	Role        optional[string] `json:"role"`
	Disabled    optional[bool]   `json:"disabled"`
}

// handleUpdateUser 处理 PATCH /api/users/{userID}。
func handleUpdateUser(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		operator, ok := requireAdmin(w, d, r)
		if !ok {
			return
		}
		targetID := chi.URLParam(r, "userID")

		var req updateUserRequest
		if !decodeAndValidate(w, d, r, &req) {
			return
		}
		if !req.DisplayName.Set && !req.Role.Set && !req.Disabled.Set {
			badRequest(w, d, "没有需要修改的字段")
			return
		}

		current, err := d.DB.GetUserByID(r.Context(), targetID)
		if err != nil {
			respondStoreError(w, d, err, "用户不存在")
			return
		}

		upd := store.UserUpdate{}
		if req.DisplayName.Set {
			if err := user.ValidateDisplayName(req.DisplayName.Value); err != nil {
				badRequest(w, d, "%s", err.Error())
				return
			}
			upd.DisplayName = &req.DisplayName.Value
		}
		if req.Role.Set {
			role, rerr := user.ValidRole(req.Role.Value)
			if rerr != nil {
				badRequest(w, d, "%s", rerr.Error())
				return
			}
			upd.Role = &role
		}
		if req.Disabled.Set {
			disabled := req.Disabled.Value
			upd.Disabled = &disabled
		}

		// 安全规则：不能让「最后一个有效管理员」被降级或禁用。
		// 判定必须在写库之前完成，否则一旦写成功、系统就再没人能改回来（只能改数据库）。
		if err := guardLastAdmin(r, d, current, upd); err != nil {
			respondGuardError(w, d, err)
			return
		}

		if err := d.DB.UpdateUser(r.Context(), targetID, upd, time.Now().UTC().Unix()); err != nil {
			respondStoreError(w, d, err, "用户不存在")
			return
		}
		d.Log.Info("管理员修改了用户", "operator", operator.Username, "target", current.Username,
			"role_changed", req.Role.Set, "disabled_changed", req.Disabled.Set)

		updated, err := d.DB.GetUserByID(r.Context(), targetID)
		if err != nil {
			respondStoreError(w, d, err, "用户不存在")
			return
		}
		groups, err := d.DB.ListGroupsByUser(r.Context(), targetID)
		if err != nil {
			respondStoreError(w, d, err, "查询用户所属组失败")
			return
		}
		respondJSON(w, d.Log, http.StatusOK, map[string]any{"user": toUserAdminView(updated, groupIDsOf(groups))})
	}
}

// guardViolation 表示触发了「保护最后一个管理员」的安全规则。
//
// 单独定义错误类型而不是直接写响应，是为了让规则判定与 HTTP 表达分离：
// 规则本身是纯逻辑（便于单测），怎么回复给用户是 HTTP 层的事。
type guardViolation struct {
	code    string
	message string
}

func (g guardViolation) Error() string { return g.message }

// guardLastAdmin 检查本次更新是否会让系统失去所有有效管理员。
//
// 规则：
//   - 被改的用户当前是「有效管理员」（admin 且未禁用）才可能触发；
//   - 改完之后它不再是有效管理员（降级 或 禁用）；
//   - 且系统里没有别的有效管理员（排除它自己后为 0）。
//
// 允许「自己把自己降级」的唯一例外是存在其他管理员——这条规则对操作者本人
// 也生效，因为「最后一个管理员」无论被谁改掉，结果都是无人可管。
func guardLastAdmin(r *http.Request, d Deps, current user.User, upd store.UserUpdate) error {
	wasActiveAdmin := current.Role == user.RoleAdmin && !current.Disabled
	if !wasActiveAdmin {
		return nil
	}

	willBeActiveAdmin := true
	if upd.Role != nil && *upd.Role != user.RoleAdmin {
		willBeActiveAdmin = false
	}
	if upd.Disabled != nil && *upd.Disabled {
		willBeActiveAdmin = false
	}
	if willBeActiveAdmin {
		return nil
	}

	others, err := d.DB.CountActiveAdmins(r.Context(), current.ID)
	if err != nil {
		return guardViolation{code: envelope.CodeInternal, message: "统计管理员数量失败"}
	}
	if others > 0 {
		return nil
	}
	return guardViolation{
		code:    envelope.CodeForbidden,
		message: "这是当前唯一有效的管理员，不能降级或禁用；请先指定另一位管理员",
	}
}

// respondGuardError 输出安全规则判定的结果。
func respondGuardError(w http.ResponseWriter, d Deps, err error) {
	var gv guardViolation
	if errors.As(err, &gv) {
		respondError(w, d.Log, gv.code, gv.message)
		return
	}
	respondError(w, d.Log, envelope.CodeInternal, "安全检查失败")
}

// resetPasswordRequest 是重置密码的请求体。
type resetPasswordRequest struct {
	Password string `json:"password"`
}

// handleResetPassword 处理 PUT /api/users/{userID}/password。
//
// 注意：管理员重置密码后，目标用户的全部会话会被清除（store.UpdateUserPassword 里做），
// 因此对方需要重新登录。这是刻意的：重置密码通常发生在怀疑账号泄漏时。
func handleResetPassword(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		operator, ok := requireAdmin(w, d, r)
		if !ok {
			return
		}
		targetID := chi.URLParam(r, "userID")

		var req resetPasswordRequest
		if !decodeAndValidate(w, d, r, &req) {
			return
		}
		if err := user.ValidatePassword(req.Password); err != nil {
			badRequest(w, d, "%s", err.Error())
			return
		}
		hash, err := user.HashPassword(req.Password)
		if err != nil {
			respondError(w, d.Log, envelope.CodeInternal, "生成密码哈希失败")
			return
		}

		if err := d.DB.UpdateUserPassword(r.Context(), targetID, hash, time.Now().UTC().Unix()); err != nil {
			respondStoreError(w, d, err, "用户不存在")
			return
		}
		d.Log.Info("管理员重置了用户密码", "operator", operator.Username, "target_id", targetID)
		// 不返回任何密码相关信息，只回 204：无需给前端多余的数据。
		w.WriteHeader(http.StatusNoContent)
	}
}

// handleDeleteUser 处理 DELETE /api/users/{userID}。
func handleDeleteUser(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		operator, ok := requireAdmin(w, d, r)
		if !ok {
			return
		}
		targetID := chi.URLParam(r, "userID")

		// 安全规则 1：不能删除自己。
		// 理由不只是「防手滑」：管理员删除自己会让当前会话立刻失效，
		// 但请求已经执行完毕，用户会看到「操作成功 + 立刻被踢出」的诡异现象。
		// 需要退出的话应当是「退出登录」这个独立动作。
		if targetID == operator.ID {
			respondError(w, d.Log, envelope.CodeForbidden, "不能删除当前登录的账号")
			return
		}

		current, err := d.DB.GetUserByID(r.Context(), targetID)
		if err != nil {
			respondStoreError(w, d, err, "用户不存在")
			return
		}

		// 安全规则 2：不能删除最后一个有效管理员。
		if current.Role == user.RoleAdmin && !current.Disabled {
			others, cerr := d.DB.CountActiveAdmins(r.Context(), current.ID)
			if cerr != nil {
				respondStoreError(w, d, cerr, "统计管理员数量失败")
				return
			}
			if others == 0 {
				respondError(w, d.Log, envelope.CodeForbidden,
					"这是当前唯一有效的管理员，不能删除；请先指定另一位管理员")
				return
			}
		}

		if err := d.DB.DeleteUser(r.Context(), targetID); err != nil {
			respondStoreError(w, d, err, "用户不存在")
			return
		}
		d.Log.Info("管理员删除了用户", "operator", operator.Username, "target", current.Username)
		w.WriteHeader(http.StatusNoContent)
	}
}

// groupIDsOf 从组列表提取 ID，供视图使用。
func groupIDsOf(groups []user.Group) []string {
	out := make([]string, 0, len(groups))
	for _, g := range groups {
		out = append(out, g.ID)
	}
	return out
}
