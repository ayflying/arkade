package httpx

// handlers_compose.go 实现容器编排的增删改查与动作接口。
//
// 接口清单（都需要对目标 Agent 有权限，创建/删除要 manage，其余 operate）：
//   GET    /api/agents/{agentID}/stacks                      列出编排（Hub 记录 + Agent 实时状态）
//   POST   /api/agents/{agentID}/stacks                      创建 {name, content}
//   GET    /api/agents/{agentID}/stacks/{name}               详情
//   PUT    /api/agents/{agentID}/stacks/{name}               更新内容 {content, version}
//   DELETE /api/agents/{agentID}/stacks/{name}               删除
//   GET    /api/agents/{agentID}/stacks/{name}/config        只取 compose.yml 文本
//   POST   /api/agents/{agentID}/stacks/{name}/actions       统一动作入口
//   GET    /api/agents/{agentID}/stacks/{name}/logs          日志
//   GET    /api/agents/{agentID}/stacks/{name}/ps            容器状态
//
// 核心设计（必须理解，否则会改错）：
// **Hub 数据库是权威副本，Agent 本机是执行副本，同步方向单向 Hub→Agent。**
// 因此创建/更新一律「先写库，再下发」。下发失败不删记录、不回滚：
// 用户写的几十行编排不能因为一台服务器临时离线就丢掉；失败只体现为
// 状态与 drift 标志，用户可以稍后重试下发。

import (
	"context"
	"errors"
	"net/http"
	"sort"
	"time"

	"github.com/go-chi/chi/v5"

	"arkade/hub/internal/agentlink"
	"arkade/hub/internal/store"
	"arkade/hub/internal/user"
	"arkade/protocol/envelope"
)

// createStackRequest 是创建编排的请求体。
type createStackRequest struct {
	Name       string `json:"name"`
	Content    string `json:"content"`
	EnvContent string `json:"env_content"`
}

// handleCreateStack 处理 POST /api/agents/{agentID}/stacks。
//
// 流程严格按「先库后机」：写库成功后才下发。这样任何时刻都不会出现
// 「Agent 上跑着编排、Hub 里没有记录」的孤儿状态（那种状态无法在界面上管理）。
func handleCreateStack(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		agentID := agentIDFromPath(r)
		// 创建会新增一份要长期维护的编排，属于 manage 档。
		if !requirePermission(w, d, r, agentID, user.PermManage) {
			return
		}
		var req createStackRequest
		if !decodeAndValidate(w, d, r, &req) {
			return
		}
		if !requireComposeName(w, d, req.Name) {
			return
		}
		if err := validateComposeContent(req.Content); err != nil {
			badRequest(w, d, "%s", err.Error())
			return
		}

		if err := validateComposeEnv(req.EnvContent); err != nil {
			badRequest(w, d, "%s", err.Error())
			return
		}
		hash := composeProjectHash(req.Content, req.EnvContent)
		now := time.Now().UTC()
		rec := store.ComposeStack{
			ID: "stk-" + randomSuffix(), AgentID: agentID, Name: req.Name,
			Content: req.Content, EnvContent: req.EnvContent, ConfigHash: hash, Version: 1,
			CreatedAt: now, UpdatedAt: now,
		}
		if err := d.DB.CreateComposeStack(r.Context(), rec); err != nil {
			// 同 Agent 下项目名重复由仓储层归一化成 ErrConflict → 409。
			respondStoreError(w, d, err, "创建编排失败")
			return
		}

		// 下发。失败不影响已保存的记录：把结果放进响应让前端显示警告。
		opRes, syncOK, syncMsg := d.dispatchComposeUp(r, agentID, rec)
		d.Log.Info("创建编排", "agent", agentID, "name", rec.Name, "hash", hash, "下发成功", syncOK)

		respondJSON(w, d.Log, http.StatusOK, map[string]any{
			"stack":     toStackView(rec, nil),
			"sync_ok":   syncOK,
			"sync_hint": syncMsg,
			"result":    compoResultOrNil(opRes, syncOK),
		})
	}
}

// compoResultOrNil 在有 Agent 返回结果时转成视图，否则返回 nil。
func compoResultOrNil(res envelope.ComposeOpResult, ok bool) any {
	if !ok {
		return nil
	}
	return map[string]any{
		"status": res.Status,
		"output": truncateComposeOutput(res.Output),
	}
}

