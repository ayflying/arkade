package store

// sessions.go 是登录会话的仓储层。
//
// 安全约定：数据库里只存令牌的 SHA-256 哈希，不存明文令牌。
// 即使数据库文件泄漏，攻击者也无法从中直接得到可用的会话 Cookie。
//
// ============================ 关于 dao 的使用方式 ============================
// 生成的 dao 的 Ctx() 内部固定走 g.DB("default")——也就是 GoFrame 的
// **全局默认实例**。而本包要支持同时打开多个独立库（测试尤其需要），
// 用全局实例会让它们互相污染。
//
// 所以这里只用 dao 提供的**元数据**（表名、列名），查询落在自己的实例上：
//
//	d.gdb.Model(dao.Sessions.Table()).Where(cols.TokenHash, hash)
//
// 写操作统一用生成的 do.Sessions 结构体填充，
// 不用 g.Map：DO 的字段是 any，未赋值的保持 nil，ORM 会自动忽略，
// 因此"只改某个字段"和"插入部分列"都能用同一个结构体表达，
// 且字段名由编译器检查（拼错 g.Map 的键只会在运行时报错）。
//
// 详见 store/dao_binding_test.go。

import (
	"context"

	"github.com/gogf/gf/v2/errors/gerror"

	"arkade/hub/internal/dao"
	"arkade/hub/internal/model/do"
)

// CreateSession 写入一条会话记录。tokenHash 由 auth 层计算后传入。
func (d *DB) CreateSession(ctx context.Context, tokenHash, userID string, createdAt, expiresAt int64) error {
	_, err := d.gdb.Model(dao.Sessions.Table()).Ctx(ctx).Data(do.Sessions{
		TokenHash: tokenHash,
		UserId:    userID,
		CreatedAt: createdAt,
		ExpiresAt: expiresAt,
	}).Insert()
	if err != nil {
		return gerror.Wrap(err, "创建会话失败")
	}
	return nil
}

// SessionUserID 按令牌哈希查会话对应的用户 ID。
// 过期会话视为不存在，同时顺手删除，避免表无限增长。
func (d *DB) SessionUserID(ctx context.Context, tokenHash string, now int64) (string, error) {
	c := dao.Sessions.Columns()

	// 一次取出 user_id 与 expires_at，避免两次查询之间会话被删的竞态。
	one, err := d.gdb.Model(dao.Sessions.Table()).Ctx(ctx).
		Fields(c.UserId, c.ExpiresAt).
		Where(c.TokenHash, tokenHash).
		One()
	if err != nil {
		return "", gerror.Wrap(err, "查询会话失败")
	}
	if one.IsEmpty() {
		return "", ErrNotFound
	}

	if one[c.ExpiresAt].Int64() <= now {
		// 过期即删除：惰性清理，无需额外定时任务。
		_ = d.DeleteSession(ctx, tokenHash)
		return "", ErrNotFound
	}
	return one[c.UserId].String(), nil
}

// DeleteSession 删除单个会话（退出登录）。
func (d *DB) DeleteSession(ctx context.Context, tokenHash string) error {
	if _, err := d.gdb.Model(dao.Sessions.Table()).Ctx(ctx).
		Where(dao.Sessions.Columns().TokenHash, tokenHash).
		Delete(); err != nil {
		return gerror.Wrap(err, "删除会话失败")
	}
	return nil
}

// DeleteExpiredSessions 清理全部过期会话，供启动时或定时任务调用。
func (d *DB) DeleteExpiredSessions(ctx context.Context, now int64) (int64, error) {
	res, err := d.gdb.Model(dao.Sessions.Table()).Ctx(ctx).
		WhereLTE(dao.Sessions.Columns().ExpiresAt, now).
		Delete()
	if err != nil {
		return 0, gerror.Wrap(err, "清理过期会话失败")
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, gerror.Wrap(err, "清理过期会话失败（读取影响行数）")
	}
	return n, nil
}
