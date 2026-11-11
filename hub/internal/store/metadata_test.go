package store

// metadata_test.go 守住"冷元数据缓存下事务内 CRUD 不挂起"。
//
// 背景：gdb 在 Fields(...) 和 DO(struct) Insert/Update 时会取表结构，
// 取结构走连接池而不是事务连接。本库 MaxOpenConns=1，若没有预热，
// 事务占住唯一连接后再取结构就会永久等待——测试会挂起而不是报错，
// 因此每个用例都必须带超时，让故障表现为失败而不是卡死。
//
// 注意：用 map[string]any 作为 Data 不会触发取结构（列名已显式给出），
// 所以下面的用例**必须**用 DO struct 或 Fields，否则测不出问题。

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/gogf/gf/v2/database/gdb"

	"arkade/hub/internal/model/do"
	"arkade/hub/internal/user"
)

// withTimeout 在超时内执行 fn，超时即判定为死锁。
//
// 挂起是这类故障的真实表现（连接永不归还），所以超时要短：
// 正常路径在毫秒级完成，2 秒足够。
func withTimeout(t *testing.T, label string, fn func() error) {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- fn() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("%s 失败: %v", label, err)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("%s 挂起（疑似元数据缓存未预热导致事务等待连接）", label)
	}
}

// TestColdCacheTransactionCRUD 在刚打开的库上立刻做会取表结构的操作。
//
// 覆盖 DO struct 写入与 Fields 查询两条会触发元数据的路径，
// 且分散到多张表，确保 businessTables() 没有遗漏。
func TestColdCacheTransactionCRUD(t *testing.T) {
	ctx := context.Background()
	d, err := Open(ctx, filepath.Join(t.TempDir(), "cold.db"))
	if err != nil {
		t.Fatalf("打开库失败: %v", err)
	}
	defer d.Close()

	// users：事务内 DO struct Insert（会取表结构做列映射）。
	withTimeout(t, "事务内 DO 插入用户", func() error {
		return d.TxModel(ctx, func(ctx context.Context, tx gdb.TX) error {
			_, err := tx.Model(do.Users{}).Data(do.Users{
				Id: "u1", Username: "alice", DisplayName: "Alice",
				PasswordHash: "h", Role: "admin", Disabled: 0,
				CreatedAt: time.Now().Unix(), UpdatedAt: time.Now().Unix(),
			}).Insert()
			return err
		})
	})

	// users：Fields 显式列名（同样触发列校验）。
	withTimeout(t, "事务内 Fields 查询用户", func() error {
		return d.TxModel(ctx, func(ctx context.Context, tx gdb.TX) error {
			_, err := tx.Model(do.Users{}).Fields("id", "username").Where("id", "u1").One()
			return err
		})
	})

	// groups / user_groups / agent_tokens / audit_logs / compose_stacks / sessions：
	// 走仓储方法，内部同样是 DO struct 写入。
	withTimeout(t, "事务内创建用户组", func() error {
		return d.CreateGroup(ctx, user.Group{ID: "g1", Name: "group-one"})
	})
	withTimeout(t, "事务内加组成员", func() error {
		return d.AddUserToGroup(ctx, "u1", "g1")
	})
	withTimeout(t, "事务内写 Agent 令牌", func() error {
		return d.SaveAgentToken(ctx, "hash-1", "a1", time.Now())
	})
	withTimeout(t, "事务内创建编排", func() error {
		return d.CreateComposeStack(ctx, mkStack("a1", "web", "services: {}"))
	})
	withTimeout(t, "事务内创建会话", func() error {
		now := time.Now().Unix()
		return d.CreateSession(ctx, "tok-1", "u1", now, now+3600)
	})
}
