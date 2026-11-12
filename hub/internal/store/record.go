package store

// record.go 集中放"把数据库行转成领域对象"的公共手段。
//
// 统一 Scan 到 entity 的原因：早先按列名取值（rec[c.Id].String()）时，
// 类型由取值方法决定而非表结构，列名拼错和字段漏读都只表现为零值、不报错。
// Scan 到结构体后类型由结构体固定，笔误在赋值处即可见。
// 边界：这**不会**让编译器发现 SQL 漏列或类型不匹配，漏读的列仍然是零值。
//
// 零行必须守 ErrNotFound：gdb 查无此行时不报错，只把结构体留成零值。
// 若直接返回，上层会拿到 ID 为空串的"记录"，存在性判断全部失效。
// 所以规则是**先判有无行，再 Scan**——走 scanOne 就不会漏。
//
// 事务内不要用 Fields()：它要查表元数据，而元数据走连接池不走事务连接，
// 饱和连接池下会等待其它事务；Open 预热元数据避免额外连接竞争。

import (
	"context"

	"github.com/gogf/gf/v2/database/gdb"
	"github.com/gogf/gf/v2/errors/gerror"
)

// scanOne 执行单行查询并 Scan 到 dest。
//
// 返回 ErrNotFound 表示没有匹配行（而不是返回错误或零值结构体）。
// 其它错误一律包装后返回，调用方可用 errors.Is(err, ErrNotFound) 判断。
//
// 这里必须传 ctx 给 m.Ctx(ctx)：否则非事务调用方传入的 ctx
// （超时、取消、trace）全部丢失，查询无法被取消。
// 对事务内的 Model 也是安全的：gdb 的 Model.Ctx 会 clone 出新 Model
// 并保留 tx 绑定（`if m.tx != nil { model.tx = model.tx.Ctx(ctx) }`），
// 不会把查询甩回连接池。
func scanOne(ctx context.Context, m *gdb.Model, dest any) error {
	one, err := m.Ctx(ctx).One()
	if err != nil {
		return gerror.Wrap(err, "查询记录失败")
	}
	if one.IsEmpty() {
		return ErrNotFound
	}
	if err := one.Struct(dest); err != nil {
		return gerror.Wrap(err, "解析记录失败")
	}
	return nil
}

// scanAll 执行多行查询并 Scan 到 dest（dest 必须是指针的切片）。
//
// 空结果集是正常情况，不返回 ErrNotFound：调用方拿到空切片即可。
func scanAll(ctx context.Context, m *gdb.Model, dest any) error {
	recs, err := m.Ctx(ctx).All()
	if err != nil {
		return gerror.Wrap(err, "查询记录列表失败")
	}
	if len(recs) == 0 {
		return nil
	}
	if err := recs.Structs(dest); err != nil {
		return gerror.Wrap(err, "解析记录列表失败")
	}
	return nil
}
