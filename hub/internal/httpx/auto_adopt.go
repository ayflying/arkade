package httpx

import (
	"context"
	"errors"
	"sync"
	"time"

	"arkade/hub/internal/agentlink"
	"arkade/hub/internal/service/compose"
	"arkade/hub/internal/store"
	"arkade/protocol/envelope"
)

// 自动扫描独立于HTTP GET；只复用source/preview/adopt，禁止调用up/down。
type autoAdoptBackend interface {
	ComposeList(context.Context, string) (envelope.ComposeListResult, *agentlink.ConnError)
	ComposeSource(context.Context, string, string) (envelope.ComposeSourceResult, *agentlink.ConnError)
	ComposePreview(context.Context, string, envelope.ComposePreviewPayload) (envelope.ComposePreviewResult, *agentlink.ConnError)
	ComposeAdopt(context.Context, string, envelope.ComposeAdoptPayload) (envelope.ComposeAdoptResult, *agentlink.ConnError)
}
type autoAdoptState struct {
	Status string
	Reason string
}
type autoAdopter struct {
	db     *store.DB
	ops    autoAdoptBackend
	online func() map[string]struct{}
	mu     sync.RWMutex
	states map[string]map[string]autoAdoptState
	cancel context.CancelFunc
	done   chan struct{}
}

func newAutoAdopter(db *store.DB, ops autoAdoptBackend, online func() map[string]struct{}) *autoAdopter {
	return &autoAdopter{db: db, ops: ops, online: online, states: make(map[string]map[string]autoAdoptState)}
}
func (a *autoAdopter) recordedState(agent, name string) (autoAdoptState, bool) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	s, ok := a.states[agent][name]
	return s, ok
}
func (a *autoAdopter) state(agent, name string) autoAdoptState {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if s, ok := a.states[agent][name]; ok {
		return s
	}
	return autoAdoptState{Status: "pending", Reason: "等待后台扫描，兼容项目将自动登记配置；容器仍可直接管理"}
}
func (a *autoAdopter) set(agent, name, status, reason string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.states[agent] == nil {
		a.states[agent] = make(map[string]autoAdoptState)
	}
	a.states[agent][name] = autoAdoptState{status, reason}
}
func (a *autoAdopter) start(parent context.Context) {
	ctx, cancel := context.WithCancel(parent)
	a.cancel = cancel
	a.done = make(chan struct{})
	go func() {
		defer close(a.done)
		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()
		next := make(map[string]time.Time)
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				ids := a.online()
				for id := range next {
					if _, ok := ids[id]; !ok {
						delete(next, id)
						a.mu.Lock()
						delete(a.states, id)
						a.mu.Unlock()
					}
				}
				for id := range ids {
					if time.Now().Before(next[id]) {
						continue
					}
					a.scan(ctx, id)
					next[id] = time.Now().Add(30 * time.Second)
				}
			}
		}
	}()
}
func (a *autoAdopter) close() {
	if a.cancel != nil {
		a.cancel()
		<-a.done
	}
}

