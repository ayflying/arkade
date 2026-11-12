package monitor

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"arkade/hub/internal/store"
	"arkade/protocol/envelope"
	"github.com/gogf/gf/v2/errors/gerror"
)

type Agent struct {
	ID, Name string
	Online   bool
	LastSeen int64
}
type Source interface {
	Agents(context.Context) ([]Agent, error)
	Snapshot(context.Context, string) (envelope.MonitorSnapshot, error)
}

// Event is one channel delivery. The adapter must only send Channel, never fan
// out to both channels: retries and recovery eligibility are tracked separately.
type Event struct {
	Incident store.MonitorIncident `json:"incident"`
	Channel  string                `json:"channel"`
	Event    string                `json:"event"`
}
type incidentState struct {
	Incident     store.MonitorIncident `json:"-"`
	Key          string                `json:"key"`
	Sent         map[string]bool       `json:"sent"`
	Delivered    map[string]string     `json:"delivered"`
	RetryAt      map[string]int64      `json:"retry_at"`
	Failures     map[string]int        `json:"failures"`
	Detail       string                `json:"detail"`
	LastObserved int64                 `json:"last_observed"`
	Recovering   bool                  `json:"recovering"`
	PendingEvent string                `json:"pending_event"`
	PendingAt    int64                 `json:"pending_at"`
}
type containerState struct {
	Running     bool
	Restarts    int
	LastRestart int64
	Seen        int64
}

// Engine 由单个调度 worker 串行执行规则、持久化和通知；采集使用最多八个
// 并发任务并在每轮结束前回收。依赖必须遵守 context，避免慢请求无限积压。
type Engine struct {
	db               *store.DB
	source           Source
	getConfig        func(context.Context) (Config, error)
	notify           func(context.Context, Event) error
	log              *slog.Logger
	now              func() time.Time
	tickMu           sync.Mutex
	lifeMu           sync.Mutex
	cancel           context.CancelFunc
	done             chan struct{}
	initialized      bool
	collectCursor    int
	deliveryCursor   string
	deliveryEligible map[string]bool
	deliveryBudget   time.Duration
	states           map[string]*incidentState
	containers       map[string]containerState
	received         map[string]int64
	offlineSince     map[string]int64
	suppressionMu    sync.Mutex
	suppressions     map[string]time.Time
	metrics          metricCache
}

func NewEngine(db *store.DB, source Source, getConfig func(context.Context) (Config, error), notify func(context.Context, Event) error, log *slog.Logger) *Engine {
	if log == nil {
		log = slog.Default()
	}
	return &Engine{db: db, source: source, getConfig: getConfig, notify: notify, log: log, now: time.Now, states: make(map[string]*incidentState), containers: make(map[string]containerState), received: make(map[string]int64), offlineSince: make(map[string]int64), suppressions: make(map[string]time.Time)}
}
func (e *Engine) Start(ctx context.Context) {
	e.lifeMu.Lock()
	defer e.lifeMu.Unlock()
	if e.done != nil {
		return
	}
	run, cancel := context.WithCancel(ctx)
	e.cancel = cancel
	e.done = make(chan struct{})
	go func() {
		defer close(e.done)
		for {
			if run.Err() != nil {
				return
			}
			tick, cancel := context.WithTimeout(run, 25*time.Second)
			if err := e.Tick(tick); err != nil && run.Err() == nil {
				e.log.Warn("监控轮询失败")
			}
			cancel()
			interval := 30 * time.Second
			if e.getConfig != nil {
				cctx, ccancel := context.WithTimeout(run, 5*time.Second)
				c, err := e.getConfig(cctx)
				ccancel()
				if err == nil && c.IntervalSeconds > 0 {
					interval = time.Duration(c.IntervalSeconds) * time.Second
				}
			}
			timer := time.NewTimer(interval)
			select {
			case <-run.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
		}
	}()
}
func (e *Engine) Shutdown(ctx context.Context) error {
	e.lifeMu.Lock()
	cancel, done := e.cancel, e.done
	e.lifeMu.Unlock()
	if cancel == nil {
		return nil
	}
	cancel()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return gerror.Wrap(ctx.Err(), "monitor shutdown")
	}
}

