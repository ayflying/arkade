package store

// groups.go 是用户组与「用户-组」成员关系的仓储层。
//
// 与 grants.go 的分工：本文件管「谁属于哪个组」，grants.go 管「这个组能碰哪些服务器」。
// 两者经常一起用，但变更原因不同（组织结构 vs 授权策略），因此分文件维护。
//
// ============================ 哪些查询保留原生 SQL ============================
// 单表 CRUD 统一走 d.gdb.Model(dao.X.Table())，列名来自生成物。
//
// **跨表 JOIN 与聚合计数仍用原生 SQL**，这是有意的：
//   - ListGroups 的 LEFT JOIN + COUNT(DISTINCT) 一次算两个计数，
//     改成 Model 链式要拼大段 Where/LeftJoin 字符串，可读性反而更差，
//     而且 COUNT(DISTINCT) 的「JOIN 放大行数」陷阱在链式写法里更不显眼；
//   - ListGroupMembers / ListGroupsByUser / ListUserGroupIDs 是纯读的多表连接。
//
// 这些查询用 store 自己的 gdb 实例，与 Model 共享同一个连接池，事务语义一致。

import (
	"context"
	"errors"
	"time"

	"github.com/gogf/gf/v2/database/gdb"
	"github.com/gogf/gf/v2/errors/gerror"

	"arkade/hub/internal/dao"
	"arkade/hub/internal/model/do"
	"arkade/hub/internal/model/entity"
	"arkade/hub/internal/user"
)

// GroupRecord 是用户组的一行记录，附带聚合计数。
//
// MemberCount / GrantCount 由单条聚合查询算出，而不是让上层再发两个 COUNT：
// 列表接口一次请求就要展示这两个数字，避免 N+1 查询。
type GroupRecord struct {
	ID          string
	Name        string
	Description string
	CreatedAt   time.Time
	MemberCount int
	GrantCount  int
}

// groupFromEntity 把 entity 行转成 user.Group。
func groupFromEntity(e entity.Groups) user.Group {
	return user.Group{
		ID:          e.Id,
		Name:        e.Name,
		Description: e.Description,
		CreatedAt:   unixToTime(int64(e.CreatedAt)),
	}
}

// CreateGroup 创建用户组。名称重复时返回 ErrConflict（上层映射 409）。
func (d *DB) CreateGroup(ctx context.Context, grp user.Group) error {
	_, err := d.gdb.Model(dao.Groups.Table()).Ctx(ctx).Data(do.Groups{
		Id:          grp.ID,
		Name:        grp.Name,
		Description: grp.Description,
		CreatedAt:   timeToUnix(grp.CreatedAt),
	}).Insert()
	return normalizeExecError(err, "创建用户组")
}

// GetGroup 按 ID 查组。
func (d *DB) GetGroup(ctx context.Context, id string) (user.Group, error) {
	var e entity.Groups
	err := scanOne(ctx, d.gdb.Model(dao.Groups.Table()).Fields(
		dao.Groups.Columns().Id, dao.Groups.Columns().Name,
		dao.Groups.Columns().Description, dao.Groups.Columns().CreatedAt).
		Where(dao.Groups.Columns().Id, id), &e)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return user.Group{}, ErrNotFound
		}
		return user.Group{}, gerror.Wrap(err, "查询用户组失败")
	}
	return groupFromEntity(e), nil
}

