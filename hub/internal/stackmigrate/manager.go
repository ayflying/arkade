package stackmigrate

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"sync"
	"time"
)

// Manager owns worker cancellation and bounded task/idempotency registries.
// Lock order is always Manager.mu then Task.mu; no backend I/O occurs under either.
type Manager struct {
	backend   Backend
	now       func() time.Time
	mu        sync.Mutex
	tasks     map[string]*Task
	byRequest map[string]string
	busyPair  map[string]string
	ctx       context.Context
	cancel    context.CancelFunc
	closed    bool
	wg        sync.WaitGroup
	done      chan struct{}
}

var ErrClosed = errors.New("迁移服务正在关闭，不能提交新任务")

func New(backend Backend) *Manager {
	ctx, cancel := context.WithCancel(context.Background())
	return &Manager{backend: backend, now: time.Now, tasks: map[string]*Task{}, byRequest: map[string]string{}, busyPair: map[string]string{}, ctx: ctx, cancel: cancel, done: make(chan struct{})}
}

// JSON tuples prevent delimiter collisions for externally supplied identities.
func pairKey(source, target string) string {
	pair := []string{source, target}
	sort.Strings(pair)
	key, _ := json.Marshal(pair)
	return string(key)
}
func requestKey(owner, request string) string {
	key, _ := json.Marshal([2]string{owner, request})
	return string(key)
}

func (m *Manager) Submit(ctx context.Context, opts Options) (Snapshot, error) {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return Snapshot{}, ErrClosed
	}
	m.sweepLocked()
	key := requestKey(opts.OwnerID, opts.RequestID)
	if opts.RequestID != "" {
		if id, ok := m.byRequest[key]; ok {
			if t, ok := m.tasks[id]; ok {
				if t.Name != opts.StackName || t.Source != opts.Source || t.Target != opts.Target || t.IncludeVolumes != opts.IncludeVolumes || t.StartAfter != opts.StartAfter {
					m.mu.Unlock()
					return Snapshot{}, ErrDuplicateRequest
				}
				snap := t.snapshot(0)
				m.mu.Unlock()
				return snap, nil
			}
		}
	}
	live := 0
	for _, t := range m.tasks {
		t.mu.Lock()
		if !terminal(t.status) {
			live++
		}
		t.mu.Unlock()
	}
	if live >= MaxTasks {
		m.mu.Unlock()
		return Snapshot{}, ErrTooManyTasks
	}
	pk := pairKey(opts.Source, opts.Target)
	if _, busy := m.busyPair[pk]; busy {
		m.mu.Unlock()
		return Snapshot{}, ErrPairBusy
	}
	// Keep at most MaxTasks snapshots. Retire the oldest terminal task when a new
	// live task needs space; idempotency for retained tasks is never evicted first.
	m.makeRoomLocked()
	id := newTaskID()
	t := &Task{id: id, Name: opts.StackName, RequestID: opts.RequestID, OwnerID: opts.OwnerID, Source: opts.Source, Target: opts.Target, IncludeVolumes: opts.IncludeVolumes, StartAfter: opts.StartAfter, Enabled: true, status: StatusQueued, createdAt: m.now()}
	m.tasks[id] = t
	m.busyPair[pk] = id
	if opts.RequestID != "" {
		m.byRequest[key] = id
	}
	m.wg.Add(1)
	// Preserve request values, but never its cancellation/deadline. Authoritative
	// owner overrides any stale actor value: backends must use OwnerIDFromContext.
	worker := context.WithValue(context.WithoutCancel(ctx), ownerContextKey{}, opts.OwnerID)
	m.mu.Unlock()
	go func() { defer m.wg.Done(); m.run(worker, t, opts) }()
	return t.snapshot(0), nil
}
func (m *Manager) Get(id string, after int64) (Snapshot, error) {
	m.mu.Lock()
	m.sweepLocked()
	t, ok := m.tasks[id]
	m.mu.Unlock()
	if !ok {
		return Snapshot{}, ErrTaskNotFound
	}
	return t.snapshot(after), nil
}

// Close rejects new work, cancels workers and waits for all backend calls to
// return. The caller's deadline only bounds waiting; it never uncancels workers.
// Backends must honor ctx and clean only temporary transfer archives on cancel.
func (m *Manager) Close(ctx context.Context) error {
	m.mu.Lock()
	if !m.closed {
		m.closed = true
		m.cancel()
		go func() { m.wg.Wait(); close(m.done) }()
	}
	done := m.done
	m.mu.Unlock()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (m *Manager) removeLocked(id string, t *Task) {
	delete(m.tasks, id)
	if t.RequestID != "" {
		key := requestKey(t.OwnerID, t.RequestID)
		if m.byRequest[key] == id {
			delete(m.byRequest, key)
		}
	}
	pk := pairKey(t.Source, t.Target)
	if m.busyPair[pk] == id {
		delete(m.busyPair, pk)
	}
}
func (m *Manager) sweepLocked() {
	now := m.now()
	for id, t := range m.tasks {
		t.mu.Lock()
		expired := terminal(t.status) && now.Sub(t.finishedAt) > TaskTTL
		t.mu.Unlock()
		if expired {
			m.removeLocked(id, t)
		}
	}
}
func (m *Manager) makeRoomLocked() {
	if len(m.tasks) < MaxTasks {
		return
	}
	var oldest *Task
	var oldestID string
	var oldestFinished time.Time
	for id, t := range m.tasks {
		t.mu.Lock()
		eligible := terminal(t.status)
		finished := t.finishedAt
		t.mu.Unlock()
		if eligible && (oldest == nil || finished.Before(oldestFinished)) {
			oldest = t
			oldestID = id
			oldestFinished = finished
		}
	}
	if oldest != nil {
		m.removeLocked(oldestID, oldest)
	}
}
