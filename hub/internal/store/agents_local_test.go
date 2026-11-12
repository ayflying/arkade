package store

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"
)

// 使用真实 PostgreSQL，覆盖首次预建、重启只读与所有接管/轮换冲突。
func TestEnsureLocalAgent(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	hash := hashOf("local-test-token-0123456789")
	results := make(chan error, 4)
	for i := 0; i < cap(results); i++ {
		go func() { results <- db.EnsureLocalAgent(ctx, hash) }()
	}
	for i := 0; i < cap(results); i++ {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	rec, err := db.GetAgent(ctx, LocalAgentID)
	if err != nil {
		t.Fatal(err)
	}
	if rec.Name != LocalAgentName || rec.DisplayName != LocalAgentName || !rec.Preprovisioned || rec.Disabled || rec.Status != StatusPending {
		t.Fatalf("预建字段错误: %+v", rec)
	}
	if id, err := db.LookupAgentByTokenHash(ctx, hash); err != nil || id != LocalAgentID {
		t.Fatalf("凭据绑定错误: %q %v", id, err)
	}
	// 模拟网页修改名称和禁用：重启不能覆盖任何字段或时间戳。
	_, err = rawDBOf(t, db).ExecContext(ctx, `UPDATE agents SET name='管理员改名', display_name='显示名', disabled=1, updated_at=123 WHERE id=$1`, LocalAgentID)
	if err != nil {
		t.Fatal(err)
	}
	before, _ := db.GetAgent(ctx, LocalAgentID)
	if err := db.EnsureLocalAgent(ctx, hash); err != nil {
		t.Fatal(err)
	}
	after, _ := db.GetAgent(ctx, LocalAgentID)
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("重启覆盖网页修改: %+v -> %+v", before, after)
	}
	if err := db.EnsureLocalAgent(ctx, hashOf("rotated-env-token")); err == nil {
		t.Fatal("环境变量轮换必须保守报错")
	}
	newHash := hashOf("web-reset-token")
	if err := db.ResetAgentTokenHash(ctx, LocalAgentID, newHash, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := db.EnsureLocalAgent(ctx, hash); err == nil {
		t.Fatal("不得恢复网页重置的旧凭据")
	}
	if id, err := db.LookupAgentByTokenHash(ctx, newHash); err != nil || id != LocalAgentID {
		t.Fatalf("重置凭据被破坏: %v", err)
	}
	if err := db.EnsureLocalAgent(ctx, newHash); err != nil {
		t.Fatal(err)
	}
	if err := db.DeleteAgentToken(ctx, LocalAgentID); err != nil {
		t.Fatal(err)
	}
	if err := db.EnsureLocalAgent(ctx, newHash); err == nil {
		t.Fatal("不得重建已撤销凭据")
	}
}

func TestEnsureLocalAgentConflicts(t *testing.T) {
	for _, kind := range []string{"非预建固定ID", "预建固定ID不同凭据", "同名", "同摘要", "孤儿固定ID凭据"} {
		t.Run(kind, func(t *testing.T) {
			db := newTestDB(t)
			ctx := context.Background()
			hash := hashOf("local-token-0123456789")
			rec := newPreprovRecord("其他服务器")
			switch kind {
			case "非预建固定ID":
				rec.ID = LocalAgentID
				if err := db.UpsertAgent(ctx, rec); err != nil {
					t.Fatal(err)
				}
			case "预建固定ID不同凭据":
				rec.ID = LocalAgentID
				if err := db.CreatePreprovisionedAgent(ctx, rec, hashOf("different"), time.Now()); err != nil {
					t.Fatal(err)
				}
			case "同名":
				rec.Name = "  " + LocalAgentName + "\t"
				if err := db.CreatePreprovisionedAgent(ctx, rec, hashOf("different"), time.Now()); err != nil {
					t.Fatal(err)
				}
			case "同摘要":
				if err := db.CreatePreprovisionedAgent(ctx, rec, hash, time.Now()); err != nil {
					t.Fatal(err)
				}
			case "孤儿固定ID凭据":
				if err := db.SaveAgentToken(ctx, hashOf("different"), LocalAgentID, time.Now()); err != nil {
					t.Fatal(err)
				}
			}
			before, err := db.ListAgents(ctx)
			if err != nil {
				t.Fatal(err)
			}
			err = db.EnsureLocalAgent(ctx, hash)
			if err == nil {
				t.Fatal("冲突必须报错")
			}
			if kind == "同名" && !errors.Is(err, ErrAgentNameTaken) {
				t.Fatalf("应保留重名错误: %v", err)
			}
			after, err := db.ListAgents(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(before, after) {
				t.Fatal("冲突不应修改任何记录")
			}
		})
	}
}
