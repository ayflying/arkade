package httpx

// handlers_audit.go 是审计日志查询接口。
//
// 权限设计：
//   - admin 看全部；
//   - 非 admin **只看自己的操作**。
//
// 为什么非 admin 不能看别人的：审计记录里包含「谁在什么时候动了哪台服务器、
// 失败了什么原因」。这类信息横向可见等于把整个运维拓扑与故障历史
// 摊开给每个登录用户，属于过度暴露。用户看自己的操作记录（"我昨天是不是
// 删错了东西"）是正当需求，看别人的不是。

import (
	"net/http"
	"strconv"
	"time"

	"arkade/hub/internal/audit"
	"arkade/hub/internal/store"
	"arkade/hub/internal/user"
	"arkade/protocol/envelope"
)

// auditItem 是单条审计记录的响应形状。
//
// 单独定义而不是直接序列化 audit.Record：
//   - 需要额外补充 action_label（中文名）；
//   - created_at 用 Unix 秒而非 RFC3339，与项目其他接口保持一致；
//   - 避免将来给 Record 加内部字段时意外泄露到接口上。
type auditItem struct {
	ID          string `json:"id"`
	UserID      string `json:"user_id"`
	Username    string `json:"username"`
	AgentID     string `json:"agent_id"`
	AgentName   string `json:"agent_name"`
	Action      string `json:"action"`
	ActionLabel string `json:"action_label"`
	Target      string `json:"target"`
	Detail      string `json:"detail"`
	Result      string `json:"result"`
	Error       string `json:"error"`
	DurationMs  int64  `json:"duration_ms"`
	CreatedAt   int64  `json:"created_at"`
}

// auditListResponse 是列表响应。
type auditListResponse struct {
	Total int         `json:"total"`
	Items []auditItem `json:"items"`
}

// handleListAudit 处理 GET /api/audit。
func handleListAudit(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u, ok := userFrom(r.Context())
		if !ok {
			respondError(w, d.Log, envelope.CodeUnauthorized, "请先登录")
			return
		}

		f, ok := parseAuditFilters(w, d, r, u)
		if !ok {
			return
		}

		records, total, err := d.DB.ListAuditLogs(r.Context(), f)
		if err != nil {
			d.Log.Error("查询审计日志失败", "err", err)
			respondError(w, d.Log, envelope.CodeInternal, "查询操作记录失败")
			return
		}

		items := make([]auditItem, 0, len(records))
		for _, rec := range records {
			items = append(items, toAuditItem(rec))
		}
		respondJSON(w, d.Log, http.StatusOK, auditListResponse{Total: total, Items: items})
	}
}

// parseAuditFilters 解析查询参数。
//
// 返回 ok=false 表示已经写好了错误响应，调用方直接 return。
func parseAuditFilters(w http.ResponseWriter, d Deps, r *http.Request, u user.User) (store.AuditFilters, bool) {
	q := r.URL.Query()
	f := store.AuditFilters{
		UserID:  q.Get("user_id"),
		AgentID: q.Get("agent_id"),
		Action:  q.Get("action"),
		Result:  q.Get("result"),
	}

	// **权限在这里收紧，而不是信任前端不传 user_id。**
	// 非 admin 强制覆盖成自己的 ID：即使他手工构造 ?user_id=别人，
	// 也只会看到自己的记录。权限判断放在参数解析层而不是"解析后校验"，
	// 是为了避免新增筛选参数时忘记加校验。
	if u.Role != user.RoleAdmin {
		f.UserID = u.ID
	}

	var ok bool
	if f.Limit, ok = parseOptionalInt(w, d, r, "limit", 0); !ok {
		return f, false
	}
	if f.Offset, ok = parseOptionalInt(w, d, r, "offset", 0); !ok {
		return f, false
	}

	// result 只允许两个取值：非法值直接报 400，
	// 而不是静默返回空列表。空列表会让人以为"那段时间没有失败操作"，
	// 实际是自己拼错了参数——两种情况的处置完全不同。
	if f.Result != "" && f.Result != audit.ResultSuccess && f.Result != audit.ResultFailed {
		respondError(w, d.Log, envelope.CodeBadRequest,
			"result 只能是 success 或 failed")
		return f, false
	}

	if s := q.Get("from"); s != "" {
		t, err := parseUnixTime(s)
		if err != nil {
			respondError(w, d.Log, envelope.CodeBadRequest, "from 必须是 Unix 秒或将时间格式")
			return f, false
		}
		f.From = t
	}
	if s := q.Get("to"); s != "" {
		t, err := parseUnixTime(s)
		if err != nil {
			respondError(w, d.Log, envelope.CodeBadRequest, "to 必须是 Unix 秒或将时间格式")
			return f, false
		}
		f.To = t
	}
	return f, true
}

// parseOptionalInt 解析可选的整数查询参数。
func parseOptionalInt(w http.ResponseWriter, d Deps, r *http.Request, name string, def int) (int, bool) {
	s := r.URL.Query().Get(name)
	if s == "" {
		return def, true
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		respondError(w, d.Log, envelope.CodeBadRequest, name+" 必须是整数")
		return def, false
	}
	return n, true
}

// parseUnixTime 解析时间参数，同时接受 Unix 秒与 RFC3339。
//
// 两种都接受是因为调用方不同：前端分页器直接传毫秒/秒时间戳很方便，
// 而人手工构造 URL 时写 2026-01-01T00:00:00Z 更直观。
// 只接受一种会让另一种用法莫名其妙地失败，而失败原因（400）看起来
// 像是权限问题，排查起来很费劲。
func parseUnixTime(s string) (int64, error) {
	// 先试整数秒。
	if n, err := strconv.ParseInt(s, 10, 64); err == nil {
		return n, nil
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return 0, err
	}
	return t.Unix(), nil
}

// toAuditItem 把存储层记录转成响应形状。
func toAuditItem(rec audit.Record) auditItem {
	return auditItem{
		ID:        rec.ID,
		UserID:    rec.UserID,
		Username:  rec.Username,
		AgentID:   rec.AgentID,
		AgentName: rec.AgentName,
		Action:    rec.Action,
		// 中文名由后端提供，前端不需要再维护一份字典。
		// 未登记的动作原样返回（见 audit.ActionLabel 的注释：
		// 暴露原始标识正是"这里漏了映射"的信号）。
		ActionLabel: audit.ActionLabel(rec.Action),
		Target:      rec.Target,
		Detail:      rec.Detail,
		Result:      rec.Result,
		Error:       rec.Error,
		DurationMs:  rec.DurationMs,
		CreatedAt:   rec.CreatedAt.Unix(),
	}
}

// handleListAuditActions 处理 GET /api/audit/actions。
//
// 返回全部动作标识与中文名，供前端渲染筛选下拉框。
// 不给这个接口的话，前端只能硬编码一份动作列表——
// 后端新增动作时前端就会漏掉，那个动作永远选不出来。
func handleListAuditActions(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if _, ok := userFrom(r.Context()); !ok {
			respondError(w, d.Log, envelope.CodeUnauthorized, "请先登录")
			return
		}
		type actionOption struct {
			Action string `json:"action"`
			Label  string `json:"label"`
		}
		opts := make([]actionOption, 0, len(audit.All()))
		for _, a := range audit.All() {
			opts = append(opts, actionOption{Action: a, Label: audit.ActionLabel(a)})
		}
		respondJSON(w, d.Log, http.StatusOK, map[string]any{"actions": opts})
	}
}
