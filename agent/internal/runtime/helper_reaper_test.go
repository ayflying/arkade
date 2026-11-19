package runtime

// helper_reaper_test.go：残留 helper 容器回收的判据测试。
//
// 这一层的风险是"误删正在使用的容器"，因此边界必须钉死：
// 运行中的、太新的、不是自己建的、名字前缀不对的，一律不许删。

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

// reaperContainer 构造一个容器列表项，默认是"本程序的 compose helper"。
func reaperContainer(name, role, state string, age time.Duration) Container {
	labels := map[string]string{}
	if role != "" {
		labels[helperLabel] = role
	}
	return Container{
		ID:          "id-" + name,
		Name:        name,
		State:       state,
		Labels:      labels,
		CreatedUnix: time.Now().Add(-age).Unix(),
	}
}

// TestHelperIsOwned 双判据：名字前缀 + 标签，缺一不可。
func TestHelperIsOwned(t *testing.T) {
	ok := []Container{
		reaperContainer("arkade-helper-abcd123456", helperRoleCompose, "exited", time.Hour),
		reaperContainer("arkade-vfh-abcd123456", helperRoleVolume, "exited", time.Hour),
	}
	for _, c := range ok {
		if !helperIsOwned(c) {
			t.Fatalf("%s 应判定为本程序的 helper", c.Name)
		}
	}
	bad := []Container{
		// 标签对但名字不是本程序的前缀：可能是别人用 -l 手工打上了标签，
		// 绝不能因为一个标签就去删别人的容器。
		reaperContainer("my-important-app", helperRoleCompose, "exited", time.Hour),
		// 名字前缀对但没有标签：无法确认来源，保守跳过。
		reaperContainer("arkade-helper-zzz", "", "exited", time.Hour),
		// 标签值不是我们写的两个之一。
		reaperContainer("arkade-helper-zzz", "something-else", "exited", time.Hour),
		// 普通容器。
		reaperContainer("nginx", "", "running", time.Hour),
	}
	for _, c := range bad {
		if helperIsOwned(c) {
			t.Fatalf("%s 不应判定为本程序的 helper", c.Name)
		}
	}
}

// TestHelperIsReapableStates 只有已停止的才可能被回收。
func TestHelperIsReapableStates(t *testing.T) {
	now := time.Now()
	// 运行中/重启中：可能是一次**正在进行**的请求，删掉就是随机打断别人。
	for _, state := range []string{"running", "restarting", "paused"} {
		c := reaperContainer("arkade-helper-live", helperRoleCompose, state, 24*time.Hour)
		if helperIsReapable(c, now, helperStaleAfter) {
			t.Fatalf("状态 %s 不应被回收", state)
		}
	}
	// 已停止且足够老：应被回收。
	for _, state := range []string{"created", "exited", "dead", "EXITED"} {
		c := reaperContainer("arkade-helper-old", helperRoleCompose, state, helperStaleAfter+time.Minute)
		if !helperIsReapable(c, now, helperStaleAfter) {
			t.Fatalf("状态 %s 且已超时，应被回收", state)
		}
	}
}

// TestHelperIsReapableAge 阈值必须严格大于 helperTimeout，避免误删正在跑的。
func TestHelperIsReapableAge(t *testing.T) {
	now := time.Now()
	// 刚停下：可能是"helper 跑完了但 Agent 还没来得及删"的正常窗口，必须跳过。
	fresh := reaperContainer("arkade-helper-fresh", helperRoleCompose, "exited", time.Minute)
	if helperIsReapable(fresh, now, helperStaleAfter) {
		t.Fatal("刚创建的容器不应被回收")
	}
	// 创建时间未知：无从判断年龄，跳过。
	unknown := reaperContainer("arkade-helper-nocreated", helperRoleCompose, "exited", 0)
	unknown.CreatedUnix = 0
	if helperIsReapable(unknown, now, helperStaleAfter) {
		t.Fatal("创建时间未知时不应回收")
	}
	// 未来时间（宿主时钟偏慢）：年龄为负，跳过而不是当作极老。
	future := reaperContainer("arkade-helper-future", helperRoleCompose, "exited", -time.Hour)
	if helperIsReapable(future, now, helperStaleAfter) {
		t.Fatal("创建时间在未来时不应回收")
	}
	// 阈值必须远大于单次 helper 的总时限，否则会误删正在运行的容器。
	if helperStaleAfter <= helperTimeout {
		t.Fatalf("回收阈值 %v 必须大于 helper 总时限 %v", helperStaleAfter, helperTimeout)
	}
}

// reapServer 模拟引擎的列表与删除端点，记录被删除的 ID。
type reapServer struct {
	mu      sync.Mutex
	list    []Container
	deleted []string
	failFor map[string]bool
}