// dispatchComposeUp 把编排内容下发给 Agent 执行。
//
// 返回 (Agent 结果, 是否成功, 人类可读的提示)。
// 无论是 Agent 离线还是执行失败，都不返回错误给「创建/更新」这个动作——
// 因为记录已经保存成功，用户的操作目标（保存这份编排）已经达成；
// 下发失败是另一回事，用 sync_ok/sync_hint 表达，前端显示橙色警告即可。
func (d Deps) dispatchComposeUp(r *http.Request, agentID string, rec store.ComposeStack) (envelope.ComposeOpResult, bool, string) {
	spec := envelope.ComposeSpecPayload{
		Name: rec.Name, Content: rec.Content, EnvContent: rec.EnvContent, ContentHash: rec.ConfigHash,
	}
	res, cerr := d.Ops.ComposeUp(r.Context(), agentID, spec)
	if cerr != nil {
		msg := "编排已保存，但下发到 Agent 失败：" + cerr.Message
		if cerr.Code == envelope.CodeAgentOffline {
			msg = "编排已保存，但 Agent 当前离线，稍后可重新下发"
		}
		d.Log.Warn("下发编排失败，记录已保留", "agent", agentID, "name", rec.Name, "err", cerr)
		return envelope.ComposeOpResult{}, false, msg
	}

	// 下发成功后立刻记录 synced_at。
	// 写失败只记日志、不影响本次响应：synced_at 是辅助信息（界面用来区分
	// "已下发" 与 "只是保存了"），它写不进去不该让用户看到一次失败提示。
	if err := d.DB.MarkComposeStackSynced(r.Context(), agentID, rec.Name); err != nil {
		d.Log.Error("记录编排同步时间失败", "agent", agentID, "name", rec.Name, "err", err)
	}
	return res, true, ""
}

// updateStackRequest 是更新编排的请求体。
type updateStackRequest struct {
	Content string `json:"content"`
	// 省略保留旧.env，空字符串明确清空，避免旧客户端静默删除密钥。
	EnvContent *string `json:"env_content"`
	// Version 是乐观锁：必须是客户端读到的版本号。
	// 不提供"跳过校验"的开关——那等于允许静默覆盖别人的修改。
	Version int64 `json:"version"`
}

// handleUpdateStack 处理 PUT /api/agents/{agentID}/stacks/{name}。
func handleUpdateStack(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		agentID := agentIDFromPath(r)
		name := chi.URLParam(r, "name")
		if !requirePermission(w, d, r, agentID, user.PermManage) {
			return
		}
		if !requireComposeName(w, d, name) {
			return
		}
		var req updateStackRequest
		if !decodeAndValidate(w, d, r, &req) {
			return
		}
		if req.Version <= 0 {
			badRequest(w, d, "必须提供 version（乐观锁版本号）")
			return
		}
		if err := validateComposeContent(req.Content); err != nil {
			badRequest(w, d, "%s", err.Error())
			return
		}

		previous, err := d.DB.GetComposeStack(r.Context(), agentID, name)
		if err != nil {
			respondStoreError(w, d, err, "编排不存在")
			return
		}
		envContent := previous.EnvContent
		if req.EnvContent != nil {
			envContent = *req.EnvContent
		}
		if err := validateComposeEnv(envContent); err != nil {
			badRequest(w, d, "%s", err.Error())
			return
		}
		hash := composeProjectHash(req.Content, envContent)
		updated, ok, err := d.DB.UpdateComposeProject(r.Context(), agentID, name, req.Content, envContent, hash, req.Version, time.Now().UTC())
		if err != nil {
			respondStoreError(w, d, err, "编排不存在")
			return
		}
		if !ok {
			// 版本不匹配：期间有人改过。必须让用户重新加载，
			// 否则后保存的人会静默覆盖先保存的内容。
			respondError(w, d.Log, codeConflict,
				"编排内容已被他人修改，请刷新后重新编辑（当前版本与提交的 version 不一致）")
			return
		}

		opRes, syncOK, syncMsg := d.dispatchComposeUp(r, agentID, updated)
		d.Log.Info("更新编排", "agent", agentID, "name", name, "version", updated.Version, "下发成功", syncOK)
		respondJSON(w, d.Log, http.StatusOK, map[string]any{
			"stack":     toStackView(updated, nil),
			"sync_ok":   syncOK,
			"sync_hint": syncMsg,
			"result":    compoResultOrNil(opRes, syncOK),
		})
	}
}

