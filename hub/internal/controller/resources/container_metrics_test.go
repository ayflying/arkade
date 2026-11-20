package resources

import (
	"testing"

	"arkade/hub/internal/store"
)

// 桶宽必须是整分钟，且降采样后不超过点数上限。
// 这些断言不经过 HTTP，避免"请求在路上的 1 秒"把结果抖动成另一种桶宽。
func TestHistoryBucket(t *testing.T) {
	cases := []struct {
		span, want int64
	}{
		{10, 60},              // 极短区间下限到 1 分钟
		{3600, 60},            // 1 小时：60 个点
		{7200, 60},            // 2 小时：刚好 120 个点
		{86400, 240},          // 1 天：240 秒一桶，360 个点
		{604800, 1680},        // 整 7 天：1680 秒一桶，360 个点
		{604799, 1680},        // 少 1 秒：截断后向上补到 1680，仍是整分钟
		{7 * 86400 * 2, 3360}, // 超长区间由 historyWindow 夹紧，这里只保证不越界
	}
	for _, c := range cases {
		got := historyBucket(c.span)
		if got != c.want {
			t.Errorf("span=%d: want %d, got %d", c.span, c.want, got)
		}
		if got%store.MonitorSampleBucketSeconds != 0 {
			t.Errorf("span=%d: 桶宽 %d 未对齐整分钟", c.span, got)
		}
		if c.span/got > store.MaxMonitorHistoryPoints {
			t.Errorf("span=%d: 桶宽 %d 只剩 %d 个点，超过上限", c.span, got, c.span/got)
		}
	}
}

// 窗口夹紧：越界不报错而是夹到可用范围，完全无效才拒绝。
func TestHistoryWindow(t *testing.T) {
	const now = 1_700_000_000
	maxSpan := int64(store.MonitorSampleRetentionDays) * 86400
	cases := []struct {
		name             string
		from, to         int64
		wantFrom, wantTo int64
		ok               bool
	}{
		{"正常一小时", now - 3600, now, now - 3600, now, true},
		{"缺参数", 0, 0, 0, 0, false},
		{"只有一端", now - 3600, 0, 0, 0, false},
		{"未来时间被夹到现在", now - 600, now + 600, now - 600, now, true},
		{"超出保留期被夹到 7 天", now - maxSpan*3, now, now - maxSpan, now, true},
		{"整段都过期", now - maxSpan*3, now - maxSpan*2, 0, 0, false},
		{"反向区间", now, now - 600, 0, 0, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			from, to, ok := historyWindow(c.from, c.to, now)
			if ok != c.ok {
				t.Fatalf("ok=%v want %v", ok, c.ok)
			}
			if !ok {
				return
			}
			if from != c.wantFrom || to != c.wantTo {
				t.Fatalf("window=%d..%d want %d..%d", from, to, c.wantFrom, c.wantTo)
			}
			if to-from > maxSpan {
				t.Fatalf("窗口 %d 超过保留期 %d", to-from, maxSpan)
			}
		})
	}
}
