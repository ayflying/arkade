package httpx

// auto_adopt_policy_test.go：扫描节流策略的纯函数测试。
//
// 这些用例不依赖数据库与 Agent 连接，是"容器创建频率"这个行为契约的
// 直接表达：什么情况下允许再读一次配置（= 再建一个 helper 容器），
// 什么情况下必须停手。

import (
	"testing"
	"time"

	"arkade/hub/internal/agentlink"
	"arkade/hub/internal/store"
	"arkade/protocol/envelope"
)

// TestAutoAdoptShouldScanPending 从未扫过的项目必须扫，否则永远无法被发现。
func TestAutoAdoptShouldScanPending(t *testing.T) {
	now := time.Now()
	if !autoAdoptShouldScan(store.AutoAdoptState{}, false, now) {
		t.Fatal("未扫描过的项目必须允许扫描")
	}
}

// TestAutoAdoptShouldScanSkippedNeverRescans 确定性阻塞不再自动重扫。
//
// 这是本次改造的核心：旧实现里被 Blockers 阻塞的项目每 30 秒重读一次，
// 每次都创建一个 helper 容器，且永远不会成功。
func TestAutoAdoptShouldScanSkippedNeverRescans(t *testing.T) {
	now := time.Now()
	prev := store.AutoAdoptState{
		Status:      store.AutoAdoptStatusSkipped,
		Reason:      "存在多个配置文件",
		Attempts:    1,
		NextCheckAt: time.Time{}, // 即使零值也不该恢复扫描
	}
	// 无论过多久都不再扫。
	for _, d := range []time.Duration{0, time.Minute, 24 * time.Hour, 365 * 24 * time.Hour} {
		if autoAdoptShouldScan(prev, true, now.Add(d)) {
			t.Fatalf("skipped 状态在 %v 后不应重扫", d)
		}
	}
}

// TestAutoAdoptShouldScanFailedBackoff 环境性失败按退避时间重试。
func TestAutoAdoptShouldScanFailedBackoff(t *testing.T) {
	now := time.Now()
	prev := store.AutoAdoptState{
		Status:      store.AutoAdoptStatusFailed,
		Attempts:    1,
		NextCheckAt: now.Add(time.Minute),
	}
	if autoAdoptShouldScan(prev, true, now) {
		t.Fatal("退避时间未到不应重扫")
	}
	if autoAdoptShouldScan(prev, true, now.Add(59*time.Second)) {
		t.Fatal("退避时间未到不应重扫")
	}
	if !autoAdoptShouldScan(prev, true, now.Add(time.Minute)) {
		t.Fatal("退避时间已到应重扫")
	}
	if !autoAdoptShouldScan(prev, true, now.Add(2*time.Minute)) {
		t.Fatal("退避时间已过应重扫")
	}
}

// TestAutoAdoptShouldScanFailedExhausted 重试次数用尽后不再自动重扫。
func TestAutoAdoptShouldScanFailedExhausted(t *testing.T) {
	now := time.Now()
	prev := store.AutoAdoptState{
		Status:      store.AutoAdoptStatusFailed,
		Attempts:    autoAdoptMaxAttempts,
		NextCheckAt: now.Add(-time.Hour), // 时间早过了也不恢复
	}
	if autoAdoptShouldScan(prev, true, now) {
		t.Fatal("重试次数用尽后不应再自动重扫")
	}
}

// TestAutoAdoptShouldScanUnknownStateNotStuck 未知状态不能把项目永久卡死。
//
// 防御性用例：将来新增状态或历史数据异常时，宁可多扫一次，
// 也不能因为不认识的状态让一个项目永远不被接管。
func TestAutoAdoptShouldScanUnknownStateNotStuck(t *testing.T) {
	now := time.Now()
	prev := store.AutoAdoptState{Status: "something-new", Attempts: 3, NextCheckAt: now.Add(time.Hour)}
	if !autoAdoptShouldScan(prev, true, now) {
		t.Fatal("未知状态应允许扫描，避免项目被永久卡死")
	}
}

// TestAutoAdoptRetryDelay 退避必须单调增长且封顶。
func TestAutoAdoptRetryDelay(t *testing.T) {
	want := []time.Duration{
		1 * time.Minute,  // attempts=1
		2 * time.Minute,  // 2
		4 * time.Minute,  // 3
		8 * time.Minute,  // 4
		16 * time.Minute, // 5
	}
	for i, w := range want {
		if got := autoAdoptRetryDelay(int64(i + 1)); got != w {
			t.Fatalf("attempts=%d 期望 %v，实际 %v", i+1, w, got)
		}
	}
	// 超过上限后一律封顶，不会无限增长。
	for _, n := range []int64{6, 10, 100} {
		if got := autoAdoptRetryDelay(n); got != autoAdoptRetryMax {
			t.Fatalf("attempts=%d 应封顶 %v，实际 %v", n, autoAdoptRetryMax, got)
		}
	}
	// 非法输入按第一次处理，不产生负数或零（零会让下一轮立刻重扫，等于没退避）。
	for _, n := range []int64{0, -1} {
		if got := autoAdoptRetryDelay(n); got != autoAdoptRetryBase {
			t.Fatalf("attempts=%d 应回退到 %v，实际 %v", n, autoAdoptRetryBase, got)
		}
	}
}

