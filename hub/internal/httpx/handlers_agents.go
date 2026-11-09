package httpx

// handlers_agents.go 实现 Agent 列表与容器相关接口。
//
// 职责边界（严格遵守）：
//   - handler 只做参数提取、权限校验调用、调用 ops、写响应；
//   - 不直接碰 store，不直接构造协议载荷；
//   - Agent 离线时立刻返回 agent_offline（503），不排队等待（02 文档 1 节）。

import (
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"arkade/hub/internal/agentlink"
	"arkade/hub/internal/store"
	"arkade/hub/internal/user"
	"arkade/protocol/envelope"
)

// agentView 是返回给前端的 Agent 视图。
// 字段名与前端契约严格一致，last_seen 单位为 Unix 秒。
type agentView struct {
	ID string `json:"id"`
	// Name 是 Agent 上报的主机名，属于机器属性，用户改不了。
	Name string `json:"name"`
	// DisplayName 是用户可编辑的显示名。**已做空值回落**：
	// 未设置时这里返回 Name，前端不需要再判空。
	// 回落放在后端而不是前端，是因为"没设置显示名"和"显示名为空"
	// 在界面上只该有一种表现——显示点什么，而不是一行空白。
	DisplayName string `json:"display_name"`
	// Note 是自由备注。
	Note string `json:"note"`
	// Disabled 表示该服务器被管理员禁用（连接会在握手阶段被拒）。
	// 前端需要它来区分"离线"和"被禁用"——两者都连不上，
	// 但处置方式完全不同（前者等它恢复，后者要去启用）。
	Disabled bool   `json:"disabled"`
	Status   string `json:"status"`
	Version  string `json:"version"`
	Runtime  string `json:"runtime"`
	// RuntimeVersion 的 JSON 名是 runtime_version。
	//
	// 这里必须与列表保持一致：曾经 PATCH 响应用的是 agent_id / runtime_ver，
	// 而列表用 id / runtime_version。前端拿到 PATCH 响应后调用
	// store.replace(updated) 会因为找不到 id 字段而静默失败——
	// 没有报错，只是界面上没更新，用户以为操作没生效。
	RuntimeVersion string            `json:"runtime_version"`
	LastSeen       int64             `json:"last_seen"`
	Labels         map[string]string `json:"labels"`
}

type agentListResponse struct {
	Agents []agentView `json:"agents"`
}

// handleListAgents 处理 GET /api/agents。
//
// 在线状态以内存在线表为准，而不是数据库里的 status：
// Hub 重启后数据库里可能还写着 online，但那台 Agent 实际上已经断开。
func handleListAgents(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u, _ := userFrom(r.Context())

		records, err := d.DB.ListAgents(r.Context())
		if err != nil {
			respondError(w, d.Log, envelope.CodeInternal, "查询服务器列表失败")
			return
		}

		onlineIDs := d.Registry.OnlineIDs()
		grants := loadGrants(r, d, u)

		views := make([]agentView, 0, len(records))
		for _, rec := range records {
			// 非管理员只能看到被授权范围内的服务器（决策 D-04）。
			if !canAccess(u, grants, rec.ID, user.PermView) {
				continue
			}
			views = append(views, toAgentView(rec, onlineIDs))
		}

		// 稳定排序：按名称升序，保证多次刷新顺序一致。
		sort.SliceStable(views, func(i, j int) bool { return views[i].Name < views[j].Name })
		respondJSON(w, d.Log, http.StatusOK, agentListResponse{Agents: views})
	}
}

// toAgentView 把存储记录转成前端视图，并叠加实时在线状态。
func toAgentView(rec store.AgentRecord, online map[string]struct{}) agentView {
	status := "offline"
	if _, ok := online[rec.ID]; ok {
		status = "online"
	}
	labels := rec.Labels
	if labels == nil {
		labels = map[string]string{}
	}
	return agentView{
		ID:   rec.ID,
		Name: rec.Name,
		// 显示名未设置时回落到主机名，前端无需判空。
		DisplayName:    displayNameOf(rec),
		Note:           rec.Note,
		Disabled:       rec.Disabled,
		Status:         status,
		Version:        rec.Version,
		Runtime:        rec.Runtime,
		RuntimeVersion: rec.RuntimeVersion,
		LastSeen:       rec.LastSeen,
		Labels:         labels,
	}
}

