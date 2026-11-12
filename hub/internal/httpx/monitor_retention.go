package httpx

import (
	"arkade/hub/internal/store"
	"context"
	"log/slog"
	"time"
)

// 定期清理30天前已结束事件和投递记录，未恢复告警始终保留。
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
				run, stop := context.WithTimeout(ctx, 15*time.Second)
				if err := db.PruneMonitorHistory(run, time.Now().Add(-30*24*time.Hour).Unix()); err != nil && ctx.Err() == nil {
					log.Warn("清理监控历史失败")
				}
				stop()
				timer.Reset(24 * time.Hour)
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