// ListGroups 列出全部用户组（含成员数与授权数），按创建时间升序。
//
// 用 LEFT JOIN + COUNT(DISTINCT ...) 一次算出两个计数：
// 若用两个子查询分别算，SQL 会写成两段相关子查询，可读性差且容易在
// 「一组成员但零授权」或反之的情况下漏算（JOIN 会放大行数，必须 DISTINCT）。
//
// 保留原生 SQL 的理由见文件头注释。
func (d *DB) ListGroups(ctx context.Context) ([]GroupRecord, error) {
	const q = `SELECT g.id, g.name, g.description, g.created_at,
	                  COUNT(DISTINCT ug.user_id) AS member_count,
	                  COUNT(DISTINCT ga.agent_id) AS grant_count
	           FROM groups g
	           LEFT JOIN user_groups ug ON ug.group_id = g.id
	           LEFT JOIN group_agents ga ON ga.group_id = g.id
	           GROUP BY g.id, g.name, g.description, g.created_at
	           ORDER BY g.created_at ASC, g.id ASC`
	recs, err := d.gdb.GetAll(ctx, q)
	if err != nil {
		return nil, gerror.Wrap(err, "查询用户组列表失败")
	}

	out := make([]GroupRecord, 0, len(recs))
	for _, r := range recs {
		var rec GroupRecord
		rec.ID = r["id"].String()
		rec.Name = r["name"].String()
		rec.Description = r["description"].String()
		rec.CreatedAt = unixToTime(r["created_at"].Int64())
		rec.MemberCount = r["member_count"].Int()
		rec.GrantCount = r["grant_count"].Int()
		out = append(out, rec)
	}
	return out, nil
}

// GroupUpdate 是用户组的局部更新参数（指针语义见 UserUpdate 的说明）。
type GroupUpdate struct {
	Name        *string
	Description *string
}

// UpdateGroup 更新组名与描述。组名重复返回 ErrConflict。
func (d *DB) UpdateGroup(ctx context.Context, id string, upd GroupUpdate) error {
	data := do.Groups{}
	changed := false
	if upd.Name != nil {
		data.Name = *upd.Name
		changed = true
	}
	if upd.Description != nil {
		data.Description = *upd.Description
		changed = true
	}
	if !changed {
		return nil
	}

	res, err := d.gdb.Model(dao.Groups.Table()).Ctx(ctx).
		Where(dao.Groups.Columns().Id, id).
		Data(data).
		Update()
	if err != nil {
		return normalizeExecError(err, "更新用户组")
	}
	return requireAffected(res, "更新用户组")
}

// DeleteGroup 删除用户组。
//
// 与外键级联的关系同 DeleteUser：显式清理 user_groups 与 group_agents，
// PostgreSQL 同时强制外键。删除组不会删除其成员用户——
// 组只是授权载体，用户是独立实体。
func (d *DB) DeleteGroup(ctx context.Context, id string) error {
	return d.TxModel(ctx, func(ctx context.Context, tx gdb.TX) error {
		if _, err := tx.Model(dao.UserGroups.Table()).Ctx(ctx).
			Where(dao.UserGroups.Columns().GroupId, id).
			Delete(); err != nil {
			return gerror.Wrap(err, "清理组成员关系失败")
		}
		if _, err := tx.Model(dao.GroupAgents.Table()).Ctx(ctx).
			Where(dao.GroupAgents.Columns().GroupId, id).
			Delete(); err != nil {
			return gerror.Wrap(err, "清理组授权失败")
		}
		res, err := tx.Model(dao.Groups.Table()).Ctx(ctx).
			Where(dao.Groups.Columns().Id, id).
			Delete()
		if err != nil {
			return gerror.Wrap(err, "删除用户组失败")
		}
		return requireAffected(res, "删除用户组")
	})
}