// Suppress clears an existing incident without recovery on the next evaluation,
// and ignores planned container operations until duration has elapsed.
func (e *Engine) Suppress(agentID, containerID string, duration time.Duration) {
	if duration <= 0 {
		return
	}
	e.suppressionMu.Lock()
	defer e.suppressionMu.Unlock()
	now := e.now()
	for key, until := range e.suppressions {
		if !now.Before(until) {
			delete(e.suppressions, key)
		}
	}
	if len(e.suppressions) >= 10000 {
		return
	}
	e.suppressions[metricKey(agentID, containerID)] = now.Add(duration)
}
func (e *Engine) suppressed(agent, container string, now time.Time) bool {
	// 操作抑制仅针对容器，绝不能吞掉 Agent 失联或采集失败事件。
	if container == "" {
		return false
	}
	e.suppressionMu.Lock()
	defer e.suppressionMu.Unlock()
	for key, until := range e.suppressions {
		if !now.Before(until) {
			delete(e.suppressions, key)
			continue
		}
		parts := strings.SplitN(key, "\x00", 2)
		if len(parts) == 2 && parts[0] == agent && (parts[1] == "" || parts[1] == "*" || (container != "" && strings.HasPrefix(container, parts[1]))) {
			return true
		}
	}
	return false
}
func (e *Engine) Metrics() []Metric { return e.metrics.list() }
func durationSeconds(value int) int64 {
	if value < 0 {
		return 120
	}
	return int64(value)
}
func seconds(value, fallback int) int64 {
	if value <= 0 {
		return int64(fallback)
	}
	return int64(value)
}
func contains(values []string, value string) bool {
	for _, v := range values {
		if v == value {
			return true
		}
	}
	return false
}
func identifier() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return time.Now().Format("20060102150405.000000000")
	}
	return hex.EncodeToString(b[:])
}
func (e *Engine) restore(ctx context.Context) error {
	if e.initialized {
		return nil
	}
	if e.db != nil {
		rows, err := e.db.ListOpenMonitorIncidents(ctx)
		if err != nil {
			return err
		}
		for _, row := range rows {
			var s incidentState
			if err := json.Unmarshal([]byte(row.DetailJSON), &s); err != nil || s.Key == "" {
				continue
			}
			s.Incident = row
			if row.Status == "pending" {
				s.LastObserved = 0
			}
			s.initMaps()
			e.states[s.Key] = &s
		}
	}
	e.initialized = true
	return nil
}
func (s *incidentState) initMaps() {
	if s.Sent == nil {
		s.Sent = map[string]bool{}
	}
	if s.Delivered == nil {
		s.Delivered = map[string]string{}
	}
	if s.RetryAt == nil {
		s.RetryAt = map[string]int64{}
	}
	if s.Failures == nil {
		s.Failures = map[string]int{}
	}
}
func (e *Engine) save(ctx context.Context, s *incidentState) error {
	b, err := json.Marshal(s)
	if err != nil {
		return err
	}
	s.Incident.DetailJSON = string(b)
	if e.db == nil {
		return nil
	}
	return e.db.UpsertMonitorIncident(ctx, s.Incident)
}
func (e *Engine) closeSilently(ctx context.Context, key string, now int64) error {
	s := e.states[key]
	if s == nil {
		return nil
	}
	s.Incident.Status = "resolved"
	s.Incident.ResolvedAt = now
	s.Incident.LastSeen = now
	if err := e.save(ctx, s); err != nil {
		return err
	}
	delete(e.states, key)
	return nil
}

