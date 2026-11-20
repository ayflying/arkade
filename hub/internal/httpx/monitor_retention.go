package httpx

import (
	"arkade/hub/internal/store"
	"context"
	"log/slog"
	"time"
)

// 定期清理：30 天前的已结束事件和投递记录，以及 7 天前的容器资源历史。
// 未恢复告警始终保留，不随保留期删除。
func startMonitorRetention(db *store.DB, log *slog.Logger) func(context.Context) error {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		timer := time.NewTimer(time.Minute)
		defer timer.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-timer.C:
				// 历史清理比事件清理更勤：7 天保留期内多留一天样本只是浪费磁盘，
				// 但让它拖成"保留 8 天"就与文档和查询上限不一致了。
				pruneMonitorHistory(ctx, db, log, time.Now())
				timer.Reset(6 * time.Hour)
			}
		}
	}()
	return func(ctx context.Context) error {
		cancel()
		select {
		case <-done:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func pruneMonitorHistory(ctx context.Context, db *store.DB, log *slog.Logger, now time.Time) {
	run, stop := context.WithTimeout(ctx, 30*time.Second)
	defer stop()
	if err := db.PruneMonitorHistory(run, now.Add(-30*24*time.Hour).Unix()); err != nil && ctx.Err() == nil {
		log.Warn("清理监控历史失败")
	}
	runSample, stopSample := context.WithTimeout(ctx, 60*time.Second)
	defer stopSample()
	if err := db.PruneMonitorSamples(runSample, now.AddDate(0, 0, -store.MonitorSampleRetentionDays).Unix()); err != nil && ctx.Err() == nil {
		log.Warn("清理容器资源历史失败")
	}
}