// AddUserToGroup 把用户加入组。重复加入视为成功（幂等），不报冲突。
//
// 为什么幂等：整体设置成员的接口会反复下发同一份成员列表，
// 把「已存在」当错误会让前端的重试逻辑变得极其啰嗦。
func (d *DB) AddUserToGroup(ctx context.Context, userID, groupID string) error {
	return d.TxModel(ctx, func(ctx context.Context, tx gdb.TX) error {
		c := dao.UserGroups.Columns()

		// 先查再插，而不是 INSERT OR IGNORE：
		// 后者会把「主键冲突」和「外键失败」一起吞掉，
		// 给不存在的用户/组加成员时会静默成功，留下一堆无处可查的意图。
		exist, err := tx.Model(dao.UserGroups.Table()).Ctx(ctx).
			Where(c.UserId, userID).
			Where(c.GroupId, groupID).
			Count()
		if err != nil {
			return gerror.Wrap(err, "查询组成员关系失败")
		}
		if exist > 0 {
			return nil
		}

		if _, err := tx.Model(dao.UserGroups.Table()).Ctx(ctx).Data(do.UserGroups{
			UserId:  userID,
			GroupId: groupID,
		}).Insert(); err != nil {
			return normalizeExecError(err, "加入用户组")
		}
		return nil
	})
}

// RemoveUserFromGroup 把用户移出组。用户本就不在组里时视为成功。
func (d *DB) RemoveUserFromGroup(ctx context.Context, userID, groupID string) error {
	c := dao.UserGroups.Columns()
	if _, err := d.gdb.Model(dao.UserGroups.Table()).Ctx(ctx).
		Where(c.UserId, userID).
		Where(c.GroupId, groupID).
		Delete(); err != nil {
		return gerror.Wrap(err, "移出用户组失败")
	}
	return nil
}

// SetGroupMembers 原子地把组成员设置成给定集合。
//
// 为什么需要「整体设置」而不是让前端算增删：前端拿到的是旧列表，两次请求之间
// 其他人可能改过成员，按差集增删会把别人的改动冲掉。整体设置在一个事务里
// 先清后插，语义确定且不会出现中间态。
//
// 注意：调用方必须保证 userIDs 里的用户真实存在（httpx 层会先做存在性校验），
// 这里不做校验是为了避免在事务里再跑一轮查询。
func (d *DB) SetGroupMembers(ctx context.Context, groupID string, userIDs []string) error {
	return d.TxModel(ctx, func(ctx context.Context, tx gdb.TX) error {
		// 先确认组存在，避免给不存在的组「塞」成员后返回成功。
		exist, err := tx.Model(dao.Groups.Table()).Ctx(ctx).
			Where(dao.Groups.Columns().Id, groupID).
			Count()
		if err != nil {
			return gerror.Wrap(err, "查询用户组失败")
		}
		if exist == 0 {
			return ErrNotFound
		}

		ugCols := dao.UserGroups.Columns()
		if _, err := tx.Model(dao.UserGroups.Table()).Ctx(ctx).
			Where(ugCols.GroupId, groupID).
			Delete(); err != nil {
			return gerror.Wrap(err, "清空组成员失败")
		}

		// 去重：同一用户在一个组里只能有一条关系（主键约束），
		// 提前去重可以避免把「调用方传了重复 ID」变成 500。
		seen := make(map[string]struct{}, len(userIDs))
		for _, uid := range userIDs {
			if _, dup := seen[uid]; dup {
				continue
			}
			seen[uid] = struct{}{}
			if _, err := tx.Model(dao.UserGroups.Table()).Ctx(ctx).Data(do.UserGroups{
				UserId:  uid,
				GroupId: groupID,
			}).Insert(); err != nil {
				return normalizeExecError(err, "设置组成员")
			}
		}
		return nil
	})
}

// GroupMember 是组成员视图，带上用户基本信息，省得前端再查一次用户表。
type GroupMember struct {
	ID          string
	Username    string
	DisplayName string
	Role        user.Role
	Disabled    bool
}

// ListGroupMembers 列出某个组的成员，按用户名升序（界面顺序稳定）。
func (d *DB) ListGroupMembers(ctx context.Context, groupID string) ([]GroupMember, error) {
	const q = `SELECT u.id, u.username, u.display_name, u.role, u.disabled
	           FROM user_groups ug
	           JOIN users u ON u.id = ug.user_id
	           WHERE ug.group_id = ?
	           ORDER BY u.username ASC`
	recs, err := d.gdb.GetAll(ctx, q, groupID)
	if err != nil {
		return nil, gerror.Wrap(err, "查询组成员失败")
	}

	out := make([]GroupMember, 0, len(recs))
	for _, r := range recs {
		out = append(out, GroupMember{
			ID:          r["id"].String(),
			Username:    r["username"].String(),
			DisplayName: r["display_name"].String(),
			Role:        user.Role(r["role"].String()),
			Disabled:    r["disabled"].Int() != 0,
		})
	}
	return out, nil
}