// Tick performs exactly one collection cycle and is exported for deterministic
// integration tests. Production callers should normally use Start.
func (e *Engine) Tick(ctx context.Context) error {
	e.tickMu.Lock()
	defer e.tickMu.Unlock()
	if e.getConfig == nil {
		return gerror.New("monitor config getter is nil")
	}
	c, err := e.getConfig(ctx)
	if err != nil {
		return err
	}
	if err = e.restore(ctx); err != nil {
		return err
	}
	now := e.now()
	ts := now.Unix()
	e.deliveryEligible = make(map[string]bool)
	// Configuration changes and intentional operations never generate recovery.
	excludedKeys := make(map[string]bool)
	for _, metric := range e.metrics.list() {
		if contains(c.ExcludedContainers, metric.ContainerID) || contains(c.ExcludedContainers, metric.Name) {
			excludedKeys[metricKey(metric.AgentID, metric.ContainerID)] = true
			e.metrics.removeContainer(metric.AgentID, metric.ContainerID)
		}
	}
	for key, s := range e.states {
		if excludedKeys[metricKey(s.Incident.AgentID, s.Incident.ContainerID)] || !c.Enabled || contains(c.ExcludedAgents, s.Incident.AgentID) || contains(c.ExcludedContainers, s.Incident.ContainerID) || e.suppressed(s.Incident.AgentID, s.Incident.ContainerID, now) || (s.Incident.Kind == "cpu" && c.CPUPercent <= 0) || (s.Incident.Kind == "memory" && c.MemoryPercent <= 0) {
			if err = e.closeSilently(ctx, key, ts); err != nil {
				return err
			}
		}
	}
	if !c.Enabled {
		e.containers = map[string]containerState{}
		e.received = map[string]int64{}
		e.offlineSince = map[string]int64{}
		e.metrics.clear()
		return nil
	}
	if e.source == nil {
		return gerror.New("monitor source is nil")
	}
	agents, err := e.source.Agents(ctx)
	if err != nil {
		return err
	}
	collectionCtx, collectionCancel := context.WithTimeout(ctx, 10*time.Second)
	results := e.collect(collectionCtx, agents, c)
	collectionCancel()
	active := make(map[string]bool, len(agents))
	for _, a := range agents {
		if err = ctx.Err(); err != nil {
			return err
		}
		if contains(c.ExcludedAgents, a.ID) {
			e.metrics.removeAgent(a.ID)
			continue
		}
		active[a.ID] = true
		if !a.Online {
			if e.offlineSince[a.ID] == 0 {
				e.offlineSince[a.ID] = ts
			}
			// Offline timing is based on observations received by this Hub, never LastSeen.
			since := e.offlineSince[a.ID]
			if last := e.received[a.ID]; last > 0 && last < since {
				since = last
			}
			if err = e.evaluate(ctx, c, a.ID, "", "offline", ts-since >= seconds(c.OfflineSeconds, 120), true, "Agent 已失联", ts, true); err != nil {
				return err
			}
			// Freeze firing container incidents, reset pending duration. Do not fan out
			// loss-of-contact into a container-down storm or false recoveries.
			for _, s := range e.states {
				if s.Incident.AgentID == a.ID && s.Incident.ContainerID != "" && s.Incident.Status == "pending" {
					s.Incident.FirstSeen = ts
					s.LastObserved = 0
					if err = e.save(ctx, s); err != nil {
						return err
					}
				}
			}
			continue
		}
		delete(e.offlineSince, a.ID)
		if err = e.evaluate(ctx, c, a.ID, "", "offline", false, true, "Agent 已恢复连接", ts, true); err != nil {
			return err
		}
		result, collected := results[a.ID]
		if !collected {
			e.pauseResources(ts, a.ID)
			continue
		}
		snapshot, snapshotErr := result.snapshot, result.err
		if snapshotErr == nil && snapshot.Error != "" {
			snapshotErr = gerror.New(snapshot.Error)
		}
		if snapshotErr != nil {
			if err = e.evaluate(ctx, c, a.ID, "", "collection", true, true, "监控快照采集失败", ts, false); err != nil {
				return err
			}
			e.pauseResources(ts, a.ID)
			continue
		}
		e.received[a.ID] = ts
		if err = e.evaluate(ctx, c, a.ID, "", "collection", !snapshot.Complete, true, "监控快照采集不完整或已恢复", ts, false); err != nil {
			return err
		}
		seen := map[string]bool{}
		for _, m := range snapshot.Containers {
			if m.ID == "" {
				continue
			}
			seen[m.ID] = true
			if contains(c.ExcludedContainers, m.ID) || contains(c.ExcludedContainers, m.Name) || e.suppressed(a.ID, m.ID, now) {
				e.resetContainer(ctx, a.ID, m.ID, ts)
				e.metrics.removeContainer(a.ID, m.ID)
				continue
			}
			e.metrics.put(a.ID, m, now)
			e.metrics.annotate(a.ID, m.ID, a.Name, snapshot.Complete)
			if err = e.evaluateContainer(ctx, c, a.ID, m, ts); err != nil {
				return err
			}
		}
		if snapshot.Complete {
			e.metrics.retainAgentContainers(a.ID, seen)
			for key, v := range e.containers {
				if len(key) > len(a.ID) && key[:len(a.ID)+1] == a.ID+"\x00" && !seen[key[len(a.ID)+1:]] {
					if ts-v.Seen > 86400 {
						delete(e.containers, key)
					}
				}
			}
		}
	}
	// Inventory removal/agent exclusions silently close state rather than notify.
	for key, s := range e.states {
		if !active[s.Incident.AgentID] {
			if err = e.closeSilently(ctx, key, ts); err != nil {
				return err
			}
		}
	}
	for key, v := range e.containers {
		if ts-v.Seen > 86400 {
			delete(e.containers, key)
		}
	}
	e.metrics.prune(now.Add(-24 * time.Hour).Unix())
	return e.dispatchPhase(ctx, c, ts)
}

