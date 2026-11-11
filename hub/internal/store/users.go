// Package store 里用户相关的仓储实现。
//
// 文件划分（按职责，不按方法数量）：
//   - users.go     用户自身的读写（含唯一约束冲突归一化）
//   - groups.go    用户组自身 + 成员关系（user_groups）
//   - grants.go    组对服务器的授权（group_agents）
//
// 边界：本包只做持久化与约束冲突翻译，不做权限语义判断
// （例如「最后一个管理员不能降级」属于业务规则，放在 httpx 层）。
package store

import (
	"context"
	"database/sql"
	"errors"

	"github.com/gogf/gf/v2/database/gdb"
	"github.com/gogf/gf/v2/errors/gerror"

	"arkade/hub/internal/dao"
	"arkade/hub/internal/model/do"
	"arkade/hub/internal/model/entity"
	"arkade/hub/internal/user"
)

// ============================ 关于 dao 的使用方式 ============================
// 生成的 dao 的 Ctx() 内部固定走 g.DB("default")（全局默认实例），
// 而本包要支持同时打开多个独立库（测试尤其需要），用全局实例会互相污染。
//
// 所以只用 dao 提供的**元数据**（表名、列名），查询落在自己的实例上。
// 详见 store/dao_binding_test.go。

// userFields 返回用户表要查询的列名，顺序与 scanUser 的 Scan 顺序严格一致。
//
// ============================ 为什么要按顺序列出 ============================
// gdb 的 Struct/Scan 是按**列名映射**的，但 user.User 里有多个字段
// 需要类型转换（disabled 是 bool 但库里是整数，时间字段是时间戳整数）。
// 用显式 Fields + 手写 Scan 能保证转换口径唯一，不依赖 gconv 的隐式规则。
//
// 顺序必须与 scanUser 一致：这类错位一旦出现会非常隐蔽
// （role 读到 display_name 里还不报错）。
func userFields() []string {
	c := dao.Users.Columns()
	return []string{
		c.Id, c.Username, c.DisplayName, c.PasswordHash,
		c.Role, c.Disabled, c.CreatedAt, c.UpdatedAt,
	}
}

// GetUserByUsername 按用户名查用户。登录流程的唯一查询入口。
func (d *DB) GetUserByUsername(ctx context.Context, username string) (user.User, error) {
	var e entity.Users
	err := scanOne(ctx, d.gdb.Model(dao.Users.Table()).Fields(userFields()).
		Where(dao.Users.Columns().Username, username), &e)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return user.User{}, ErrNotFound
		}
		return user.User{}, gerror.Wrap(err, "查询用户失败")
	}
	return userFromEntity(e), nil
}

// GetUserByID 按 ID 查用户。会话校验时使用。
func (d *DB) GetUserByID(ctx context.Context, id string) (user.User, error) {
	var e entity.Users
	err := scanOne(ctx, d.gdb.Model(dao.Users.Table()).Fields(userFields()).
		Where(dao.Users.Columns().Id, id), &e)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return user.User{}, ErrNotFound
		}
		return user.User{}, gerror.Wrap(err, "查询用户失败")
	}
	return userFromEntity(e), nil
}

// userFromEntity 把 entity 行转成领域对象，是单行/多行共用的唯一转换实现。
//
// entity 的时间列是 int（gf 对 INTEGER 一律生成 int），在 64 位平台上
// 与 int64 等价；这里显式转 int64，避免 32 位平台上时间戳被截断。
func userFromEntity(e entity.Users) user.User {
	var u user.User
	u.ID = e.Id
	u.Username = e.Username
	u.DisplayName = e.DisplayName
	u.PasswordHash = e.PasswordHash
	u.Role = user.Role(e.Role)
	u.Disabled = e.Disabled != 0
	u.CreatedAt = unixToTime(int64(e.CreatedAt))
	u.UpdatedAt = unixToTime(int64(e.UpdatedAt))
	return u
}

// usersFromEntities 批量转换，供列表查询使用。
func usersFromEntities(ents []entity.Users) []user.User {
	out := make([]user.User, 0, len(ents))
	for _, e := range ents {
		out = append(out, userFromEntity(e))
	}
	return out
}

