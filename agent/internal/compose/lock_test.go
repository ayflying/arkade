package compose

// lock_test.go 覆盖项目锁的两条关键性质：
//
//  1. **取消安全**：等锁期间 ctx 结束 → 动作**绝不执行**。
//     这不是"提示一句已取消然后偷偷执行"，而是真的不碰磁盘、不碰容器。
//  2. **读侧不被拖死**：某项目在长任务时，List 不为它整体阻塞，
//     而是把它标成 busy，其余项目照常返回。

import (
	"context"
	"strings"
	"testing"
	"time"
)

// TestWithProjectCanceledWaiterDoesNotRun 验证等锁被取消后动作不执行。
//
// 这是最危险的一类 bug 的回归钉子：用户点取消 / Hub 侧 15 秒超时返回后，
// 如果实现用的是 sync.Mutex.Lock，那个 goroutine 还卡在排队上，
// 锁一放开就**继续往下执行 up/down**——用户以为超时什么都没发生，
// 实际几秒后服务被重建。
func TestWithProjectCanceledWaiterDoesNotRun(t *testing.T) {
	m, _ := newTestManager(t)
	const name = "lockcancel"

	// 先占住锁，模拟一个长任务（例如正在 up）。
	holdStarted := make(chan struct{})
	release := make(chan struct{})
	holderDone := make(chan struct{})
	go func() {
		defer close(holderDone)
		_ = m.withProject(context.Background(), name, func() error {
			close(holdStarted)
			<-release
			return nil
		})
	}()
	<-holdStarted

	// 第二个调用在等锁期间被取消。
	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()
	ran := false
	err := m.withProject(ctx, name, func() error {
		ran = true
		return nil
	})

	if err == nil {
		t.Fatal("等锁期间 ctx 结束应返回错误")
	}
	var lc errLockCanceled
	if !asLockCanceled(err, &lc) {
		t.Fatalf("应返回 errLockCanceled（便于区分'没开始'与'执行到一半'），实际 %T: %v", err, err)
	}
	if ran {
		t.Fatal("**ctx 已结束却仍然执行了动作**——取消语义失效，这是严重缺陷")
	}
	if !strings.Contains(err.Error(), "未执行") {
		t.Errorf("错误文案应明确说明未执行，实际 %q", err.Error())
	}

	close(release)
	<-holderDone
}

// TestWithProjectCanceledBeforeLockNotRun 验证"拿到锁时 ctx 已结束"也不执行。
//
// 与上一个用例的差别：这里锁是空闲的，但 ctx 已经结束。
// 若不检查，就会在请求超时之后照样开服务。
func TestWithProjectCanceledBeforeLockNotRun(t *testing.T) {
	m, _ := newTestManager(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // 一开始就取消

	ran := false
	err := m.withProject(ctx, "alreadycanceled", func() error {
		ran = true
		return nil
	})
	if err == nil {
		t.Fatal("ctx 已结束应返回错误")
	}
	if ran {
		t.Fatal("**ctx 已结束却仍然执行了动作**")
	}
}

// TestWithProjectSerializes 验证同一项目的操作确实串行。
func TestWithProjectSerializes(t *testing.T) {
	m, _ := newTestManager(t)
	const name = "serialize"

	firstStarted := make(chan struct{})
	release := make(chan struct{})
	done := make(chan struct{})

	go func() {
		defer close(done)
		_ = m.withProject(context.Background(), name, func() error {
			close(firstStarted)
			<-release
			return nil
		})
	}()
	<-firstStarted

	// 第二个调用必须等到第一个放锁。
	second := make(chan struct{})
	go func() {
		_ = m.withProject(context.Background(), name, func() error {
			close(second)
			return nil
		})
	}()

	select {
	case <-second:
		t.Fatal("第一个操作未结束时第二个不应进入")
	case <-time.After(60 * time.Millisecond):
	}
	close(release)
	select {
	case <-second:
	case <-time.After(2 * time.Second):
		t.Fatal("第一个操作结束后第二个应能进入")
	}
	<-done
}

// TestWithProjectDifferentNamesDoNotBlock 验证不同项目互不阻塞。
//
// 用一把大锁会让"给 A 项目 up"阻塞"给 B 项目备份"，
// 界面上表现为莫名其妙的整体卡顿。
func TestWithProjectDifferentNamesDoNotBlock(t *testing.T) {
	m, _ := newTestManager(t)
	holdStarted := make(chan struct{})
	release := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = m.withProject(context.Background(), "proj-a", func() error {
			close(holdStarted)
			<-release
			return nil
		})
	}()
	<-holdStarted

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	ran := false
	if err := m.withProject(ctx, "proj-b", func() error {
		ran = true
		return nil
	}); err != nil {
		t.Fatalf("不同项目不应互相阻塞: %v", err)
	}
	if !ran {
		t.Fatal("不同项目应立即执行")
	}
	close(release)
	<-done
}

// TestListMarksBusyProject 验证忙碌项目在列表里标成 busy，而不是拖死整个列表。
func TestListMarksBusyProject(t *testing.T) {
	m, _ := newSafeManager(t)

	// 建两个项目，确保列表有内容可比对。
	for _, n := range []string{"busyone", "idleone"} {
		spec := ProjectSpec{Name: n, Content: "services: {}\n"}
		spec.ExpectHash = HashProject(spec.Content, "")
		if _, err := m.ApplyProject(context.Background(), spec); err != nil {
			t.Fatal(err)
		}
	}

	// 占住 busyone 的锁，模拟长任务。
	holdStarted := make(chan struct{})
	release := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = m.withProject(context.Background(), "busyone", func() error {
			close(holdStarted)
			<-release
			return nil
		})
	}()
	<-holdStarted

	// List 必须立刻返回，不能等长任务。
	listDone := make(chan []ProjectStatus, 1)
	go func() {
		got, _ := m.List(context.Background())
		listDone <- got
	}()

	var list []ProjectStatus
	select {
	case list = <-listDone:
	case <-time.After(3 * time.Second):
		t.Fatal("List 被长任务拖住了——应给忙碌项目标 busy 并立即返回")
	}

	var busy, idle int
	for _, ps := range list {
		switch ps.Name {
		case "busyone":
			busy++
			if ps.Status != StatusBusy {
				t.Errorf("busyone 应标为 %s，实际 %s", StatusBusy, ps.Status)
			}
			// busy 的项目不读文件：可能正处在两次 rename 之间。
			if ps.Hash != "" {
				t.Errorf("busy 项目不应给出可能撕裂的哈希，实际 %q", ps.Hash)
			}
		case "idleone":
			idle++
			if ps.Status == StatusBusy {
				t.Error("未被占用的项目不应标 busy")
			}
		}
	}
	if busy != 1 || idle != 1 {
		t.Errorf("两个项目都应出现在列表里，实际 busy=%d idle=%d", busy, idle)
	}

	close(release)
	<-done
}

// asLockCanceled 是 errors.As 的小包装（避免测试里到处写 errors 包）。
func asLockCanceled(err error, target *errLockCanceled) bool {
	for err != nil {
		if lc, ok := err.(errLockCanceled); ok {
			*target = lc
			return true
		}
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}
