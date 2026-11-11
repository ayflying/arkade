package httpx

// handlers_groups.go 实现用户组管理接口（仅 admin）。
//
// 接口清单：
//   GET    /api/groups                       列出用户组（含成员数、授权数）
//   POST   /api/groups                       创建
//   PATCH  /api/groups/{groupID}             改组名 / 描述
//   DELETE /api/groups/{groupID}             删除
//   GET    /api/groups/{groupID}/members     列出成员
//   PUT    /api/groups/{groupID}/members     整体设置成员
//   GET    /api/groups/{groupID}/grants      列出该组对服务器的授权
//   PUT    /api/groups/{groupID}/grants      整体设置授权
//
// 关于「整体设置」而不是增删单个元素：前端展示的是完整成员/授权列表，
// 两次请求之间别人可能改过，按差集增删会把别人的改动冲掉。整体替换语义确定。

import (
	"net/http"
	"time"


	"arkade/hub/internal/store"
	"arkade/hub/internal/user"
)

// groupView 是返回给前端的用户组视图。
type groupView struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	MemberCount int    `json:"member_count"`
	GrantCount  int    `json:"grant_count"`
	CreatedAt   int64  `json:"created_at"`
}

// toGroupView 转换视图。created_at 用 Unix 秒，与 agents 接口保持一致。
func toGroupView(rec store.GroupRecord) groupView {
	return groupView{
		ID:          rec.ID,
		Name:        rec.Name,
		Description: rec.Description,
		MemberCount: rec.MemberCount,
		GrantCount:  rec.GrantCount,
		CreatedAt:   rec.CreatedAt.Unix(),
	}
}

// handleListGroups 处理 GET /api/groups。
func handleListGroups(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if _, ok := requireAdmin(w, d, r); !ok {
			return
		}
		records, err := d.DB.ListGroups(r.Context())
		if err != nil {
			respondStoreError(w, d, err, "查询用户组列表失败")
			return
		}
		views := make([]groupView, 0, len(records))
		for _, rec := range records {
			views = append(views, toGroupView(rec))
		}
		respondJSON(w, d.Log, http.StatusOK, map[string]any{"groups": views})
	}
}

// createGroupRequest 是创建用户组的请求体。
type createGroupRequest struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

// handleCreateGroup 处理 POST /api/groups。
func handleCreateGroup(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		operator, ok := requireAdmin(w, d, r)
		if !ok {
			return
		}
		var req createGroupRequest
		if !decodeAndValidate(w, d, r, &req) {
			return
		}

		// 组名规整复用 user 包：与用户名不同，组名不转小写（保留用户书写习惯）。
		name := user.NormalizeGroupName(req.Name)
		if err := user.ValidateGroupName(name); err != nil {
			badRequest(w, d, "%s", err.Error())
			return
		}
		if err := user.ValidateDescription(req.Description); err != nil {
			badRequest(w, d, "%s", err.Error())
			return
		}

		grp := user.Group{
			ID:          "g-" + randomSuffix(),
			Name:        name,
			Description: req.Description,
			CreatedAt:   time.Now().UTC(),
		}
		if err := d.DB.CreateGroup(r.Context(), grp); err != nil {
			respondStoreError(w, d, err, "创建用户组失败")
			return
		}
		d.Log.Info("管理员创建了用户组", "operator", operator.Username, "group", grp.Name)
		// 新建组的成员数与授权数必然为 0，直接给出完整视图，前端不必再拉一次列表。
		respondJSON(w, d.Log, http.StatusOK, map[string]any{"group": toGroupView(store.GroupRecord{
			ID: grp.ID, Name: grp.Name, Description: grp.Description, CreatedAt: grp.CreatedAt,
		})})
	}
}

// updateGroupRequest 是修改用户组的请求体（PATCH 语义，字段可选）。
type updateGroupRequest struct {
	Name        optional[string] `json:"name"`
	Description optional[string] `json:"description"`
}