// CreateUser 创建用户。调用方必须已完成密码哈希与用户名规整（见 user.NormalizeUsername）。
//
// 用户名重复时返回可被 errors.Is(err, ErrConflict) 识别的错误，上层映射成 409，
// 而不是把驱动的 "UNIQUE constraint failed" 原文甩给前端。
func (d *DB) CreateUser(ctx context.Context, u user.User) error {
	_, err := d.gdb.Model(dao.Users.Table()).Ctx(ctx).Data(do.Users{
		Id:           u.ID,
		Username:     u.Username,
		DisplayName:  u.DisplayName,
		PasswordHash: u.PasswordHash,
		Role:         string(u.Role),
		Disabled:     boolToInt(u.Disabled),
		CreatedAt:    timeToUnix(u.CreatedAt),
		UpdatedAt:    timeToUnix(u.UpdatedAt),
	}).Insert()
	return normalizeExecError(err, "创建用户")
}

// CountUsers 统计用户数量，供「首次启动是否要创建管理员」判断。
func (d *DB) CountUsers(ctx context.Context) (int, error) {
	n, err := d.gdb.Model(dao.Users.Table()).Ctx(ctx).Count()
	if err != nil {
		return 0, gerror.Wrap(err, "统计用户数失败")
	}
	return n, nil
}

// CountActiveAdmins 统计「有效管理员」数量：角色为 admin 且未被禁用。
//
// 这是「不能让最后一个管理员消失」的判定依据。单独成方法而不是让上层
// ListUsers + 自己过滤，是为了让这条规则只有一种实现口径——一旦两处口径不同，
// 就会出现「界面说还有一个管理员、实际删除时却说没有」的矛盾。
//
// excludeID 非空时不计入该用户（用于「如果这次操作会把这个人降级/删除，还剩几个」）。
func (d *DB) CountActiveAdmins(ctx context.Context, excludeID string) (int, error) {
	c := dao.Users.Columns()
	m := d.gdb.Model(dao.Users.Table()).Ctx(ctx).
		Where(c.Role, string(user.RoleAdmin)).
		Where(c.Disabled, 0)
	if excludeID != "" {
		m = m.WhereNot(c.Id, excludeID)
	}
	n, err := m.Count()
	if err != nil {
		return 0, gerror.Wrap(err, "统计管理员数量失败")
	}
	return n, nil
}

// ListUsers 列出全部用户，按创建时间升序（同时间的按 id 兜底，保证顺序稳定）。
//
// 返回带 PasswordHash 的完整模型仅供内部使用：HTTP 层必须用视图结构转换，
// 绝不能直接序列化 user.User（PasswordHash 已有 json:"-" 标签，但不能只靠它兜底）。
func (d *DB) ListUsers(ctx context.Context) ([]user.User, error) {
	c := dao.Users.Columns()
	var ents []entity.Users
	err := scanAll(ctx, d.gdb.Model(dao.Users.Table()).Fields(userFields()).
		Order(c.CreatedAt+" ASC").Order(c.Id+" ASC"), &ents)
	if err != nil {
		return nil, gerror.Wrap(err, "查询用户列表失败")
	}
	return usersFromEntities(ents), nil
}

// UserUpdate 是用户资料的局部更新参数。
//
// 用指针字段表达「本次要不要改这个字段」，而不是传零值。
// 为什么必须这样：空字符串与 false 都是合法输入（清空显示名虽然会被校验拦住，
// 但 disabled=false 是完全合法的「启用」操作），用零值判断会把「不修改」和
// 「改成零值」混为一谈。
type UserUpdate struct {
	DisplayName *string
	Role        *user.Role
	Disabled    *bool
}