// handleListStacks 处理 GET /api/agents/{agentID}/stacks。
//
// 合并两个来源：
//   - Hub 数据库记录（权威内容与哈希、版本号）——永远有；
//   - Agent 实时状态（本机是否真的存在、跑没跑、哈希是否一致）——离线时没有。
//
// Agent 离线时仍然返回 Hub 记录，状态标 offline。这是刻意的一致性：
// 现有的容器列表在 Agent 离线时也是返回列表 + 状态，不让用户以为"记录没了"。
func handleListStacks(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		agentID := agentIDFromPath(r)
		if !requirePermission(w, d, r, agentID, user.PermView) {
			return
		}

		records, err := d.DB.ListComposeStacks(r.Context(), agentID)
		if err != nil {
			respondStoreError(w, d, err, "查询编排列表失败")
			return
		}

		// 拉 Agent 实时状态。失败（离线）不是错误：降级为 offline 继续返回 Hub 记录。
		remote := map[string]envelope.ComposeProjectSummary{}
		// 数据来源仍分两组供旧客户端兼容，新界面按名称合并为一个列表。
		// 外部项目没有Hub配置，但容器依然可直接管理；不能因此调用工作区
		// 保存并部署/删除项目接口。动作按配置能力选择，不按列表位置猜测。
		externals := make([]envelope.ComposeProjectSummary, 0)
		agentOnline := true
		list, cerr := d.Ops.ComposeList(r.Context(), agentID)
		if cerr != nil {
			agentOnline = false
		} else {
			for _, p := range list.Projects {
				remote[p.Name] = p
			}
			externals = list.External
		}

		views := make([]composeStackView, 0, len(records))
		for _, rec := range records {
			var rp *envelope.ComposeProjectSummary
			if p, ok := remote[rec.Name]; ok {
				rp = &p
			}
			view := toStackView(rec, rp)
			if d.AutoAdopt != nil && rec.SyncedAt.IsZero() {
				if s, ok := d.AutoAdopt.recordedState(agentID, rec.Name); ok {
					view.SyncHint = s.Reason
				}
			}
			views = append(views, view)
		}

		resp := map[string]any{
			"agent_id":     agentID,
			"stacks":       views,
			"agent_online": agentOnline,
		}
		// 额外列出「Agent 上有、Hub 里没有」的项目：这类孤儿编排无法通过界面管理，
		// 必须显式暴露出来，否则用户只能上服务器手删。离线时不查，避免误导。
		if agentOnline {
			orphans := make([]string, 0)
			known := make(map[string]struct{}, len(records))
			for _, rec := range records {
				known[rec.Name] = struct{}{}
			}
			// 工作区里但 Hub 未登记的项 → orphans（保持原有语义与类型）。
			for name := range remote {
				if _, ok := known[name]; !ok {
					orphans = append(orphans, name)
				}
			}
			resp["orphans"] = orphans
			// external_stacks 给完整状态，供前端渲染「服务器已有编排（未纳管）」表格。
			//
			// 为什么 orphans 不直接改成对象数组：它已是既有的字符串数组契约，
			// 改动会让已发布的前端解析失败。新增字段而不是改旧字段。
			externalViews := toExternalStackViews(externals, known)
			if d.AutoAdopt != nil {
				for i := range externalViews {
					s := d.AutoAdopt.state(agentID, externalViews[i].Name)
					externalViews[i].AutoAdoptStatus = s.Status
					externalViews[i].AutoAdoptReason = s.Reason
				}
			}
			resp["external_stacks"] = externalViews
		}
		respondJSON(w, d.Log, http.StatusOK, resp)
	}
}