func (s *reapServer) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		switch {
		case p == "/containers/json":
			s.mu.Lock()
			items := make([]map[string]any, 0, len(s.list))
			for _, c := range s.list {
				items = append(items, map[string]any{
					"Id": c.ID, "Names": []string{"/" + c.Name}, "State": c.State,
					"Labels": c.Labels, "Created": c.CreatedUnix,
				})
			}
			s.mu.Unlock()
			_ = json.NewEncoder(w).Encode(items)
		case r.Method == http.MethodDelete && strings.HasPrefix(p, "/containers/"):
			id := strings.TrimPrefix(p, "/containers/")
			s.mu.Lock()
			if s.failFor[id] {
				s.mu.Unlock()
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = w.Write([]byte(`{"message":"boom"}`))
				return
			}
			s.deleted = append(s.deleted, id)
			s.mu.Unlock()
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})
}

// TestReapStaleHelpersOnlyRemovesStale 回收整轮流程：只删该删的。
func TestReapStaleHelpersOnlyRemovesStale(t *testing.T) {
	srv := &reapServer{list: []Container{
		// 应删：本程序创建、已停止、超时。
		reaperContainer("arkade-helper-a1", helperRoleCompose, "created", 30*time.Minute),
		reaperContainer("arkade-vfh-b2", helperRoleVolume, "exited", time.Hour),
		// 不应删：还在跑。
		reaperContainer("arkade-helper-running", helperRoleCompose, "running", time.Hour),
		// 不应删：太新。
		reaperContainer("arkade-helper-fresh", helperRoleCompose, "exited", time.Second),
		// 不应删：不是我们的。
		reaperContainer("nginx", "", "exited", 24*time.Hour),
		reaperContainer("somebody-else", helperRoleCompose, "exited", 24*time.Hour),
	}}
	e := helperEngine(t, srv.handler())

	removed, err := e.ReapStaleHelpers(context.Background())
	if err != nil {
		t.Fatalf("回收不应报错: %v", err)
	}
	if removed != 2 {
		t.Fatalf("应回收 2 个，实际 %d（删除列表 %v）", removed, srv.deleted)
	}
	srv.mu.Lock()
	defer srv.mu.Unlock()
	if len(srv.deleted) != 2 {
		t.Fatalf("删除次数不符: %v", srv.deleted)
	}
	for _, id := range srv.deleted {
		if id != "id-arkade-helper-a1" && id != "id-arkade-vfh-b2" {
			t.Fatalf("删除了不该删的容器: %s", id)
		}
	}
}

// TestReapStaleHelpersContinuesAfterFailure 单个删除失败不应中断整轮回收。
func TestReapStaleHelpersContinuesAfterFailure(t *testing.T) {
	srv := &reapServer{
		list: []Container{
			reaperContainer("arkade-helper-bad", helperRoleCompose, "exited", time.Hour),
			reaperContainer("arkade-helper-good", helperRoleCompose, "exited", time.Hour),
		},
		failFor: map[string]bool{"id-arkade-helper-bad": true},
	}
	e := helperEngine(t, srv.handler())

	removed, err := e.ReapStaleHelpers(context.Background())
	if err == nil {
		t.Fatal("有删除失败时应上报错误")
	}
	if removed != 1 {
		t.Fatalf("另一批应继续删除，实际删除 %d 个", removed)
	}
	if !strings.Contains(err.Error(), "arkade-helper-bad") {
		t.Fatalf("错误里应点明是哪个容器失败: %v", err)
	}
}

// TestReapStaleHelpersNoopWhenNothingStale 没有残留时必须是沉默的空操作。
func TestReapStaleHelpersNoopWhenNothingStale(t *testing.T) {
	srv := &reapServer{list: []Container{
		reaperContainer("arkade-helper-running", helperRoleCompose, "running", time.Hour),
		reaperContainer("nginx", "", "exited", time.Hour),
	}}
	e := helperEngine(t, srv.handler())
	removed, err := e.ReapStaleHelpers(context.Background())
	if err != nil || removed != 0 {
		t.Fatalf("不应有动作: removed=%d err=%v", removed, err)
	}
	if len(srv.deleted) != 0 {
		t.Fatalf("不应删除任何容器: %v", srv.deleted)
	}
}

// TestRunHelperReaperStopsOnCancel ctx 取消后必须立刻退出，不留后台 goroutine。
func TestRunHelperReaperStopsOnCancel(t *testing.T) {
	srv := &reapServer{}
	e := helperEngine(t, srv.handler())
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		e.RunHelperReaper(ctx, func(int, error) {})
		close(done)
	}()
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("取消后回收协程必须退出")
	}
}