// UpdateUser 更新用户的可变字段（显示名 / 角色 / 禁用状态）。
//
// 所有写入都放在一次 UPDATE 里完成，避免「先改角色成功、再改禁用失败」留下半截状态。
// 用户名与密码哈希不在此更新：前者涉及唯一约束与登录记忆，后者有独立的改密流程。
func (d *DB) UpdateUser(ctx context.Context, id string, upd UserUpdate, updatedAt int64) error {
	// 只放本次真正要改的字段：这样并发修改不同字段时不会互相覆盖。
	// DO 的字段是 any，未赋值的保持 nil，ORM 会自动忽略——
	// 所以"本次改哪些字段"直接由赋值与否表达，不需要拼 map。
	data := do.Users{}
	changed := false
	if upd.DisplayName != nil {
		data.DisplayName = *upd.DisplayName
		changed = true
	}
	if upd.Role != nil {
		data.Role = string(*upd.Role)
		changed = true
	}
	if upd.Disabled != nil {
		data.Disabled = boolToInt(*upd.Disabled)
		changed = true
	}
	if !changed {
		// 没有任何变更就不写库：空 UPDATE 会白白推进一个写事务，
		// 在 SQLite 单写连接下等于无谓地串行化其他请求。
		return nil
	}
	data.UpdatedAt = updatedAt

	res, err := d.gdb.Model(dao.Users.Table()).Ctx(ctx).
		Where(dao.Users.Columns().Id, id).
		Data(data).
		Update()
	if err != nil {
		return normalizeExecError(err, "更新用户")
	}
	return requireAffected(res, "更新用户")
}

// UpdateUserPassword 重置密码。passwordHash 必须由 user.HashPassword 生成。
//
// 同时删除该用户的全部会话：改密码的常见动机就是「账号可能泄漏」，
// 若旧会话继续有效，改密就失去了意义。
func (d *DB) UpdateUserPassword(ctx context.Context, id, passwordHash string, updatedAt int64) error {
	return d.TxModel(ctx, func(ctx context.Context, tx gdb.TX) error {
		c := dao.Users.Columns()
		res, err := tx.Model(dao.Users.Table()).Ctx(ctx).
			Where(c.Id, id).
			Data(do.Users{
				PasswordHash: passwordHash,
				UpdatedAt:    updatedAt,
			}).
			Update()
		if err != nil {
			return gerror.Wrap(err, "重置密码失败")
		}
		if err := requireAffected(res, "重置密码"); err != nil {
			return err
		}
		if _, err := tx.Model(dao.Sessions.Table()).Ctx(ctx).
			Where(dao.Sessions.Columns().UserId, id).
			Delete(); err != nil {
			return gerror.Wrap(err, "清理用户会话失败")
		}
		return nil
	})
}

// DeleteUser 删除用户。
//
// 关于级联删除：Open 时已通过 DSN 的 _pragma=foreign_keys(1) 为每条连接开启外键约束
// （store.go 有说明），users 被 user_groups 与 sessions 以 ON DELETE CASCADE 引用，
// 因此关联记录会自动清理。这里仍然显式删一次关联记录，原因是：
//   - 外键开关是连接级状态，若将来有人改 DSN 或换驱动，级联会静默失效并留下孤儿行；
//   - 显式删除是幂等的，与外键同时生效不会冲突，但能把「依赖隐式行为」变成「代码里写明的意图」。
func (d *DB) DeleteUser(ctx context.Context, id string) error {
	return d.TxModel(ctx, func(ctx context.Context, tx gdb.TX) error {
		if _, err := tx.Model(dao.UserGroups.Table()).Ctx(ctx).
			Where(dao.UserGroups.Columns().UserId, id).
			Delete(); err != nil {
			return gerror.Wrap(err, "清理用户组成员关系失败")
		}
		if _, err := tx.Model(dao.Sessions.Table()).Ctx(ctx).
			Where(dao.Sessions.Columns().UserId, id).
			Delete(); err != nil {
			return gerror.Wrap(err, "清理用户会话失败")
		}
		res, err := tx.Model(dao.Users.Table()).Ctx(ctx).
			Where(dao.Users.Columns().Id, id).
			Delete()
		if err != nil {
			return gerror.Wrap(err, "删除用户失败")
		}
		return requireAffected(res, "删除用户")
	})
}

// requireAffected 在影响行数为 0 时返回 ErrNotFound。
//
// ============================ 为什么集中成函数 ============================
// 「更新/删除的 WHERE 没命中」必须翻译成 ErrNotFound，否则上层会把
// 「目标不存在」当成成功，返回 200 却什么也没改。
// 这个判断在十来个方法里重复，集中一处能保证口径一致
// （比如有人忘了判断、或判断成 == -1 之类的）。
func requireAffected(res sql.Result, action string) error {
	n, err := res.RowsAffected()
	if err != nil {
		// 驱动不支持 RowsAffected 时不能假定成功：宁可报错，
		// 也不要让「没删到东西」被当成成功。
		return gerror.Wrapf(err, "%s失败（无法确认影响行数）", action)
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}
