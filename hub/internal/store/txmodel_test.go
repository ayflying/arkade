package store

// Transaction visibility, rollback, sentinel and panic regression tests.

import (
	"arkade/hub/internal/testdb"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/gogf/gf/v2/database/gdb"

	"arkade/hub/internal/dao"
	"arkade/hub/internal/model/do"
)

// TestTxModelUsesTransactionConnection 事务内用 tx.Model 不应死锁。
func TestTxModelUsesTransactionConnection(t *testing.T) {
	ctx := context.Background()
	db, err := Open(ctx, testdb.New(t))
	if err != nil {
		t.Fatalf("打开库失败: %v", err)
	}
	defer func() { _ = db.Close() }()

	// 用一个显式超时兜住：若真的死锁，测试会超时失败而不是永远挂着。
	done := make(chan error, 1)
	go func() {
		done <- db.TxModel(ctx, func(ctx context.Context, tx gdb.TX) error {
			_, ierr := tx.Model(dao.Groups.Table()).Ctx(ctx).Data(do.Groups{Id: "g1", Name: "组一", CreatedAt: 1}).Insert()
			if ierr != nil {
				return ierr
			}
			// 同一事务内再读回来，确认写可见。
			n, cerr := tx.Model(dao.Groups.Table()).Ctx(ctx).Count()
			if cerr != nil {
				return cerr
			}
			if n != 1 {
				return errors.New("事务内读不到刚写入的行")
			}
			return nil
		})
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("TxModel 失败: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("TxModel 超时（疑似死锁）：事务内的 Model 没有走事务连接")
	}

	// 事务提交后数据应当存在。
	n, err := db.gdb.Model(dao.Groups.Table()).Ctx(ctx).Count()
	if err != nil {
		t.Fatalf("统计失败: %v", err)
	}
	if n != 1 {
		t.Fatalf("提交后应有 1 行，实际 %d", n)
	}
}

// TestTxModelRollsBackOnError 回调返回错误必须回滚。
func TestTxModelRollsBackOnError(t *testing.T) {
	ctx := context.Background()
	db, err := Open(ctx, testdb.New(t))
	if err != nil {
		t.Fatalf("打开库失败: %v", err)
	}
	defer func() { _ = db.Close() }()

	wantErr := errors.New("故意失败")

	err = db.TxModel(ctx, func(ctx context.Context, tx gdb.TX) error {
		_, ierr := tx.Model(dao.Groups.Table()).Ctx(ctx).Data(do.Groups{Id: "g1", Name: "组一", CreatedAt: 1}).Insert()
		if ierr != nil {
			return ierr
		}
		return wantErr
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("期望原样返回回调错误，实际 %v", err)
	}

	n, err := db.gdb.Model(dao.Groups.Table()).Ctx(ctx).Count()
	if err != nil {
		t.Fatalf("统计失败: %v", err)
	}
	if n != 0 {
		t.Fatalf("回滚后应为 0 行，实际 %d", n)
	}
}

// TestTxModelPreservesSentinelErrors 哨兵错误必须原样透出。
//
// ============================ 为什么单独测 ============================
// 上层大量依赖 errors.Is(err, ErrNotFound) 来映射 404。
// 若 TxModel 把错误包装一层（fmt.Errorf %w 也不行——那会保留 Is，
// 但若用不带的 %v 就断了），404 会变成 500。
func TestTxModelPreservesSentinelErrors(t *testing.T) {
	ctx := context.Background()
	db, err := Open(ctx, testdb.New(t))
	if err != nil {
		t.Fatalf("打开库失败: %v", err)
	}
	defer func() { _ = db.Close() }()

	err = db.TxModel(ctx, func(ctx context.Context, tx gdb.TX) error {
		return ErrNotFound
	})
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("ErrNotFound 未原样透出，实际 %v", err)
	}
}

// TestTxModelRollsBackOnPanic 回调 panic 时必须回滚且不悬空事务。
//
// ============================ 这条钉住的是官方保证 ============================
// gdb.Transaction 内部 callTxFunc 的 defer 会 recover panic，转成错误后
// 先 Rollback 再返回。也就是说 panic 不会留下未结束的事务。
//
// 旧 Tx 没有这个 defer，panic 时事务会悬空——这正是要迁到 TxModel 的原因之一。
// 本测试确认在当前 gf 版本上这个保证成立。
func TestTxModelRollsBackOnPanic(t *testing.T) {
	ctx := context.Background()
	db, err := Open(ctx, testdb.New(t))
	if err != nil {
		t.Fatalf("打开库失败: %v", err)
	}
	defer func() { _ = db.Close() }()

	// panic 不能被吞掉：调用方（或测试）应该看到它。
	// gdb 把它转成带 CodeInternalPanic 的错误返回，而不是重新抛出。
	func() {
		defer func() {
			// 若 gdb 选择重新抛出，这里会截住；无论哪种方式，
			// 下面的数据断言都必须成立。
			_ = recover()
		}()
		err = db.TxModel(ctx, func(ctx context.Context, tx gdb.TX) error {
			if _, ierr := tx.Model(dao.Groups.Table()).Ctx(ctx).Data(do.Groups{
				Id:        "g1",
				Name:      "组一",
				CreatedAt: 1,
			}).Insert(); ierr != nil {
				return ierr
			}
			panic("事务内故意 panic")
		})
	}()

	// 关键断言：panic 前的写入必须已回滚。
	n, err2 := db.gdb.Model(dao.Groups.Table()).Ctx(ctx).Count()
	if err2 != nil {
		t.Fatalf("统计失败: %v", err2)
	}
	if n != 0 {
		t.Fatalf("panic 后应回滚，实际仍有 %d 行", n)
	}

	// 事务不能悬空：池里的连接必须已归还，否则后续写入会死锁。
	stats := rawDBOf(t, db).Stats()
	if stats.InUse != 0 {
		t.Fatalf("panic 后连接未归还，InUse=%d（事务悬空）", stats.InUse)
	}
}