// ListGroupsByUser 列出某用户所属的组（按组名排序）。
//
// 用户列表接口需要「每个用户属于哪些组」，逐个用户调用会产生 N+1；
// 因此另有 ListUserGroupIDs 供批量场景使用，本方法面向单个用户的详情场景。
func (d *DB) ListGroupsByUser(ctx context.Context, userID string) ([]user.Group, error) {
	const q = `SELECT g.id, g.name, g.description, g.created_at
	           FROM user_groups ug
	           JOIN groups g ON g.id = ug.group_id
	           WHERE ug.user_id = ?
	           ORDER BY g.name ASC`
	recs, err := d.gdb.GetAll(ctx, q, userID)
	if err != nil {
		return nil, gerror.Wrap(err, "查询用户所属组失败")
	}

	out := make([]user.Group, 0, len(recs))
	for _, r := range recs {
		out = append(out, user.Group{
			ID:          r["id"].String(),
			Name:        r["name"].String(),
			Description: r["description"].String(),
			CreatedAt:   unixToTime(r["created_at"].Int64()),
		})
	}
	return out, nil
}

// ListUserGroupIDs 一次性查出「用户 → 组 ID 列表」的映射，供用户列表接口避免 N+1。
//
// 返回 map 而不是切片：调用方按 user ID 直接索引，缺失的用户自然得到 nil。
func (d *DB) ListUserGroupIDs(ctx context.Context) (map[string][]string, error) {
	const q = `SELECT ug.user_id, ug.group_id
	           FROM user_groups ug
	           JOIN groups g ON g.id = ug.group_id
	           ORDER BY ug.user_id ASC, g.name ASC`
	recs, err := d.gdb.GetAll(ctx, q)
	if err != nil {
		return nil, gerror.Wrap(err, "查询用户组关系失败")
	}

	out := make(map[string][]string, len(recs))
	for _, r := range recs {
		uid := r["user_id"].String()
		out[uid] = append(out[uid], r["group_id"].String())
	}
	return out, nil
}

// GroupExists 判断组是否存在，供整体设置接口做前置校验（返回 404 而非 400）。
func (d *DB) GroupExists(ctx context.Context, id string) (bool, error) {
	n, err := d.gdb.Model(dao.Groups.Table()).Ctx(ctx).
		Where(dao.Groups.Columns().Id, id).
		Count()
	if err != nil {
		return false, gerror.Wrap(err, "查询用户组是否存在失败")
	}
	return n > 0, nil
}

// UsersMissing 返回给定 ID 中不存在的那些，供「整体设置成员」做存在性校验。
//
// 返回缺失列表而不是 bool，是为了让错误信息能具体告诉调用方「哪几个用户不存在」，
// 而不是笼统的「参数错误」。去重后逐个查询：入参来自一次 HTTP 请求，
// 数量级是几十个，用 IN (...) 动态拼接反而要处理占位符数量与空列表两种边界。
func (d *DB) UsersMissing(ctx context.Context, ids []string) ([]string, error) {
	missing := make([]string, 0, len(ids))
	seen := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		n, err := d.gdb.Model(dao.Users.Table()).Ctx(ctx).
			Where(dao.Users.Columns().Id, id).
			Count()
		if err != nil {
			return nil, gerror.Wrap(err, "查询用户是否存在失败")
		}
		if n == 0 {
			missing = append(missing, id)
		}
	}
	return missing, nil
}
