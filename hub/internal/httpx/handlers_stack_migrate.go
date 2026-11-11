package httpx

// handlers_stack_migrate.go：编排跨服务器迁移的 HTTP 接口。
//
// ============================ 契约（与前端已对齐，不得变更） ============================
//
//	POST /api/agents/{agentID}/stacks/{name}/migrate
//	  body: { target_agent_id, include_volumes, start_after, confirmation }
//	  resp: { task: <编排操作任务结构> }   ← task.name 是源编排名，task.action = "migrate"
//
//	GET  /api/agents/{agentID}/stacks/{name}/operation-tasks/{taskID}?after=N
//
// ============================ 为什么进度复用 operation-tasks ============================
// 前端已按 compose 编排操作任务写好日志弹窗。迁移的用户认知与它完全一致：
// "一次编排操作，会打出多条进度行"。另开端点意味着前端要写第二个弹窗、
// 第二套轮询、第二种错误展示——而它本来没必要存在。
//
// 因此 operation-tasks 的 GET 在"Agent 侧查不到该任务"时回落到 Hub 的迁移任务表。
// 这个回落是**必要**的：迁移的全局进度只可能存在于 Hub，
// Agent 端只看到自己那一段。
//
// ============================ 404 的语义 ============================
// 前端约定：该端点 404 → 显示「后端尚未部署」而不是「迁移失败」。
// 因此这里必须极度小心：**只有"真的没有这个任务"才回 404**，
// 权限不足必须回 403、参数错误必须回 400、两端状态问题必须回 409，
// 否则用户会被告知"功能没部署"，而实际上是他的权限不够。

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"arkade/hub/internal/stackmigrate"
	"arkade/hub/internal/user"
)

// migrateRequest 是迁移请求体。
type migrateRequest struct {
	// TargetAgentID 是目标服务器标识。
	TargetAgentID string `json:"target_agent_id"`
	// IncludeVolumes 表示是否同时迁移数据卷（**默认 false**）。
	//
	// 用 bool 而非 *bool：前端总是显式传这个字段（它的默认值是 false），
	// 因此"没传"与"传 false"在产品上等价——都是不迁卷。
	// 这个方向是安全的：少了卷不会毁数据，多了卷才会。
	IncludeVolumes bool `json:"include_volumes"`
	// StartAfter 表示迁移完成后是否启动（**默认 false**）。
	StartAfter bool `json:"start_after"`
	// Confirmation 是用户的显式确认标记。
	//
	// ============================ 为什么必须有它 ============================
	// 迁移会在目标上**创建**编排与数据卷，且不可自动回滚。
	// 要求前端传一个明确的确认值，能挡住"误点"和"脚本里循环调用"。
	// 值本身不重要（前端传的是编排名），重要的是它必须非空。
	Confirmation string `json:"confirmation"`
	// RequestID 是幂等键（前端可选传）。
	RequestID string `json:"request_id,omitempty"`
}

// handleStartStackMigrate 处理 POST .../migrate。
func handleStartStackMigrate(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		agent, name := agentIDFromPath(r), pathParam(r, "name")
		// 迁移会**创建**目标端编排与卷，属于管理级操作。
		if !requirePermission(w, d, r, agent, user.PermManage) || !requireComposeName(w, d, name) {
			return
		}

		var req migrateRequest
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&req); err != nil {
			badRequest(w, d, "请求内容不是合法的 JSON")
			return
		}

		target := strings.TrimSpace(req.TargetAgentID)
		if target == "" {
			badRequest(w, d, "必须指定目标服务器")
			return
		}
		if target == agent {
			badRequest(w, d, "目标服务器不能与源服务器相同")
			return
		}
		// 确认标记必须非空且与编排名一致：前端传的正是编排名。
		//
		// 为什么要比对而不是只判非空：一个写死 "yes" 的客户端也能通过
		// "非空"检查，但它显然没有真的让用户确认"迁移哪个编排"。
		// 要求等于编排名，等于要求调用方知道自己在迁什么。
		if strings.TrimSpace(req.Confirmation) != name {
			badRequest(w, d, "请确认要迁移的编排名称，确认值必须与编排名一致")
			return
		}

		// 目标服务器也必须在该用户的管理范围内。
		//
		// 这一步在编排器的阶段 1 还会再查一次（那是给并发场景兜底的）；
		// 这里提前查是为了在**开启任务之前**就拒绝，
		// 否则用户会看到一个"失败的迁移任务"，而它其实从未开始。
		if !requirePermission(w, d, r, target, user.PermManage) {
			return
		}

		if d.StackMigrate == nil {
			// 编排器未装配：这是部署配置问题，不是"功能没部署"。
			// 用 500 而不是 404——404 会被前端说成「后端尚未部署」，
			// 而实际上后端在运行、只是这个能力没接上。
			respondError(w, d.Log, "internal", "迁移功能未启用（服务端未装配迁移编排器）")
			return
		}

		owner := ""
		if u, ok := userFrom(r.Context()); ok {
			owner = u.ID
		}

		snap, err := d.StackMigrate.Submit(r.Context(), stackmigrate.Options{
			StackName:      name,
			Source:         agent,
			Target:         target,
			IncludeVolumes: req.IncludeVolumes,
			StartAfter:     req.StartAfter,
			RequestID:      req.RequestID,
			OwnerID:        owner,
		})
		if err != nil {
			switch {
			case errors.Is(err, stackmigrate.ErrTooManyTasks):
				respondError(w, d.Log, "conflict", err.Error())
			case errors.Is(err, stackmigrate.ErrPairBusy):
				respondError(w, d.Log, "conflict", err.Error())
			case errors.Is(err, stackmigrate.ErrDuplicateRequest):
				respondError(w, d.Log, "conflict", err.Error())
			default:
				respondError(w, d.Log, "internal", "启动迁移失败")
			}
			return
		}

		// 审计：记录源、目标、是否含卷、镜像/卷数量。
		//
		// 在**提交时**记一次，而不是等任务结束：任务可能跑几十分钟，
		// 而审计需要回答的是"谁在什么时候发起了什么"。
		// 数量此刻还未知，先记选择；最终结果由任务事件流体现。

		respondSecretJSON(w, d.Log, http.StatusAccepted, map[string]any{
			"task": migrateTaskView(agent, name, snap),
		})
	}
}