// handleUpdateGroup 处理 PATCH /api/groups/{groupID}。
func handleUpdateGroup(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if _, ok := requireAdmin(w, d, r); !ok {
			return
		}
		groupID := pathParam(r, "groupID")

		var req updateGroupRequest
		if !decodeAndValidate(w, d, r, &req) {
			return
		}
		if !req.Name.Set && !req.Description.Set {
			badRequest(w, d, "没有需要修改的字段")
			return
		}

		upd := store.GroupUpdate{}
		if req.Name.Set {
			name := user.NormalizeGroupName(req.Name.Value)
			if err := user.ValidateGroupName(name); err != nil {
				badRequest(w, d, "%s", err.Error())
				return
			}
			upd.Name = &name
		}
		if req.Description.Set {
			if err := user.ValidateDescription(req.Description.Value); err != nil {
				badRequest(w, d, "%s", err.Error())
				return
			}
			upd.Description = &req.Description.Value
		}

		// 先确认组存在：否则 UpdateGroup 的 RowsAffected==0 无法区分
		// 「组不存在」与「值没变化」（某些驱动对无变更 UPDATE 也返回 0）。
		if _, err := d.DB.GetGroup(r.Context(), groupID); err != nil {
			respondStoreError(w, d, err, "用户组不存在")
			return
		}
		if err := d.DB.UpdateGroup(r.Context(), groupID, upd); err != nil {
			respondStoreError(w, d, err, "用户组不存在")
			return
		}

		grp, err := d.DB.GetGroup(r.Context(), groupID)
		if err != nil {
			respondStoreError(w, d, err, "用户组不存在")
			return
		}
		// 成员数与授权数需要重新统计，直接复用列表查询里同口径的计数方法。
		members, err := d.DB.ListGroupMembers(r.Context(), groupID)
		if err != nil {
			respondStoreError(w, d, err, "查询组成员失败")
			return
		}
		grants, err := d.DB.CountGroupGrants(r.Context(), groupID)
		if err != nil {
			respondStoreError(w, d, err, "统计组授权失败")
			return
		}
		respondJSON(w, d.Log, http.StatusOK, map[string]any{"group": toGroupView(store.GroupRecord{
			ID: grp.ID, Name: grp.Name, Description: grp.Description, CreatedAt: grp.CreatedAt,
			MemberCount: len(members), GrantCount: grants,
		})})
	}
}

// handleDeleteGroup 处理 DELETE /api/groups/{groupID}。
//
// 删除组不会删除成员用户，只会移除成员关系与授权（store.DeleteGroup 负责）。
// 因此这里不需要「最后一个组」之类的保护规则：组只是授权载体。
func handleDeleteGroup(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		operator, ok := requireAdmin(w, d, r)
		if !ok {
			return
		}
		groupID := pathParam(r, "groupID")

		grp, err := d.DB.GetGroup(r.Context(), groupID)
		if err != nil {
			respondStoreError(w, d, err, "用户组不存在")
			return
		}
		if err := d.DB.DeleteGroup(r.Context(), groupID); err != nil {
			respondStoreError(w, d, err, "用户组不存在")
			return
		}
		d.Log.Info("管理员删除了用户组", "operator", operator.Username, "group", grp.Name)
		w.WriteHeader(http.StatusNoContent)
	}
}

// groupMemberView 是组成员视图。
type groupMemberView struct {
	ID          string `json:"id"`
	Username    string `json:"username"`
	DisplayName string `json:"display_name"`
	Role        string `json:"role"`
	Disabled    bool   `json:"disabled"`
}

// handleListGroupMembers 处理 GET /api/groups/{groupID}/members。
func handleListGroupMembers(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if _, ok := requireAdmin(w, d, r); !ok {
			return
		}
		groupID := pathParam(r, "groupID")

		// 先确认组存在，否则「组不存在」会被误报成「组里没有成员」。
		if _, err := d.DB.GetGroup(r.Context(), groupID); err != nil {
			respondStoreError(w, d, err, "用户组不存在")
			return
		}
		members, err := d.DB.ListGroupMembers(r.Context(), groupID)
		if err != nil {
			respondStoreError(w, d, err, "查询组成员失败")
			return
		}
		views := make([]groupMemberView, 0, len(members))
		for _, m := range members {
			views = append(views, groupMemberView{
				ID:          m.ID,
				Username:    m.Username,
				DisplayName: m.DisplayName,
				Role:        string(m.Role),
				Disabled:    m.Disabled,
			})
		}
		respondJSON(w, d.Log, http.StatusOK, map[string]any{
			"group_id": groupID,
			"members":  views,
		})
	}
}

// setMembersRequest 是整体设置成员的请求体。
type setMembersRequest struct {
	UserIDs []string `json:"user_ids"`
}

// handleSetGroupMembers 处理 PUT /api/groups/{groupID}/members。
//
// 校验顺序：组存在 → 用户存在 → 写入。先校验再写，避免出现「删了一半成员后
// 才发现某个 ID 不存在」的中间态（虽然事务会回滚，但先把错误报出来体验更好）。
func handleSetGroupMembers(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if _, ok := requireAdmin(w, d, r); !ok {
			return
		}
		groupID := pathParam(r, "groupID")

		var req setMembersRequest
		if !decodeAndValidate(w, d, r, &req) {
			return
		}
		// 空数组合法（表示清空成员），但字段必须出现——传 null 也按空处理。
		if req.UserIDs == nil {
			req.UserIDs = []string{}
		}

		if _, err := d.DB.GetGroup(r.Context(), groupID); err != nil {
			respondStoreError(w, d, err, "用户组不存在")
			return
		}
		missing, err := d.DB.UsersMissing(r.Context(), req.UserIDs)
		if err != nil {
			respondStoreError(w, d, err, "查询用户是否存在失败")
			return
		}
		if len(missing) > 0 {
			// 返回 400 而不是 404：请求体里的某个 ID 有问题，属于参数错误。
			badRequest(w, d, "以下用户不存在: %v", missing)
			return
		}

		if err := d.DB.SetGroupMembers(r.Context(), groupID, req.UserIDs); err != nil {
			respondStoreError(w, d, err, "用户组不存在")
			return
		}
		// 返回设置后的成员列表，前端可以直接用它刷新界面，省一次请求。
		members, err := d.DB.ListGroupMembers(r.Context(), groupID)
		if err != nil {
			respondStoreError(w, d, err, "查询组成员失败")
			return
		}
		views := make([]groupMemberView, 0, len(members))
		for _, m := range members {
			views = append(views, groupMemberView{
				ID: m.ID, Username: m.Username, DisplayName: m.DisplayName,
				Role: string(m.Role), Disabled: m.Disabled,
			})
		}
		respondJSON(w, d.Log, http.StatusOK, map[string]any{
			"group_id": groupID,
			"members":  views,
		})
	}
}

