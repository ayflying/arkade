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
//
// ============================ 节奏控制（见 auto_adopt_policy.go） ============================
//
// 每轮扫描对每个**尚未接管**的外部项目都要读一次配置，而在 Agent 侧
// 读一次配置 = 创建一个一次性 helper 容器。因此这里必须做到：
//   - 已接管 / 确定性阻塞 / 重试次数用尽的项目：**完全不读**；
//   - 环境性失败：指数退避，到点才读；
//   - 结论落库（auto_adopt_states），进程重启不会把失败项全部重扫一遍。
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

// autoAdoptTickInterval 是**检查**间隔，不是**扫描**间隔。
//
// 2 秒只决定"多久看一次某个 Agent 是不是到点了"，真正决定读取频率的是
// 下面 perAgentInterval 以及每个项目自己的退避时间。
const autoAdoptTickInterval = 2 * time.Second

// autoAdoptPerAgentInterval 是"同一台 Agent 两轮扫描之间"的最小间隔。
//
// 30 秒保持不变：外部项目是低频变化的东西，再密没有意义；
// 而真正把创建量压下去的，是每轮里"哪些项目压根不读"。
const autoAdoptPerAgentInterval = 30 * time.Second

func newAutoAdopter(db *store.DB, ops autoAdoptBackend, online func() map[string]struct{}) *autoAdopter {
	return &autoAdopter{db: db, ops: ops, online: online, states: make(map[string]map[string]autoAdoptState)}
}
func (a *autoAdopter) recordedState(agent, name string) (autoAdoptState, bool) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if s, ok := a.states[agent][name]; ok {
		return s, true
	}
	// 内存里没有（例如刚重启）时回落到库里的结论：
	// 不回落的话，界面上会显示"等待后台扫描"，而实际上它已经明确不再扫了。
	if a.db == nil {
		return autoAdoptState{}, false
	}
	prev, err := a.db.GetAutoAdoptState(context.Background(), agent, name)
	if err != nil {
		return autoAdoptState{}, false
	}
	return autoAdoptState{Status: prev.Status, Reason: prev.Reason}, true
}
func (a *autoAdopter) state(agent, name string) autoAdoptState {
	if s, ok := a.recordedState(agent, name); ok {
		return s
	}
	return autoAdoptState{Status: autoAdoptStatusPending, Reason: "等待后台扫描，兼容项目将自动登记配置；容器仍可直接管理"}
}
func (a *autoAdopter) set(agent, name, status, reason string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.states[agent] == nil {
		a.states[agent] = make(map[string]autoAdoptState)
	}
	a.states[agent][name] = autoAdoptState{status, reason}
}

// forget 清掉内存里的结论，使下一次请求回落到库里的状态。
func (a *autoAdopter) forget(agent, name string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.states[agent], name)
}

// persist 把结论写入库，同时更新内存镜像。
//
// 落库失败不改变内存结论：本轮扫描的行为已经确定了，
// 只是下一轮/重启后会重新扫一次——那是可以接受的降级。
func (a *autoAdopter) persist(ctx context.Context, agent, name string, s store.AutoAdoptState) {
	a.set(agent, name, s.Status, s.Reason)
	if a.db == nil {
		return
	}
	if err := a.db.SaveAutoAdoptState(ctx, agent, name, s); err != nil {
		// 存不下不影响本轮判断；不在这里打日志是因为 autoAdopter 没有 logger，
		// 而把 logger 传进来只为一行调试信息不值得。
		return
	}
}

