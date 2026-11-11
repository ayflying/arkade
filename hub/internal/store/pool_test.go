package store

// pool_test.go：验证连接池在使用后不会遗留连接。
//
// ============================ 为什么需要这个测试 ============================
// 连接池 MaxOpen=1。如果某个 API 走了*另一条*连接（而不是池里的唯一那条），
// 或者取出的连接没有归还，Windows 上会表现为
// "测试结束后 TempDir 里的 db 文件删不掉"——因为句柄还开着。
//
// 这类泄漏在 Linux 上症状轻微（可以删除正在使用的文件），
// 所以只在 Linux 跑测试会漏掉。本章直接检查池状态。

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"arkade/hub/internal/dao"
	"arkade/hub/internal/user"
)

// TestPoolHasNoLeakedConnections 常规读写之后，池里不应残留占用中的连接。
//
// 判断方式：OpenConns/InUse 是 database/sql 的统计。
// 读操作结束后 InUse 必须回到 0；否则说明有 rows 没关、或事务没结束。
func TestPoolHasNoLeakedConnections(t *testing.T) {
	ctx := context.Background()
	db, err := Open(ctx, filepath.Join(t.TempDir(), "pool.db"))
	if err != nil {
		t.Fatalf("打开库失败: %v", err)
	}
	defer func() { _ = db.Close() }()

	// 覆盖各类访问路径：gdb.Model 读、原生 SQL 读（rows）、事务写。
	if err := db.CreateGroup(ctx, user.Group{ID: "g1", Name: "组一", CreatedAt: time.Now()}); err != nil {
		t.Fatalf("建组失败: %v", err)
	}
	if _, err := db.ListGroups(ctx); err != nil {
		t.Fatalf("列组失败: %v", err)
	}
	if _, err := db.ListGroupsByUser(ctx, "u1"); err != nil {
		t.Fatalf("按用户列组失败: %v", err)
	}
	if _, err := db.ListUserGroupIDs(ctx); err != nil {
		t.Fatalf("列用户组关系失败: %v", err)
	}
	if err := db.SetGroupMembers(ctx, "g1", nil); err != nil {
		t.Fatalf("设置成员失败: %v", err)
	}

	// 给 database/sql 一点时间把连接归还（归还发生在 Close 之后，
	// 但统计计数是即时的，这里留一点余量以防驱动异步处理）。
	deadline := time.Now().Add(2 * time.Second)
	for {
		stats := rawDBOf(t, db).Stats()
		if stats.InUse == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("连接池仍有 %d 条连接未归还，OpenConnections=%d",
				stats.InUse, stats.OpenConnections)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestCloseReleasesFileHandle 关闭后库文件应可删除。
//
// ============================ 这条直接对应线上症状 ============================
// Windows 上文件被占用就无法删除。若 Close 没有真正释放所有连接，
// 测试用的 TempDir 清理会失败（"being used by another process"）。
//
// Linux 上删除正在使用的文件是允许的，所以这条**在 Linux 上不会失败**——
// 它的价值在 Windows CI。
func TestCloseReleasesFileHandle(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	path := filepath.Join(dir, "close.db")

	db, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("打开库失败: %v", err)
	}
	// 做一轮完整的读写，确保各类路径都碰过。
	if err := db.CreateGroup(ctx, user.Group{ID: "g1", Name: "组一", CreatedAt: time.Now()}); err != nil {
		t.Fatalf("建组失败: %v", err)
	}
	if _, err := db.ListGroups(ctx); err != nil {
		t.Fatalf("列组失败: %v", err)
	}
	if _, err := db.gdb.Model(dao.Groups.Table()).Ctx(ctx).All(); err != nil {
		t.Fatalf("gdb 查询失败: %v", err)
	}

	if err := db.Close(); err != nil {
		t.Fatalf("关闭库失败: %v", err)
	}

	// 关闭后：池的统计应为 0。
	stats := rawDBOf(t, db).Stats()
	if stats.OpenConnections != 0 {
		t.Fatalf("关闭后仍有 %d 条连接打开", stats.OpenConnections)
	}
}
