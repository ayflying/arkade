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

const Interval = 6 * time.Hour

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
	if err == nil && (settings.AuditRetentionDays < 1 || settings.AuditRetentionDays > 3650) {
		err = gerror.New("审计保留天数必须为1至3650")
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