// migrateTaskView 把迁移任务快照转成**编排操作任务结构**。
//
// ============================ 为什么必须逐字段映射 ============================
// 前端复用的是 compose 编排操作任务的视图解析代码，它读的字段名是
// task_id/name/action/status/output/next_cursor/error 等。
// 返回一套"自己的"结构会让前端拿到一个字段全空的对象，
// 表现为"弹窗打开但什么都不显示"——这类问题排查起来极其耗时。
//
// task.action 固定为 "migrate"：前端据此显示"迁移"而不是"启动/重建"。
// task.name 必须是**源编排名**：前端用它匹配"我正在看哪个编排的弹窗"。
func migrateTaskView(agentID, name string, snap stackmigrate.Snapshot) map[string]any {
	output := make([]map[string]any, 0, len(snap.Events))
	for _, ev := range snap.Events {
		output = append(output, map[string]any{
			"seq":    ev.Seq,
			"stream": ev.Stream,
			"text":   ev.Text,
		})
	}

	view := map[string]any{
		"task_id":     snap.TaskID,
		"agent_id":    agentID,
		"name":        name,
		"action":      "migrate",
		"status":      snap.Status,
		"next_cursor": snap.NextCursor,
		"output":      output,
	}
	if snap.Truncated {
		view["truncated"] = true
	}
	if snap.Error != "" {
		view["error"] = snap.Error
	}
	if !snap.StartedAt.IsZero() {
		view["started_at"] = snap.StartedAt.UTC().Format(time.RFC3339)
	}
	if !snap.FinishedAt.IsZero() {
		view["finished_at"] = snap.FinishedAt.UTC().Format(time.RFC3339)
	}

	// ============================ 迁移特有字段（附加，不冲突） ============================
	// 前端不认这些字段也不会坏：它只读自己认识的那些。
	// 但把它们带上，让"迁移"这个动作的信息完整（谁迁到哪、跳过了什么）。
	view["migrate"] = map[string]any{
		"source_agent_id": snap.Source,
		"target_agent_id": snap.Target,
		"include_volumes": snap.IncludeVolumes,
		"start_after":     snap.StartAfter,
		"stage":           snap.Stage,
		"stage_title":     snap.StageTitle,
		"image_count":     snap.ImageCount,
		"volume_count":    snap.VolumeCount,
		"skipped_volumes": snap.SkippedVolumes,
		"bind_dirs":       snap.BindDirs,
		"residual": map[string]any{
			"images_imported":  snap.Residual.ImagesImported,
			"volumes_restored": snap.Residual.VolumesRestored,
			"compose_written":  snap.Residual.ComposeWritten,
			"started":          snap.Residual.Started,
		},
	}
	return view
}

// 审计由 authz 中间件按路由表自动记录（见 audit_routes.go 的
// "/stacks/{name}/migrate" 条目）。这里**不再手写**一次：
// 手写第二遍会产生两条重复记录，而审计列表上看到同一件事出现两次，
// 用户会以为是"操作执行了两次"，那是很严重的误导。

// handleGetMigrateTask 是 operation-tasks GET 的**回落分支**：
// 当 Agent 侧没有该任务时，尝试把它当作 Hub 侧的迁移任务读取。
//
// 返回 handled=false 表示"确实不是迁移任务"，调用方应继续走原来的 404 路径。
func handleGetMigrateTask(d Deps, w http.ResponseWriter, r *http.Request,
	agent, name, taskID string) (handled bool) {
	if d.StackMigrate == nil {
		return false
	}
	after := int64(0)
	if raw := r.URL.Query().Get("after"); raw != "" {
		v, err := parseInt64(raw)
		if err != nil || v < 0 {
			badRequest(w, d, "输出游标不合法")
			return true
		}
		after = v
	}

	snap, err := d.StackMigrate.Get(taskID, after)
	if err != nil {
		if errors.Is(err, stackmigrate.ErrTaskNotFound) {
			// 真的没有这个任务：交给调用方回 404（前端会显示「后端尚未部署」）。
			return false
		}
		respondError(w, d.Log, "internal", "读取迁移任务失败")
		return true
	}
	// ============================ 归属校验 ============================
	// 任务必须属于路径里的这个编排与这台服务器，否则就是有人拿别人的
	// task_id 来探测。不匹配时**当成不存在**（404），
	// 而不是 403——后者会泄露"这个 task_id 是存在的"。
	if snap.Name != name || snap.Source != agent {
		return false
	}

	respondSecretJSON(w, d.Log, http.StatusOK, map[string]any{"task": migrateTaskView(agent, name, snap)})
	return true
}

// parseInt64 解析十进制整数。
func parseInt64(s string) (int64, error) {
	var v int64
	if s == "" {
		return 0, errors.New("空")
	}
	neg := false
	i := 0
	if s[0] == '-' {
		neg = true
		i = 1
	}
	if i >= len(s) {
		return 0, errors.New("非法")
	}
	for ; i < len(s); i++ {
		c := s[i]
		if c < '0' || c > '9' {
			return 0, errors.New("非法")
		}
		v = v*10 + int64(c-'0')
	}
	if neg {
		v = -v
	}
	return v, nil
}