func (a *autoAdopter) start(parent context.Context) {
	ctx, cancel := context.WithCancel(parent)
	a.cancel = cancel
	a.done = make(chan struct{})
	go func() {
		defer close(a.done)
		ticker := time.NewTicker(autoAdoptTickInterval)
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
					next[id] = time.Now().Add(autoAdoptPerAgentInterval)
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
	// 一次取出该 Agent 的全部扫描结论：逐个查库会让每轮产生 N 次查询。
	// 读失败按"没有任何结论"处理——最坏情况只是多扫一轮，不会误判成已接管。
	prevStates, perr := a.db.ListAutoAdoptStates(ctx, agent)
	if perr != nil {
		prevStates = map[string]store.AutoAdoptState{}
	}

	seen := make(map[string]bool)
	now := time.Now()
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
			// 名字不合规是**确定性**结论：不落库也不重扫，
			// 但仍在内存里给出原因，界面才能解释为什么它没被接管。
			a.set(agent, p.Name, store.AutoAdoptStatusSkipped, "项目名称不符合管理规范，暂不能自动接管")
			continue
		}
		if r, ok := known[p.Name]; ok && !r.SyncedAt.IsZero() {
			// 已接管：这是最关键的跳过点。每跳过一个，就少建一个 helper 容器。
			continue
		}
		prev, has := prevStates[p.Name]
		if !autoAdoptShouldScan(prev, has, now) {
			// 不重扫，但要把库里的结论镜像到内存，让界面显示真实原因。
			a.set(agent, p.Name, prev.Status, prev.Reason)
			continue
		}
		a.adopt(ctx, agent, p.Name, known, prev, has)
	}

	a.prune(ctx, agent, seen, prevStates)
}

// prune 清理已消失项目的结论。
//
// 内存与库都要清：库里的残留结论会在项目名被重新创建时把它挡住
// （例如老的"多配置文件"结论落到一个全新的项目上），那是很难归因的错误。
func (a *autoAdopter) prune(ctx context.Context, agent string, seen map[string]bool, prevStates map[string]store.AutoAdoptState) {
	a.mu.Lock()
	for n := range a.states[agent] {
		if !seen[n] {
			delete(a.states[agent], n)
		}
	}
	a.mu.Unlock()

	keep := make([]string, 0, len(prevStates))
	for n := range prevStates {
		if seen[n] {
			keep = append(keep, n)
		}
	}
	if len(keep) == len(prevStates) {
		// 没有需要清理的行，省掉一次 DELETE。
		return
	}
	if err := a.db.DeleteAutoAdoptStatesExcept(ctx, agent, keep); err != nil {
		return
	}
}

