package store

// auto_adopt_test.go：自动发现扫描状态的仓储层测试。
//
// 这些用例跑在真实 PostgreSQL 上（与其他 store 测试一致），
// 因为 upsert 的 ON CONFLICT 语义、以及 WhereNotIn 的空列表行为
// 都是数据库相关的——用内存假实现测不出真问题。

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestAutoAdoptStateUpsertAndGet(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	if _, err := db.GetAutoAdoptState(ctx, "ag", "proj"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("不存在的结论应返回 ErrNotFound，实际 %v", err)
	}

	first := AutoAdoptState{
		Status:        AutoAdoptStatusSkipped,
		Reason:        "存在多个配置文件",
		Attempts:      1,
		LastScannedAt: time.Unix(1000, 0).UTC(),
	}
	if err := db.SaveAutoAdoptState(ctx, "ag", "proj", first); err != nil {
		t.Fatal(err)
	}
	got, err := db.GetAutoAdoptState(ctx, "ag", "proj")
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != AutoAdoptStatusSkipped || got.Reason != "存在多个配置文件" || got.Attempts != 1 {
		t.Fatalf("首次写入读回不符: %+v", got)
	}
	if got.LastScannedAt.Unix() != 1000 {
		t.Fatalf("最后扫描时间不符: %v", got.LastScannedAt)
	}
	if !got.NextCheckAt.IsZero() {
		t.Fatalf("skipped 的下次检查时间应为零值: %v", got.NextCheckAt)
	}

	// 同一 (agent,name) 再写一次必须是覆盖而不是插入第二行：
	// 否则"这个项目到底还扫不扫"会出现两个互相矛盾的答案。
	second := AutoAdoptState{
		Status:        AutoAdoptStatusFailed,
		Reason:        "读取超时",
		Attempts:      3,
		NextCheckAt:   time.Unix(2000, 0).UTC(),
		LastScannedAt: time.Unix(1500, 0).UTC(),
	}
	if err := db.SaveAutoAdoptState(ctx, "ag", "proj", second); err != nil {
		t.Fatal(err)
	}
	got, err = db.GetAutoAdoptState(ctx, "ag", "proj")
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != AutoAdoptStatusFailed || got.Attempts != 3 {
		t.Fatalf("覆盖后不符: %+v", got)
	}
	if got.NextCheckAt.Unix() != 2000 {
		t.Fatalf("下次检查时间未更新: %v", got.NextCheckAt)
	}

	// 不同 Agent 之间必须互不影响（同名项目是常态）。
	if _, err := db.GetAutoAdoptState(ctx, "other", "proj"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("不同 Agent 不应共享结论: %v", err)
	}
}

func TestAutoAdoptStateListAndDelete(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	for _, name := range []string{"a", "b"} {
		if err := db.SaveAutoAdoptState(ctx, "ag", name, AutoAdoptState{
			Status: AutoAdoptStatusSkipped, Reason: "r", Attempts: 1,
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.SaveAutoAdoptState(ctx, "ag2", "c", AutoAdoptState{
		Status: AutoAdoptStatusFailed, Attempts: 2,
	}); err != nil {
		t.Fatal(err)
	}

	// 一次取出某台 Agent 的全部结论（扫描每轮都要用，不能 N 次查询）。
	m, err := db.ListAutoAdoptStates(ctx, "ag")
	if err != nil {
		t.Fatal(err)
	}
	if len(m) != 2 || m["a"].Status != AutoAdoptStatusSkipped || m["b"].Status != AutoAdoptStatusSkipped {
		t.Fatalf("列表不符: %+v", m)
	}
	if _, ok := m["c"]; ok {
		t.Fatal("不应包含其他 Agent 的结论")
	}

	if err := db.DeleteAutoAdoptState(ctx, "ag", "a"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.GetAutoAdoptState(ctx, "ag", "a"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("删除后应查不到: %v", err)
	}
	// 删除不存在的结论不应报错（幂等：手动重试可能连点）。
	if err := db.DeleteAutoAdoptState(ctx, "ag", "a"); err != nil {
		t.Fatalf("重复删除应幂等: %v", err)
	}
}

// TestAutoAdoptStatePruneExcept 项目消失后结论必须被清掉。
//
// 不清的后果很隐蔽：项目名被重新创建时会带着老结论（例如"多配置文件"）
// 被直接跳过，而现场其实已经是另一个项目了——用户怎么查都找不到原因。
func TestAutoAdoptStatePruneExcept(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	for _, name := range []string{"keep", "gone"} {
		if err := db.SaveAutoAdoptState(ctx, "ag", name, AutoAdoptState{
			Status: AutoAdoptStatusSkipped, Reason: "r", Attempts: 1,
		}); err != nil {
			t.Fatal(err)
		}
	}
	// 另一台 Agent 的结论不受本次清理影响。
	if err := db.SaveAutoAdoptState(ctx, "ag2", "gone", AutoAdoptState{
		Status: AutoAdoptStatusSkipped, Reason: "r", Attempts: 1,
	}); err != nil {
		t.Fatal(err)
	}

	if err := db.DeleteAutoAdoptStatesExcept(ctx, "ag", []string{"keep"}); err != nil {
		t.Fatal(err)
	}
	m, err := db.ListAutoAdoptStates(ctx, "ag")
	if err != nil {
		t.Fatal(err)
	}
	if len(m) != 1 || m["keep"].Status != AutoAdoptStatusSkipped {
		t.Fatalf("应只保留 keep: %+v", m)
	}
	if _, err := db.GetAutoAdoptState(ctx, "ag2", "gone"); err != nil {
		t.Fatalf("不应影响其他 Agent: %v", err)
	}

	// 空 keep 表示"这台 Agent 下一个都不留"，必须清空而不是"什么都不删"。
	// 这个语义容易写反（WhereNotIn 空列表会拼出非法 SQL），所以单独钉住。
	if err := db.DeleteAutoAdoptStatesExcept(ctx, "ag", nil); err != nil {
		t.Fatal(err)
	}
	m, err = db.ListAutoAdoptStates(ctx, "ag")
	if err != nil {
		t.Fatal(err)
	}
	if len(m) != 0 {
		t.Fatalf("空 keep 应清空该 Agent 的全部结论: %+v", m)
	}
}