// externalStackView 是「服务器已有编排（未纳管）」的展示模型。
//
// 字段刻意只读：没有 id、没有 hash、没有 editable。
// 这不是漏写——外部项目没有可编辑的对象，前端也就不可能拼出
// 「保存/删除」请求。**用类型本身堵住误操作的入口**，
// 比在前端加个 if 判断可靠得多。
type externalStackView struct {
	Name string `json:"name"`
	// Status ∈ running / partial / stopped / unknown，与已纳管项目同一套取值。
	Status string `json:"status"`
	// Services 是服务名（已排序，顺序稳定）。
	Services []string `json:"services"`
	// Running / Total 让前端能显示「2/3 运行中」这类状态数量。
	Running int `json:"running"`
	Total   int `json:"total"`
	// Managed 恒为 false，且**显式给出**而不是省略。
	//
	// 前端可以据此统一走一套渲染逻辑（managed=false 就不显示操作按钮），
	// 而不是靠"这个对象没有 id"来推断——那种隐式判断在新增字段时极易失效。
	Managed         bool   `json:"managed"`
	AutoAdoptStatus string `json:"auto_adopt_status,omitempty"`
	AutoAdoptReason string `json:"auto_adopt_reason,omitempty"`
}

// toExternalStackViews 把 Agent 上报的外部项目转成展示模型。
//
// 会剔除与 Hub 已登记项目重名的项：同一个名字不可能既是"我们管理的"
// 又是"未纳管的"。若两侧都有，以已登记为准——否则用户会看到两条同名行，
// 一条能编辑一条不能，根本无法判断该点哪个。
//
// 按名称排序：Agent 侧已排过序，但这里仍显式再排一次，
// 因为去重后无法假设顺序，且前端可能只取部分列表做展示。
func toExternalStackViews(externals []envelope.ComposeProjectSummary, known map[string]struct{}) []externalStackView {
	out := make([]externalStackView, 0, len(externals))
	for _, p := range externals {
		if _, ok := known[p.Name]; ok {
			continue
		}
		services := p.Services
		if services == nil {
			services = []string{}
		}
		status := p.Status
		if status == "" {
			status = "unknown"
		}
		out = append(out, externalStackView{
			Name:     p.Name,
			Status:   status,
			Services: services,
			Running:  p.Running,
			Total:    p.Total,
			Managed:  false,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// toStackView 把 Hub 记录与 Agent 状态合并成视图。remote 为 nil 表示 Agent 离线或该项目在 Agent 上不存在。
func toStackView(rec store.ComposeStack, remote *envelope.ComposeProjectSummary) composeStackView {
	v := composeStackView{
		AgentID:    rec.AgentID,
		Name:       rec.Name,
		Managed:    true,
		ConfigHash: rec.ConfigHash,
		Version:    rec.Version,
		UpdatedAt:  rec.UpdatedAt.Unix(),
		Services:   []string{},
	}
	// 零值（未成功下发过）保持 0，前端据此区分"已下发"与"只保存了"。
	if !rec.SyncedAt.IsZero() {
		v.SyncedAt = rec.SyncedAt.Unix()
	}
	// 默认状态 offline：Agent 没报告就是不知道，不能乐观地写成 stopped。
	v.Status = composeProjectStatusOffline

	if remote == nil {
		return v
	}
	v.Status = remote.Status
	v.Services = remote.Services
	v.Running = remote.Running
	v.Total = remote.Total
	v.RemoteHash = remote.ConfigHash
	// drift 判定：两边都有哈希且不同才算偏离。
	// 远端哈希为空说明 Agent 上根本没有这个项目（还没下发成功），
	// 这种情况用 status=offline/unknown 表达更准确，不要误报成 drift。
	if remote.ConfigHash != "" && remote.ConfigHash != rec.ConfigHash {
		v.Drift = true
	}
	if v.Services == nil {
		v.Services = []string{}
	}
	return v
}

// handleGetStack 处理 GET /api/agents/{agentID}/stacks/{name}（详情）。
func handleGetStack(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		agentID := agentIDFromPath(r)
		name := chi.URLParam(r, "name")
		if !requirePermission(w, d, r, agentID, user.PermView) {
			return
		}
		if !requireComposeName(w, d, name) {
			return
		}

		rec, err := d.DB.GetComposeStack(r.Context(), agentID, name)
		if errors.Is(err, store.ErrNotFound) {
			handleUnifiedExternalDetail(w, r, d, agentID, name)
			return
		}
		if err != nil {
			respondStoreError(w, d, err, "编排不存在")
			return
		}

		// 尝试取实时状态：离线就只返回 Hub 侧信息。
		var remote *envelope.ComposeProjectSummary
		if list, cerr := d.Ops.ComposeList(r.Context(), agentID); cerr == nil {
			for _, p := range list.Projects {
				if p.Name == name {
					pp := p
					remote = &pp
					break
				}
			}
		}

		// 详情仅返回运行元数据；配置内容从manage专用/config接口读取。
		view := toStackView(rec, remote)
		if d.AutoAdopt != nil && rec.SyncedAt.IsZero() {
			if s, ok := d.AutoAdopt.recordedState(agentID, rec.Name); ok {
				view.SyncHint = s.Reason
			}
		}
		respondUnifiedStackDetail(w, r, d, agentID, name, view)
	}
}

// handleGetStackConfig 处理 GET /api/agents/{agentID}/stacks/{name}/config。
//
// 配置含.env敏感信息，仅manage可读取，JSON返回项目文件与版本，禁止缓存。
func handleGetStackConfig(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		agentID := agentIDFromPath(r)
		name := chi.URLParam(r, "name")
		if !requirePermission(w, d, r, agentID, user.PermManage) {
			return
		}
		if !requireComposeName(w, d, name) {
			return
		}
		rec, err := d.DB.GetComposeStack(r.Context(), agentID, name)
		if err != nil {
			respondStoreError(w, d, err, "编排不存在")
			return
		}
		respondSecretJSON(w, d.Log, http.StatusOK, map[string]any{
			"content": rec.Content, "env_content": rec.EnvContent, "version": rec.Version,
		})
	}
}

// handleDeleteStack 处理 DELETE /api/agents/{agentID}/stacks/{name}。
//
// 顺序是「先让 Agent 删，再删记录」——与创建相反。理由：
// 如果先删记录、Agent 那边删失败，就留下一个界面看不见但机器上还在跑、
// 还占着端口和卷的编排，用户彻底无法管理。反之若 Agent 已删、删记录失败，
// 下次列表会显示 drift/孤儿，还能再点一次删除。
//
// Agent离线或执行失败时保留记录，避免仍运行的服务从管理界面消失。
// 默认保留卷，删除数据卷必须是明确的独立勾选。
func handleDeleteStack(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		agentID := agentIDFromPath(r)
		name := chi.URLParam(r, "name")
		if !requirePermission(w, d, r, agentID, user.PermManage) {
			return
		}
		if !requireComposeName(w, d, name) {
			return
		}

		rec, err := d.DB.GetComposeStack(r.Context(), agentID, name)
		if err != nil {
			respondStoreError(w, d, err, "编排不存在")
			return
		}

		removeVolumes, valid := parseBoolQuery(r.URL.Query().Get("remove_volumes"))
		if !valid {
			badRequest(w, d, "remove_volumes必须是true或false")
			return
		}
		// 默认保留数据卷，只有用户明确确认时才允许删除。
		opRes, cerr := d.Ops.ComposeRemove(r.Context(), agentID, envelope.ComposeOpPayload{
			Name: name, RemoveVolumes: removeVolumes,
		})
		syncOK := cerr == nil
		syncMsg := ""
		if cerr != nil {
			// 删除执行失败必须保留管理记录，否则仍在运行的项目会从界面消失。
			respondError(w, d.Log, cerr.Code, "删除未完成，编排记录已保留："+cerr.Message)
			return
		}

		if err := d.DB.DeleteComposeStack(r.Context(), agentID, name); err != nil {
			respondStoreError(w, d, err, "编排不存在")
			return
		}
		d.Log.Info("删除编排", "agent", agentID, "name", name, "下发成功", syncOK)

		respondJSON(w, d.Log, http.StatusOK, map[string]any{
			"name":        name,
			"sync_ok":     syncOK,
			"sync_hint":   syncMsg,
			"result":      compoResultOrNil(opRes, syncOK),
			"config_hash": rec.ConfigHash,
		})
	}
}

// actionRequest 是统一动作入口的请求体。
type actionRequest struct {
	// Action ∈ up / down / stop / restart / pull / rebuild / remove / backup
	Action        string   `json:"action"`
	Services      []string `json:"services"`
	RemoveVolumes bool     `json:"remove_volumes"`
	// IncludeVolumes 仅 backup 使用。
	IncludeVolumes bool `json:"include_volumes"`
}

// handleStackAction 处理 POST /api/agents/{agentID}/stacks/{name}/actions。
//
// 所有动作共用一个入口而不是八个路由：动作集的语义是"针对同一份编排的
// 不同操作"，前端只需要一个动作分发器；路径爆炸对前后端都是负担。
//
// 权限分两档：只读类不改系统（其实没有只读动作），
// up/restart/rebuild/remove/down/backup 都会改变运行状态或产生副作用，
// 统一要求 operate；其中 remove（删文件+删卷）与 backup 之外的 down -v
// 属于更危险的操作，这里按 operate 放行、由 remove 单独走 DELETE 接口要求 manage。
func handleStackAction(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		agentID := agentIDFromPath(r)
		name := chi.URLParam(r, "name")
		if !requirePermission(w, d, r, agentID, user.PermOperate) {
			return
		}
		if !requireComposeName(w, d, name) {
			return
		}
		var req actionRequest
		if !decodeAndValidate(w, d, r, &req) {
			return
		}

		// 编排必须存在于 Hub：否则用户可能在 Agent 上操作一个"界面里没有"的项目，
		// 状态无法回到 Hub。这是「Hub 是权威副本」的直接推论。
		rec, err := d.DB.GetComposeStack(r.Context(), agentID, name)
		if err != nil {
			respondStoreError(w, d, err, "编排不存在")
			return
		}

		opPayload := envelope.ComposeOpPayload{
			Name:          name,
			Services:      req.Services,
			RemoveVolumes: req.RemoveVolumes,
			Force:         req.Action == "rebuild",
		}

		ctx := r.Context()
		switch req.Action {
		case "up":
			// up 必须重新下发内容：Agent 本机文件可能被别人改过或丢了，
			// 直接执行 up 会跑起一份与 Hub 记录不一致的编排。
			spec := envelope.ComposeSpecPayload{Name: rec.Name, Content: rec.Content, EnvContent: rec.EnvContent, ContentHash: rec.ConfigHash}
			res, cerr := d.Ops.ComposeUp(ctx, agentID, spec)
			if cerr != nil {
				respondConnError(w, d, cerr)
				return
			}
			respondJSON(w, d.Log, http.StatusOK, composeOpResultToView(agentID, res))

		case "down":
			// down 默认保留卷；只有明确要求才删。数据卷里可能是数据库，
			// 误删不可恢复，因此默认值必须是"保留"。
			res, cerr := d.Ops.ComposeDown(ctx, agentID, opPayload)
			if cerr != nil {
				respondConnError(w, d, cerr)
				return
			}
			respondJSON(w, d.Log, http.StatusOK, composeOpResultToView(agentID, res))

		case "stop", "restart", "pull", "rebuild":
			res, cerr := d.dispatchSimpleAction(ctx, agentID, req.Action, opPayload)
			if cerr != nil {
				respondConnError(w, d, cerr)
				return
			}
			respondJSON(w, d.Log, http.StatusOK, composeOpResultToView(agentID, res))

		case "remove":
			// remove 会删文件并连带删卷，权限要求提到 manage。
			// 已经在上面按 operate 校验过，这里再严格校验一次。
			if !requirePermission(w, d, r, agentID, user.PermManage) {
				return
			}
			res, cerr := d.Ops.ComposeRemove(ctx, agentID, opPayload)
			if cerr != nil {
				respondConnError(w, d, cerr)
				return
			}
			// 记录也一并删掉：动作语义是"删除这个编排"。
			if err := d.DB.DeleteComposeStack(ctx, agentID, name); err != nil && err != store.ErrNotFound {
				d.Log.Warn("删除编排记录失败", "agent", agentID, "name", name, "err", err)
			}
			respondJSON(w, d.Log, http.StatusOK, composeOpResultToView(agentID, res))

		case "backup":
			res, cerr := d.Ops.ComposeBackup(ctx, agentID, envelope.ComposeBackupPayload{
				Name: name, IncludeVolumes: req.IncludeVolumes,
			})
			if cerr != nil {
				respondConnError(w, d, cerr)
				return
			}
			respondJSON(w, d.Log, http.StatusOK, map[string]any{
				"name":       res.Name,
				"path":       res.Path,
				"size_bytes": res.SizeBytes,
				"files":      res.Files,
				"created_at": res.CreatedAt,
			})

		default:
			badRequest(w, d, "不支持的 action %q（可用：up/down/stop/restart/pull/rebuild/remove/backup）", req.Action)
		}
	}
}

