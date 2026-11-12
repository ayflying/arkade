package store

// PostgreSQL pool leak and close regression tests.

import (
	"arkade/hub/internal/testdb"
	"context"
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
	db, err := Open(ctx, testdb.New(t))
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

// TestCloseReleasesConnections verifies server connection pool shutdown.
func TestCloseReleasesConnections(t *testing.T) {
	ctx := context.Background()
	path := testdb.New(t)

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