// 所有采集和规则持久化先完成，再使用独立预算投递；通知慢或失败不影响
// 后续 Agent 的规则评估。按规则键轮转，预算耗尽后下轮从后续事件继续。
func (e *Engine) dispatchPhase(ctx context.Context, c Config, ts int64) error {
	keys := make([]string, 0, len(e.deliveryEligible))
	for key := range e.deliveryEligible {
		if e.states[key] != nil {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	budget := e.deliveryBudget
	if budget <= 0 {
		budget = 12 * time.Second
	}
	notifyCtx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	start := sort.SearchStrings(keys, e.deliveryCursor)
	for n := 0; n < len(keys); n++ {
		if notifyCtx.Err() != nil {
			break
		}
		index := (start + n) % len(keys)
		key := keys[index]
		// 提前保存下一键，即使此事件投递用完预算，下轮也不会再次霸占队首。
		e.deliveryCursor = keys[(index+1)%len(keys)]
		if err := e.dispatch(ctx, notifyCtx, c, e.states[key], ts); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			e.log.Warn("监控投递状态保存失败")
		}
	}
	return nil
}

type collectionResult struct {
	snapshot envelope.MonitorSnapshot
	err      error
}

// 有界并发配合轮转游标，避免慢 Agent 阻塞其他节点或使队尾长期饥饿。
// 所有 worker 在本轮结束前回收，Source 必须遵守 context 取消。
func (e *Engine) collect(ctx context.Context, agents []Agent, c Config) map[string]collectionResult {
	results := make(map[string]collectionResult, len(agents))
	var mu sync.Mutex
	jobs := make(chan Agent)
	var wg sync.WaitGroup
	workers := 8
	if len(agents) < workers {
		workers = len(agents)
	}
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for a := range jobs {
				call, cancel := context.WithTimeout(ctx, 5*time.Second)
				snapshot, err := e.source.Snapshot(call, a.ID)
				cancel()
				mu.Lock()
				results[a.ID] = collectionResult{snapshot: snapshot, err: err}
				mu.Unlock()
			}
		}()
	}
	start := e.collectCursor
send:
	for i := 0; i < len(agents); i++ {
		index := (start + i) % len(agents)
		a := agents[index]
		if !a.Online || contains(c.ExcludedAgents, a.ID) {
			continue
		}
		select {
		case <-ctx.Done():
			break send
		default:
		}
		select {
		case jobs <- a:
			e.collectCursor = (index + 1) % len(agents)
		case <-ctx.Done():
			break send
		}
	}
	close(jobs)
	wg.Wait()
	return results
}
func (e *Engine) pauseResources(ts int64, agent string) {
	for _, s := range e.states {
		if s.Incident.AgentID == agent && s.Incident.ContainerID != "" && s.Incident.Status == "pending" {
			s.Incident.FirstSeen = ts
			s.LastObserved = 0
		}
	}
}
func (e *Engine) resetContainer(ctx context.Context, agent, container string, ts int64) {
	delete(e.containers, metricKey(agent, container))
	for k, s := range e.states {
		if s.Incident.AgentID == agent && s.Incident.ContainerID == container {
			if err := e.closeSilently(ctx, k, ts); err != nil {
				e.log.Warn("监控抑制状态保存失败")
			}
		}
	}
}
