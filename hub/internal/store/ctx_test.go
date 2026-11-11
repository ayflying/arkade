package store

// ctx_test.go 守住两条与 ctx 有关的约束：
//   - scanOne/scanAll 必须把 ctx 传到 m.Ctx(ctx)，否则取消信号丢失；
//   - 事务内的 Model 查询必须留在事务连接上，不能回到连接池。
//
// 第二条是单连接池下的硬约束：一旦事务内的查询去池里取连接，
// 池里唯一连接被事务占着，就会永久挂起。

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gogf/gf/v2/database/gdb"

	"arkade/hub/internal/user"
)

// TestScanRespectsCanceledContext 已取消的 ctx 必须让查询立刻返回。
//
// 若 scanOne/scanAll 没有把 ctx 传给 Model（例如写成 m.One()），
// ctx 完全被忽略，查询会照常执行并成功返回——这里就抓得到。
func TestScanRespectsCanceledContext(t *testing.T) {
	ctx := context.Background()
	d, err := Open(ctx, filepath.Join(t.TempDir(), "ctx.db"))
	if err != nil {
		t.Fatalf("打开库失败: %v", err)
	}
	defer d.Close()

	if err := d.CreateUser(ctx, mkUser("u1", "alice", "admin")); err != nil {
		t.Fatalf("准备数据失败: %v", err)
	}

	canceled, cancel := context.WithCancel(ctx)
	cancel()

	// scanOne 路径：GetUserByID 内部走 scanOne。
	if _, err := d.GetUserByID(canceled, "u1"); err == nil {
		t.Fatal("已取消的 ctx 仍查询成功，说明 ctx 没有传到 Model")
	} else if !isCancelErr(err) {
		t.Fatalf("期望取消类错误，实际: %v", err)
	}

	// scanAll 路径：ListUsers 内部走 scanAll。
	if _, err := d.ListUsers(canceled); err == nil {
		t.Fatal("已取消的 ctx 仍查询成功（scanAll 路径）")
	} else if !isCancelErr(err) {
		t.Fatalf("期望取消类错误（scanAll），实际: %v", err)
	}
}

// isCancelErr 判断错误链里是否含 context 取消语义。
//
// 优先用 errors.Is（gerror 与 fmt.Errorf("%w") 都会保持 Is 链）；
// 驱动偶尔返回自定义包装，故再用文案兜一层。
func isCancelErr(err error) bool {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	msg := err.Error()
	return strings.Contains(msg, "context canceled") ||
		strings.Contains(msg, "context deadline exceeded") ||
		strings.Contains(msg, "context.Canceled")
}

// TestTxCRUDStaysOnTransactionConnection 事务内 CRUD 不回到连接池。
//
// 单连接池下，若事务内的查询另取连接，就会与事务争抢唯一连接而挂起。
// 每个操作都带超时，让"挂起"表现为失败。
func TestTxCRUDStaysOnTransactionConnection(t *testing.T) {
	ctx := context.Background()
	d, err := Open(ctx, filepath.Join(t.TempDir(), "txconn.db"))
	if err != nil {
		t.Fatalf("打开库失败: %v", err)
	}
	defer d.Close()

	if err := d.CreateUser(ctx, mkUser("u1", "alice", "admin")); err != nil {
		t.Fatalf("准备用户失败: %v", err)
	}
	if err := d.CreateGroup(ctx, user.Group{ID: "g1", Name: "group-one"}); err != nil {
		t.Fatalf("准备用户组失败: %v", err)
	}

	done := make(chan error, 1)
	go func() {
		done <- d.TxModel(ctx, func(ctx context.Context, tx gdb.TX) error {
			// 全用仓储方法，覆盖 users / groups / user_groups 三张表的事务内读写。
			if _, err := d.GetUserByID(ctx, "u1"); err != nil {
				return err
			}
			if _, err := d.GetGroup(ctx, "g1"); err != nil {
				return err
			}
			if _, err := d.ListUsers(ctx); err != nil {
				return err
			}
			if _, err := tx.Model("user_groups").Where("user_id", "u1").Count(); err != nil {
				return err
			}
			return nil
		})
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("事务内 CRUD 失败: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("事务内 CRUD 挂起（查询回到了连接池）")
	}
}