// displayNameOf 返回展示用的名称：优先显示名，未设置则用主机名。
//
// 集中成一处是因为列表、PATCH、启用/禁用等多个响应都要用它。
// 各处各写一遍的话，很容易在新增接口时忘了回落，
// 出现"列表里显示名字、详情里显示空白"的不一致。
func displayNameOf(rec store.AgentRecord) string {
	if name := strings.TrimSpace(rec.DisplayName); name != "" {
		return name
	}
	return rec.Name
}

// handleListContainers 处理 GET /api/agents/{agentID}/containers。
func handleListContainers(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		agentID := chi.URLParam(r, "agentID")
		if !requirePermission(w, d, r, agentID, user.PermView) {
			return
		}

		res, cerr := d.Ops.ListContainers(r.Context(), agentID)
		if cerr != nil {
			respondConnError(w, d, cerr)
			return
		}
		respondJSON(w, d.Log, http.StatusOK, map[string]any{
			"agent_id":   agentID,
			"containers": res.Containers,
		})
	}
}

// handleContainerStart 处理 POST /api/agents/{agentID}/containers/{containerID}/start。
func handleContainerStart(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		agentID := chi.URLParam(r, "agentID")
		// 启动容器属于常规操作，需要 operate 档位。
		if !requirePermission(w, d, r, agentID, user.PermOperate) {
			return
		}
		cid := chi.URLParam(r, "containerID")

		res, cerr := d.Ops.StartContainer(r.Context(), agentID, cid)
		if cerr != nil {
			respondConnError(w, d, cerr)
			return
		}
		respondJSON(w, d.Log, http.StatusOK, map[string]any{
			"container_id": res.ContainerID,
			"state":        res.State,
		})
	}
}

// handleContainerStop 处理停止容器。语义与启动对称。
func handleContainerStop(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		agentID := chi.URLParam(r, "agentID")
		if !requirePermission(w, d, r, agentID, user.PermOperate) {
			return
		}
		cid := chi.URLParam(r, "containerID")

		res, cerr := d.Ops.StopContainer(r.Context(), agentID, cid)
		if cerr != nil {
			respondConnError(w, d, cerr)
			return
		}
		respondJSON(w, d.Log, http.StatusOK, map[string]any{
			"container_id": res.ContainerID,
			"state":        res.State,
		})
	}
}

// handleContainerLogs 处理 GET .../logs?tail=N。
func handleContainerLogs(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		agentID := chi.URLParam(r, "agentID")
		if !requirePermission(w, d, r, agentID, user.PermView) {
			return
		}
		cid := chi.URLParam(r, "containerID")

		tail, err := parseTail(r.URL.Query().Get("tail"))
		if err != nil {
			respondError(w, d.Log, envelope.CodeBadRequest, "tail 必须是 1 到 2000 之间的整数")
			return
		}

		res, cerr := d.Ops.Logs(r.Context(), agentID, cid, tail)
		if cerr != nil {
			respondConnError(w, d, cerr)
			return
		}
		respondJSON(w, d.Log, http.StatusOK, map[string]any{
			"lines":     res.Lines,
			"truncated": res.Truncated,
		})
	}
}

// 日志默认与最大行数。上限与前端契约（200）及 Agent 侧上界（5000）保持一致，
// 取 2000 作为 Hub 层的硬闸，防止滥用。
const (
	defaultLogTail = 200
	maxLogTail     = 2000
)

// parseTail 解析日志行数参数。
func parseTail(raw string) (int, error) {
	if raw == "" {
		return defaultLogTail, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return 0, err
	}
	if n < 1 || n > maxLogTail {
		return 0, strconv.ErrRange
	}
	return n, nil
}

// respondConnError 把连接层错误转成 HTTP 响应。
func respondConnError(w http.ResponseWriter, d Deps, cerr *agentlink.ConnError) {
	respondError(w, d.Log, cerr.Code, cerr.Message)
}
