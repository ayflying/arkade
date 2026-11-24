// Package auditretention owns instance-scoped, dynamically configured audit expiry.
package auditretention

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"arkade/hub/internal/store"
	"github.com/gogf/gf/v2/errors/gerror"
)

// Interval 是两次清理尝试之间的间隔。
//
// 保留期由系统设置决定，是"最长保留 30 天"这类硬上限；1 小时的清理间隔
// 决定了**实际保留期最多比设置多 1 小时**。为什么不做更短：
// 清理要扫 created_at 索引并删除一批行，开销随表增长；1 小时一次既能
// 把逾期控制在小时级，又不会把 DELETE 变成常跑的热路径。这里也是唯一
// 的清理入口——写入路径不再顺手删旧记录。
const Interval = time.Hour

type Store interface {
	GetSystemSettings(context.Context) (store.SystemSettings, error)
	PruneAuditBefore(context.Context, time.Time) (int64, error)
}
type Worker struct {
	store    Store
	log      *slog.Logger
	cancel   context.CancelFunc
	done     chan struct{}
	once     sync.Once
	now      func() time.Time
	interval time.Duration
}

// New starts an immediate asynchronous sweep, then retries every six hours.
// Close must finish before the backing database is closed.
func New(ctx context.Context, db Store, log *slog.Logger) *Worker {
	return start(ctx, db, log, Interval, time.Now)
}
func start(parent context.Context, db Store, log *slog.Logger, interval time.Duration, now func() time.Time) *Worker {
	ctx, cancel := context.WithCancel(parent)
	if log == nil {
		log = slog.Default()
	}
	w := &Worker{store: db, log: log, cancel: cancel, done: make(chan struct{}), interval: interval, now: now}
	go func() {
		defer close(w.done)
		w.sweep(ctx)
		ticker := time.NewTicker(w.interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				w.sweep(ctx)
			}
		}
	}()
	return w
}
func (w *Worker) sweep(ctx context.Context) {
	if ctx.Err() != nil {
		return
	}
	settings, err := w.store.GetSystemSettings(ctx)
	if err == nil && (settings.AuditRetentionDays < store.AuditMinRetentionDays || settings.AuditRetentionDays > store.AuditRetentionDays) {
		// 读到的值超限时不删任何东西：宁可多留一天，也不按一个
		// 讲不通的天数去删历史（例如被手工改坏的库）。
		err = gerror.New("审计保留天数必须在1至30天之间")
	}
	if err == nil {
		_, err = w.store.PruneAuditBefore(ctx, w.now().UTC().AddDate(0, 0, -int(settings.AuditRetentionDays)))
	}
	if err != nil && ctx.Err() == nil {
		w.log.Warn("审计保留清理失败，将在下个周期重试")
	}
}

// Close is idempotent. A timed-out caller can retry waiting with a new context.
func (w *Worker) Close(ctx context.Context) error {
	w.once.Do(w.cancel)
	select {
	case <-w.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