// dispatchSimpleAction 是 stop/restart/pull/rebuild 共用的下发实现。
//
// 这四个动作的请求载荷与结果结构完全一致，只是协议方法名不同；
// 集中在一处避免四段近乎相同的分支各自演化（改一处漏三处的典型来源）。
// 方法名的映射在这里用显式 switch：写错方法名的代价是运行时才发现的
// "Agent 不支持该方法"，比多写四行 case 严重得多。
func (d Deps) dispatchSimpleAction(ctx context.Context, agentID, action string, req envelope.ComposeOpPayload) (envelope.ComposeOpResult, *agentlink.ConnError) {
	switch action {
	case "stop":
		return d.Ops.ComposeStop(ctx, agentID, req)
	case "restart":
		return d.Ops.ComposeRestart(ctx, agentID, req)
	case "pull":
		return d.Ops.ComposePull(ctx, agentID, req)
	case "rebuild":
		return d.Ops.ComposeRebuild(ctx, agentID, req)
	default:
		// 不可达：调用方已经用 action 值做过分支。
		return envelope.ComposeOpResult{}, &agentlink.ConnError{
			Code: envelope.CodeBadRequest, Message: "不支持的动作: " + action,
		}
	}
}

// handleStackLogs 处理 GET /api/agents/{agentID}/stacks/{name}/logs?service=&tail=。
func handleStackLogs(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		agentID := agentIDFromPath(r)
		name := chi.URLParam(r, "name")
		if !requirePermission(w, d, r, agentID, user.PermView) {
			return
		}
		if !requireComposeName(w, d, name) {
			return
		}
		if _, err := d.DB.GetComposeStack(r.Context(), agentID, name); err != nil {
			respondStoreError(w, d, err, "编排不存在")
			return
		}

		service := r.URL.Query().Get("service")
		if service != "" && !validComposeName(service) {
			// 服务名同样会拼进命令行参数，用与项目名一致的白名单校验。
			badRequest(w, d, "服务名不合法: %q", service)
			return
		}
		tail, err := parseTail(r.URL.Query().Get("tail"))
		if err != nil {
			badRequest(w, d, "tail 必须是 1 到 %d 之间的整数", maxLogTail)
			return
		}

		res, cerr := d.Ops.ComposeLogs(r.Context(), agentID, envelope.ComposeLogsPayload{
			Name: name, Service: service, TailLines: tail,
		})
		if cerr != nil {
			respondConnError(w, d, cerr)
			return
		}
		lines := res.Lines
		if lines == nil {
			lines = []string{}
		}
		respondJSON(w, d.Log, http.StatusOK, map[string]any{
			"lines":     lines,
			"truncated": res.Truncated,
		})
	}
}

// handleStackPS 处理 GET /api/agents/{agentID}/stacks/{name}/ps。
//
// 返回结构与容器列表一致（agent_id + containers），前端复用同一套渲染。
func handleStackPS(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		agentID := agentIDFromPath(r)
		name := chi.URLParam(r, "name")
		if !requirePermission(w, d, r, agentID, user.PermView) {
			return
		}
		if !requireComposeName(w, d, name) {
			return
		}
		if _, err := d.DB.GetComposeStack(r.Context(), agentID, name); err != nil {
			respondStoreError(w, d, err, "编排不存在")
			return
		}
		res, cerr := d.Ops.ComposePS(r.Context(), agentID, envelope.ComposeOpPayload{Name: name})
		if cerr != nil {
			respondConnError(w, d, cerr)
			return
		}
		containers := res.Containers
		if containers == nil {
			containers = []envelope.ContainerSummary{}
		}
		respondJSON(w, d.Log, http.StatusOK, map[string]any{
			"agent_id":   agentID,
			"containers": containers,
		})
	}
}