// groupGrantView 是组授权的对外视图。
//
// permission 用字符串（view/operate/manage），与 user.Permission.String() 一致，
// 前端与数据库里看到的是同一套字面量，不会出现「库里是 view、接口是 1」的错位。
type groupGrantView struct {
	AgentID    string `json:"agent_id"`
	Permission string `json:"permission"`
}

// handleListGroupGrants 处理 GET /api/groups/{groupID}/grants。
func handleListGroupGrants(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if _, ok := requireAdmin(w, d, r); !ok {
			return
		}
		groupID := pathParam(r, "groupID")

		if _, err := d.DB.GetGroup(r.Context(), groupID); err != nil {
			respondStoreError(w, d, err, "用户组不存在")
			return
		}
		grants, err := d.DB.ListGroupGrants(r.Context(), groupID)
		if err != nil {
			respondStoreError(w, d, err, "查询组授权失败")
			return
		}
		views := make([]groupGrantView, 0, len(grants))
		for _, g := range grants {
			views = append(views, groupGrantView{AgentID: g.AgentID, Permission: g.Permission.String()})
		}
		respondJSON(w, d.Log, http.StatusOK, map[string]any{
			"group_id": groupID,
			"grants":   views,
		})
	}
}

// setGrantsRequest 是整体设置授权的请求体。
type setGrantsRequest struct {
	Grants []grantInput `json:"grants"`
}

// grantInput 是单条授权输入。用正常 JSON 对象表达，不做字符串嵌套。
type grantInput struct {
	AgentID    string `json:"agent_id"`
	Permission string `json:"permission"`
}

// handleSetGroupGrants 处理 PUT /api/groups/{groupID}/grants。
//
// 校验要点：
//   - agent_id 必须是非空字符串；
//   - permission 必须是 view/operate/manage（不接受 "none"——要取消授权就该从
//     列表里去掉这条，写入 none 会在表里留下无意义的行）；
//   - agent 是否存在不在 Hub 侧强校验：Agent 可能正处离线/未注册状态，
//     直接拒绝会让「预先给还没上线的服务器配好权限」这件事做不了。
func handleSetGroupGrants(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if _, ok := requireAdmin(w, d, r); !ok {
			return
		}
		groupID := pathParam(r, "groupID")

		var req setGrantsRequest
		if !decodeAndValidate(w, d, r, &req) {
			return
		}
		if req.Grants == nil {
			req.Grants = []grantInput{}
		}

		if _, err := d.DB.GetGroup(r.Context(), groupID); err != nil {
			respondStoreError(w, d, err, "用户组不存在")
			return
		}

		out := make([]user.GroupGrant, 0, len(req.Grants))
		for i, g := range req.Grants {
			if g.AgentID == "" {
				badRequest(w, d, "第 %d 条授权缺少 agent_id", i+1)
				return
			}
			perm := user.ParsePermission(g.Permission)
			// ParsePermission 对未知值降级为 PermNone（读库时的安全默认），
			// 但处理用户输入必须报错，否则打成 "View" 会被静默变成无权限。
			if perm == user.PermNone {
				badRequest(w, d, "第 %d 条授权的 permission 必须是 view、operate 或 manage，实际为 %q", i+1, g.Permission)
				return
			}
			out = append(out, user.GroupGrant{GroupID: groupID, AgentID: g.AgentID, Permission: perm})
		}

		if err := d.DB.SetGroupGrants(r.Context(), groupID, out); err != nil {
			respondStoreError(w, d, err, "用户组不存在")
			return
		}
		saved, err := d.DB.ListGroupGrants(r.Context(), groupID)
		if err != nil {
			respondStoreError(w, d, err, "查询组授权失败")
			return
		}
		views := make([]groupGrantView, 0, len(saved))
		for _, g := range saved {
			views = append(views, groupGrantView{AgentID: g.AgentID, Permission: g.Permission.String()})
		}
		respondJSON(w, d.Log, http.StatusOK, map[string]any{
			"group_id": groupID,
			"grants":   views,
		})
	}
}