func (a *autoAdopter) scan(parent context.Context, agent string) {
	ctx, cancel := context.WithTimeout(parent, 3*time.Minute)
	defer cancel()
	list, err := a.ops.ComposeList(ctx, agent)
	if err != nil {
		return
	}
	records, e := a.db.ListComposeStacks(ctx, agent)
	if e != nil {
		return
	}
	known := make(map[string]store.ComposeStack, len(records))
	for _, r := range records {
		known[r.Name] = r
	}
	seen := make(map[string]bool)
	// 包括Agent已纳管但Hub落库未完成的项目，保证进程中断后可恢复登记。
	candidates := append(list.External, list.Projects...)
	for _, p := range candidates {
		if ctx.Err() != nil {
			return
		}
		if seen[p.Name] {
			continue
		}
		seen[p.Name] = true
		if !compose.ValidName(p.Name) {
			a.set(agent, p.Name, "skipped", "项目名称不符合管理规范，暂不能自动接管")
			continue
		}
		if r, ok := known[p.Name]; ok && !r.SyncedAt.IsZero() {
			continue
		}
		a.adopt(ctx, agent, p.Name, known)
	}
	// 清理已消失项目状态，避免项目名不断变化导致内存无限增长。
	a.mu.Lock()
	for n := range a.states[agent] {
		if !seen[n] {
			delete(a.states[agent], n)
		}
	}
	a.mu.Unlock()
}
func (a *autoAdopter) adopt(ctx context.Context, agent, name string, known map[string]store.ComposeStack) {
	a.set(agent, name, "pending", "正在读取并校验项目配置，不会重启或重建容器")
	source, cerr := a.ops.ComposeSource(ctx, agent, name)
	if cerr != nil {
		a.set(agent, name, "failed", "无法读取项目配置，请检查Agent版本、本地助手镜像及项目文件权限")
		return
	}
	if len(source.Blockers) > 0 {
		a.set(agent, name, "skipped", safeAutoReason(source.Blockers))
		return
	}
	if source.Name != name || compose.ValidateContent(source.Content) != nil || source.ConfigHash != compose.ProjectHash(source.Content, "") {
		a.set(agent, name, "skipped", "项目配置为空、超限或校验不一致，未自动登记配置")
		return
	}
	hash := source.ConfigHash
	if r, ok := known[name]; ok && (r.ConfigHash != hash || r.EnvContent != "") {
		a.set(agent, name, "skipped", "Hub已有同名配置且内容不同，保留已有记录，不自动覆盖")
		return
	}
	preview, cerr := a.ops.ComposePreview(ctx, agent, envelope.ComposePreviewPayload{Name: name, Content: source.Content})
	if cerr != nil {
		a.set(agent, name, "failed", "项目安全预检未完成，将在下次后台扫描重试")
		return
	}
	if !preview.CanImport || preview.ConfigHash != hash || len(preview.Blockers) > 0 {
		reason := "项目未通过安全预检，配置未登记；不影响直接管理容器"
		if len(preview.Blockers) > 0 {
			reason = safeAutoReason(preview.Blockers)
		}
		a.set(agent, name, "skipped", reason)
		return
	}
	// 先保存配置权威副本；失败不触发任何容器动作，重试也不覆盖同名记录。
	rec, ok := known[name]
	if !ok {
		now := time.Now().UTC()
		rec = store.ComposeStack{ID: "stk-" + randomSuffix(), AgentID: agent, Name: name, Content: source.Content, ConfigHash: hash, Version: 1, CreatedAt: now, UpdatedAt: now}
		if err := a.db.CreateComposeStack(ctx, rec); err != nil {
			if errors.Is(err, store.ErrConflict) {
				a.set(agent, name, "skipped", "同名项目已由其他操作登记，未覆盖")
			} else {
				a.set(agent, name, "failed", "保存项目登记失败，将在下次扫描重试")
			}
			return
		}
	}
	result, cerr := a.ops.ComposeAdopt(ctx, agent, envelope.ComposeAdoptPayload{Name: name, Content: source.Content, ExpectedContentHash: hash, Confirmation: true})
	if cerr != nil || !result.Adopted || result.ConfigHash != hash {
		a.set(agent, name, "failed", "配置已登记，但Agent尚未完成接管；不会启动或重建容器，将自动重试")
		return
	}
	if err := a.db.MarkComposeStackSynced(ctx, agent, name); err != nil {
		a.set(agent, name, "failed", "接管已完成但登记状态未保存，将自动重试")
		return
	}
	a.set(agent, name, "adopted", "已自动接管，未重启或重建容器")
}

// Agent只应返回脱敏原因；再限制数量与长度，避免无限量结果进入列表。
func safeAutoReason(reasons []string) string { return compose.SafeAutoReason(reasons) }
