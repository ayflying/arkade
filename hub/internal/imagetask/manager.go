package imagetask

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"sort"
	"sync"
	"time"
)

type remote struct{ agent, id string }
type job struct {
	task        Task
	ctx         context.Context
	cancel      context.CancelFunc
	refs        []remote
	disposed    bool
	archive     RemoteTask
	downloading bool
}
type Manager struct {
	mu           sync.Mutex
	backend      Backend
	jobs         map[string]*job
	closed       bool
	timeout      time.Duration
	pollInterval time.Duration
}

func New(backend Backend) *Manager {
	return &Manager{backend: backend, jobs: make(map[string]*job), timeout: 30 * time.Minute, pollInterval: 250 * time.Millisecond}
}
func terminal(s string) bool { return s == "succeeded" || s == "failed" || s == "canceled" }
func snapshot(t Task) Task {
	if t.Progress != nil {
		v := *t.Progress
		t.Progress = &v
	}
	if t.BytesTotal != nil {
		v := *t.BytesTotal
		t.BytesTotal = &v
	}
	return t
}
func (m *Manager) Get(id string) (Task, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	j := m.jobs[id]
	if j == nil {
		return Task{}, ErrNotFound
	}
	return snapshot(j.task), nil
}
func (m *Manager) List() []Task {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Task, 0, len(m.jobs))
	for _, j := range m.jobs {
		out = append(out, snapshot(j.task))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt > out[j].CreatedAt })
	return out
}
func (m *Manager) create(kind, agent, target string, owner string) (*job, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return nil, ErrClosed
	}
	if m.backend == nil {
		return nil, ErrInvalid
	}
	active := 0
	for _, j := range m.jobs {
		if !terminal(j.task.Status) {
			active++
		}
	}
	if active >= MaxActive {
		return nil, ErrLimit
	}
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return nil, ErrBackend
	}
	ctx, cancel := context.WithTimeout(context.Background(), m.timeout)
	j := &job{ctx: ctx, cancel: cancel, task: Task{ID: hex.EncodeToString(b[:]), Kind: kind, AgentID: agent, TargetAgentID: target, OwnerID: owner, Status: "running", Phase: "准备中", Message: "镜像操作已开始", CreatedAt: time.Now().UTC().Format(time.RFC3339Nano)}}
	m.jobs[j.task.ID] = j
	go func() { <-ctx.Done(); m.expire(j) }()
	return j, nil
}
func (m *Manager) expire(j *job) {
	m.mu.Lock()
	done := terminal(j.task.Status)
	if done {
		j.task.DownloadReady = false
	}
	m.mu.Unlock()
	if !done {
		if j.ctx.Err() == context.DeadlineExceeded {
			m.finish(j, ErrTimeout)
		} else {
			m.finish(j, ErrCanceled)
		}
	}
	m.dispose(j, true)
}
func (m *Manager) update(j *job, phase string, done int64, total *int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if terminal(j.task.Status) {
		return
	}
	j.task.Phase = phase
	if done < 0 {
		done = 0
	}
	if done > MaxArchiveSize {
		done = MaxArchiveSize
	}
	j.task.BytesDone = done
	if total != nil && *total >= 0 && *total <= MaxArchiveSize {
		v := *total
		j.task.BytesTotal = &v
		if v > 0 {
			p := float64(done) / float64(v)
			if p > 1 {
				p = 1
			}
			j.task.Progress = &p
		}
	}
}
func (m *Manager) finish(j *job, err error) {
	m.mu.Lock()
	if terminal(j.task.Status) {
		m.mu.Unlock()
		return
	}
	if j.ctx.Err() != nil {
		err = ErrCanceled
		if j.ctx.Err() == context.DeadlineExceeded {
			err = ErrTimeout
		}
	}
	j.task.FinishedAt = time.Now().UTC().Format(time.RFC3339Nano)
	j.task.Status = "succeeded"
	j.task.Phase = "已完成"
	j.task.Message = "镜像操作已完成"
	if err != nil {
		j.task.Status = "failed"
		j.task.Phase = "失败"
		j.task.Message = "远程镜像操作失败，请检查服务器连接和镜像参数"
		if err == ErrCanceled {
			j.task.Status = "canceled"
			j.task.Phase = "已取消"
			j.task.Message = "操作已取消；已导入的镜像不会自动回滚"
		}
		if err == ErrTimeout {
			j.task.Message = "镜像操作超时，请稍后重试"
		}
		if err == ErrIntegrity {
			j.task.Message = "镜像归档大小或校验和不一致，操作已终止"
		}
	}
	keep := err == nil && j.task.Kind == "export" && !m.closed && !j.disposed && j.ctx.Err() == nil
	j.task.DownloadReady = keep
	// Bound only historical records; live jobs are never evicted.
	for {
		count := 0
		var oldest *job
		for _, x := range m.jobs {
			if terminal(x.task.Status) {
				count++
				if oldest == nil || x.task.FinishedAt < oldest.task.FinishedAt {
					oldest = x
				}
			}
		}
		if count <= MaxHistory || oldest == nil {
			break
		}
		delete(m.jobs, oldest.task.ID)
		oldest.cancel()
	}
	m.mu.Unlock()
	if !keep {
		j.cancel()
		m.dispose(j, err != nil)
	}
}
func (m *Manager) register(j *job, agent string, r RemoteTask) error {
	if r.ID == "" {
		return ErrBackend
	}
	ref := remote{agent, r.ID}
	m.mu.Lock()
	disposed := m.closed || j.disposed || terminal(j.task.Status) || j.ctx.Err() != nil
	if !disposed {
		j.refs = append(j.refs, ref)
	}
	m.mu.Unlock()
	if disposed {
		m.cleanup([]remote{ref}, true)
		return ErrCanceled
	}
	return nil
}
func (m *Manager) cleanup(refs []remote, cancel bool) {
	for _, r := range refs {
		ctx, stop := context.WithTimeout(context.Background(), 5*time.Second)
		if cancel {
			_ = m.backend.Cancel(ctx, r.agent, r.id)
		}
		stop()
		// Cancel timing out must not prevent a separate release attempt.
		ctx, stop = context.WithTimeout(context.Background(), 5*time.Second)
		_ = m.backend.Release(ctx, r.agent, r.id)
		stop()
	}
}
func (m *Manager) dispose(j *job, cancel bool) {
	m.mu.Lock()
	j.disposed = true
	j.task.DownloadReady = false
	refs := j.refs
	j.refs = nil
	m.mu.Unlock()
	m.cleanup(refs, cancel)
}
func (m *Manager) Cancel(id string) (Task, error) {
	m.mu.Lock()
	j := m.jobs[id]
	m.mu.Unlock()
	if j == nil {
		return Task{}, ErrNotFound
	}
	j.cancel()
	m.finish(j, ErrCanceled)
	m.dispose(j, true)
	return m.Get(id)
}
func (m *Manager) Close() error {
	m.mu.Lock()
	m.closed = true
	all := make([]*job, 0, len(m.jobs))
	for _, j := range m.jobs {
		all = append(all, j)
	}
	m.mu.Unlock()
	for _, j := range all {
		j.cancel()
		m.finish(j, ErrCanceled)
		m.dispose(j, true)
	}
	return nil
}