// TestAutoAdoptRetryable 只有会自愈的原因才值得退避重试。
func TestAutoAdoptRetryable(t *testing.T) {
	retry := []string{
		envelope.CodeAgentBusy,
		envelope.CodeTimeout,
		envelope.CodeAgentOffline,
		envelope.CodeRuntimeFailed,
		envelope.CodeInternal,
		// 缺 helper 基础镜像是**用户拉一下就能好**的环境问题，
		// 不是"运行时做不到"，所以必须可重试；
		// 否则用户按提示拉完镜像，后台已经永久停扫。
		envelope.CodeDependencyMissing,
	}
	for _, code := range retry {
		if !autoAdoptRetryable(&agentlink.ConnError{Code: code}) {
			t.Fatalf("%s 属于环境性失败，应可重试", code)
		}
	}
	// 引擎不支持/项目不存在/无权限：重试多少次都同样失败，
	// 必须当作确定性阻塞，否则就是持续创建无用容器的元凶。
	giveUp := []string{
		envelope.CodeUnsupported,
		envelope.CodeNotFound,
		envelope.CodeForbidden,
		envelope.CodeUnauthorized,
		envelope.CodeBadRequest,
		envelope.CodeConflict,
	}
	for _, code := range giveUp {
		if autoAdoptRetryable(&agentlink.ConnError{Code: code}) {
			t.Fatalf("%s 不会自愈，不应重试", code)
		}
	}
	if autoAdoptRetryable(nil) {
		t.Fatal("nil 错误不应判定为可重试")
	}
}

// TestAutoAdoptFailureStateDeterministic 确定性失败直接落 skipped 且不再排期。
func TestAutoAdoptFailureStateDeterministic(t *testing.T) {
	now := time.Now()
	prev := store.AutoAdoptState{Attempts: 2}
	got := autoAdoptFailureState(prev, true, false, "存在多个配置文件", now)
	if got.Status != store.AutoAdoptStatusSkipped {
		t.Fatalf("确定性失败应落 skipped，实际 %s", got.Status)
	}
	if !got.NextCheckAt.IsZero() {
		t.Fatal("skipped 不应有下次检查时间")
	}
	if got.Attempts != 3 {
		t.Fatalf("尝试次数应累加，实际 %d", got.Attempts)
	}
	if got.LastScannedAt.IsZero() {
		t.Fatal("应记录最后扫描时间")
	}
}

// TestAutoAdoptFailureStateRetryBackoff 环境性失败按次数排期，达上限后停手。
func TestAutoAdoptFailureStateRetryBackoff(t *testing.T) {
	now := time.Now()
	got := autoAdoptFailureState(store.AutoAdoptState{}, false, true, "读取超时", now)
	if got.Status != store.AutoAdoptStatusFailed || got.Attempts != 1 {
		t.Fatalf("首次失败状态不符: %+v", got)
	}
	if !got.NextCheckAt.Equal(now.Add(autoAdoptRetryBase)) {
		t.Fatalf("首次失败应等待 %v，实际 %v", autoAdoptRetryBase, got.NextCheckAt.Sub(now))
	}

	// 逐次重试，周期必须递增。
	cur := got
	for i := 2; i < int(autoAdoptMaxAttempts); i++ {
		next := autoAdoptFailureState(cur, true, true, "读取超时", now)
		if !next.NextCheckAt.After(cur.NextCheckAt) {
			t.Fatalf("第 %d 次重试的等待时间必须比上一次长", i)
		}
		cur = next
	}

	// 达到上限：不再排期，并在原因里说明。
	last := autoAdoptFailureState(store.AutoAdoptState{Attempts: autoAdoptMaxAttempts - 1}, true, true, "读取超时", now)
	if last.Status != store.AutoAdoptStatusFailed {
		t.Fatalf("次数用尽仍应是 failed（保留原因给界面），实际 %s", last.Status)
	}
	if !last.NextCheckAt.IsZero() {
		t.Fatal("次数用尽后不应再排期")
	}
	if !containsStr(last.Reason, "暂停自动重试") {
		t.Fatalf("原因里应说明已暂停自动重试: %s", last.Reason)
	}
}

func containsStr(s, sub string) bool {
	return len(sub) == 0 || (len(s) >= len(sub) && indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