func (a *autoAdopter) adopt(ctx context.Context, agent, name string, known map[string]store.ComposeStack, prev store.AutoAdoptState, has bool) {
	a.set(agent, name, autoAdoptStatusPending, "正在读取并校验项目配置，不会重启或重建容器")
	source, cerr := a.ops.ComposeSource(ctx, agent, name)
	if cerr != nil {
		// 区分环境性失败与确定性失败：前者退避重试，后者不再重扫。
		// 这一条判断是"容器数量"的关键——环境性原因若被判成确定性的，
		// 用户修好之后后台也永远不会再试。
		retryable := autoAdoptRetryable(cerr)
		a.persist(ctx, agent, name, autoAdoptFailureState(prev, has, retryable,
			readFailureReason(cerr, retryable), time.Now()))
		return
	}
	if len(source.Blockers) > 0 {
		// 拿到 Blockers 说明 Agent 已经**确定**这个项目不能接管：
		// 多配置文件、工作目录不在白名单、符号链接、标签不一致等。
		// 同样输入必然同样失败，所以按确定性阻塞处理，不再自动重扫。
		a.persist(ctx, agent, name, autoAdoptFailureState(prev, has, false, safeAutoReason(source.Blockers), time.Now()))
		return
	}
	if source.Name != name || compose.ValidateContent(source.Content) != nil || source.ConfigHash != compose.ProjectHash(source.Content, "") {
		a.persist(ctx, agent, name, autoAdoptFailureState(prev, has, false,
			"项目配置为空、超限或校验不一致，未自动登记配置", time.Now()))
		return
	}
	hash := source.ConfigHash
	if r, ok := known[name]; ok && (r.ConfigHash != hash || r.EnvContent != "") {
		// 用户已在 Hub 侧保存了同名配置：这是不可自动改变的结论，
		// 并且属于"用户意图"，不该被任何自动流程覆盖。
		a.persist(ctx, agent, name, store.AutoAdoptState{
			Status:        store.AutoAdoptStatusSkipped,
			Reason:        "Hub已有同名配置且内容不同，保留已有记录，不自动覆盖",
			Attempts:      prev.Attempts + 1,
			LastScannedAt: time.Now(),
		})
		return
	}
	preview, cerr := a.ops.ComposePreview(ctx, agent, envelope.ComposePreviewPayload{Name: name, Content: source.Content})
	if cerr != nil {
		a.persist(ctx, agent, name, autoAdoptFailureState(prev, has, autoAdoptRetryable(cerr),
			"项目安全预检未完成，将按退避重试", time.Now()))
		return
	}
	if !preview.CanImport || preview.ConfigHash != hash || len(preview.Blockers) > 0 {
		reason := "项目未通过安全预检，配置未登记；不影响直接管理容器"
		if len(preview.Blockers) > 0 {
			reason = safeAutoReason(preview.Blockers)
		}
		a.persist(ctx, agent, name, autoAdoptFailureState(prev, has, false, reason, time.Now()))
		return
	}
	// 先保存配置权威副本；失败不触发任何容器动作，重试也不覆盖同名记录。
	rec, ok := known[name]
	if !ok {
		now := time.Now().UTC()
		rec = store.ComposeStack{ID: "stk-" + randomSuffix(), AgentID: agent, Name: name, Content: source.Content, ConfigHash: hash, Version: 1, CreatedAt: now, UpdatedAt: now}
		if err := a.db.CreateComposeStack(ctx, rec); err != nil {
			if errors.Is(err, store.ErrConflict) {
				a.persist(ctx, agent, name, store.AutoAdoptState{
					Status:        store.AutoAdoptStatusSkipped,
					Reason:        "同名项目已由其他操作登记，未覆盖",
					Attempts:      prev.Attempts + 1,
					LastScannedAt: time.Now(),
				})
			} else {
				a.persist(ctx, agent, name, autoAdoptFailureState(prev, has, true,
					"保存项目登记失败，将按退避重试", time.Now()))
			}
			return
		}
	}
	result, cerr := a.ops.ComposeAdopt(ctx, agent, envelope.ComposeAdoptPayload{Name: name, Content: source.Content, ExpectedContentHash: hash, Confirmation: true})
	if cerr != nil || !result.Adopted || result.ConfigHash != hash {
		a.persist(ctx, agent, name, autoAdoptFailureState(prev, has, true,
			"配置已登记，但Agent尚未完成接管；不会启动或重建容器，将按退避重试", time.Now()))
		return
	}
	if err := a.db.MarkComposeStackSynced(ctx, agent, name); err != nil {
		a.persist(ctx, agent, name, autoAdoptFailureState(prev, has, true,
			"接管已完成但登记状态未保存，将按退避重试", time.Now()))
		return
	}
	// 成功接管：把之前的失败结论清掉，否则下次若 synced_at 被重置，
	// 老结论（例如"重试次数用尽"）会把它永久挡住。
	// 清库失败不作为失败上报：接管本身已经成功，残留的结论行会在
	// 项目下次扫描判定（synced_at 非零）时被忽略，且 prune 也会收走它。
	_ = a.db.DeleteAutoAdoptState(ctx, agent, name)
	a.set(agent, name, "adopted", "已自动接管，未重启或重建容器")
}

// RetryAutoAdopt 清掉某个项目的自动发现结论，让它下一轮重新被扫描。
//
// 供"用户手动重试"使用：确定性阻塞与次数用尽的项目都只能这样恢复。
func (a *autoAdopter) RetryAutoAdopt(ctx context.Context, agent, name string) error {
	if a.db == nil {
		return errors.New("自动发现未启用")
	}
	if err := a.db.DeleteAutoAdoptState(ctx, agent, name); err != nil {
		return err
	}
	a.forget(agent, name)
	return nil
}

// Agent只应返回脱敏原因；再限制数量与长度，避免无限量结果进入列表。
func safeAutoReason(reasons []string) string { return compose.SafeAutoReason(reasons) }
