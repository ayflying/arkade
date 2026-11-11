package store

// lifecycle_test.go 验证"关闭后后台任务不会重新打开连接"。
//
// 只断言 Close() 那一刻 Stats().OpenConnections == 0 是不够的：
// 异步 prune goroutine 可能在 Close 之后才跑到 d.gdb.Exec，
// 那时 gdb 会重新建一条连接，文件句柄又被打开，而即时统计早已归零。
// 因此这里必须**等一段时间后**再查统计。

import (
	"context"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"arkade/hub/internal/audit"
)

// TestCloseWaitsForBackgroundPrune 关闭时等待后台清理任务。
func TestCloseWaitsForBackgroundPrune(t *testing.T) {
	ctx := context.Background()
	db, err := Open(ctx, filepath.Join(t.TempDir(), "life.db"))
	if err != nil {
		t.Fatalf("打开库失败: %v", err)
	}

	// 触发一次后台清理：WriteAudit 内部按间隔节流后起 goroutine。
	if err := db.WriteAudit(audit.Record{Action: "test"}); err != nil {
		_ = db.Close()
		t.Fatalf("写审计失败: %v", err)
	}

	if err := db.Close(); err != nil {
		t.Fatalf("关闭失败: %v", err)
	}

	// 关键：等一段时间再查。若 Close 没等后台任务，这里会看到连接被重新打开。
	time.Sleep(300 * time.Millisecond)
	if n := rawDBOf(t, db).Stats().OpenConnections; n != 0 {
		t.Fatalf("关闭后后台任务重新打开了 %d 条连接", n)
	}
}

// TestBackgroundTaskRejectedAfterClose 关闭后再提交后台任务必须被拒绝。
func TestBackgroundTaskRejectedAfterClose(t *testing.T) {
	ctx := context.Background()
	db, err := Open(ctx, filepath.Join(t.TempDir(), "gate.db"))
	if err != nil {
		t.Fatalf("打开库失败: %v", err)
	}

	var ran atomic.Bool
	if !db.goBackground(ctx, func(context.Context) { ran.Store(true) }) {
		_ = db.Close()
		t.Fatal("关闭前应接受后台任务")
	}
	if err := db.Close(); err != nil {
		t.Fatalf("关闭失败: %v", err)
	}

	// 关闭后必须拒绝，而不是默默起一个会用已关闭实例的任务。
	if db.goBackground(ctx, func(context.Context) { ran.Store(true) }) {
		t.Fatal("关闭后不应再接受后台任务")
	}
}

// TestCloseIsIdempotent 重复关闭不应 panic 或报错。
func TestCloseIsIdempotent(t *testing.T) {
	ctx := context.Background()
	db, err := Open(ctx, filepath.Join(t.TempDir(), "idem.db"))
	if err != nil {
		t.Fatalf("打开库失败: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("第一次关闭失败: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("重复关闭失败: %v", err)
	}
}
