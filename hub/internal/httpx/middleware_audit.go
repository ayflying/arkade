package httpx

// middleware_audit.go 是审计记录中间件。
//
// 核心设计（这是本任务最关键的一点）：
// **审计在中间件统一记录，不在 handler 里手动调用。**
//
// 手动调用的方案看起来更"可控"，实际必然漏记——新增接口时忘写一次，
// 就是一条静默的空洞：没有报错、没有测试失败，只有事后查审计才发现
// "这段时间的历史没了"。中间件挂在路由上，所有接口自动覆盖。
//
// 另外三条硬性约束：
//  1. 只记有副作用的方法（POST/PUT/PATCH/DELETE），GET 不记；
//  2. 写审计失败**绝不影响业务响应**——审计是旁路，不能让它变成故障源；
//  3. 记录耗时包含完整处理时间（从进入中间件到 handler 返回）。

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"arkade/hub/internal/audit"
)

// auditWriter 是本包对审计写入端的最小依赖。
//
// 用窄接口而不是直接依赖 *store.DB：
// 测试需要一个"永远写失败"的实现来验证约束 2，
// 用接口能一行代码替换掉，不必造假的数据库。
type auditWriter interface {
	WriteAudit(rec audit.Record) error
}

// statusRecorder 包一层 ResponseWriter 以捕获状态码与响应体大小。
//
// 为什么要包：到中间件返回时 w 本身已经不告诉任何人"刚才写了几字节、
// 状态码是多少"。不记状态码的话，审计无法区分成功与失败——
// 而"操作失败了"恰恰是最需要被审计的事实之一。
type statusRecorder struct {
	http.ResponseWriter
	status int
	// size 记录已写出的响应体字节数，用于排障（比如异常的大响应）。
	size int
	// wroteHeader 防止重复 WriteHeader：HTTP 只允许第一个生效，
	// 允许重复会让实现以为自己改成功了，实际状态码仍是第一次的。
	wroteHeader bool
	// errorMessage 捕获错误响应体里的 message，写进审计的 error 列。
	//
	// 为什么连响应体一起抓：respondError 没有 request 参数，
	// 无法把消息塞进 context 让中间件读取。而错误消息恰恰是审计里
	// 最有价值的部分——"删除容器失败"和"删除容器失败：容器正在运行"
	// 对事后排查是天壤之别。
	//
	// 只抓错误响应（状态码 >= 400）且只抓 message 字段，
	// 不存整个响应体：成功响应可能有几十 KB 的容器列表，
	// 而其中还可能含敏感信息。
	errorMessage string
	// syncOK / sawSyncOK 捕获编排类接口的"下发结果"。
	//
	// 为什么状态码不够：编排的创建/更新是**两步**操作——
	// 先保存记录（成功），再下发给 Agent 执行（可能失败）。
	// 下发失败时接口仍返回 200（记录确实保存了），但用 sync_ok:false
	// 表示"保存成功、尚未生效"。
	//
	// 若只看状态码，这种情况会被记成 success，"部署成功"就成了谎话。
	// 事后查审计看到"某人成功发布了编排"，而线上根本没变——
	// 这比没有记录更糟，因为它会误导事故复盘。
	//
	// 因此这里额外读一次响应体，识别 sync_ok 字段。
	syncOK    bool
	sawSyncOK bool
}

// 保留底层流式deadline能力，归档上传不能被包装器遮断取消。
func (r *statusRecorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }

func (r *statusRecorder) WriteHeader(code int) {
	if r.wroteHeader {
		return
	}
	r.wroteHeader = true
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

func (r *statusRecorder) Write(b []byte) (int, error) {
	// 没显式调 WriteHeader 时，第一次 Write 隐式返回 200。
	// 这里必须同步记录，否则"只写响应体不写状态码"的 handler
	// 会让 status 留 0，审计里显示成异常状态。
	if !r.wroteHeader {
		r.WriteHeader(http.StatusOK)
	}
	// 只对错误响应做一次轻量解析。
	if r.status >= http.StatusBadRequest && r.errorMessage == "" {
		r.errorMessage = extractErrorMessage(b)
	}
	// 成功响应里也可能藏着"实际没成"的语义（编排的 sync_ok）。
	// 只在还没抓到过时解析一次，避免分块写入时反复 json.Unmarshal。
	if r.status < http.StatusBadRequest && !r.sawSyncOK && bytes.Contains(b, []byte("sync_ok")) {
		var payload struct {
			SyncOK *bool  `json:"sync_ok"`
			Hint   string `json:"sync_hint"`
		}
		// 用指针接收：字段缺失与显式 false 必须能区分。
		// 若用 bool 接收，任何含 sync_ok 字样的响应都会被当成
		// "下发失败"，那会污染所有正常记录。
		if err := json.Unmarshal(b, &payload); err == nil && payload.SyncOK != nil {
			r.sawSyncOK = true
			r.syncOK = *payload.SyncOK
			if *payload.SyncOK {
				// 下发成功，无需额外说明。
				r.errorMessage = ""
			} else {
				r.errorMessage = payload.Hint
			}
		}
	}
	n, err := r.ResponseWriter.Write(b)
	r.size += n
	return n, err
}

// extractErrorMessage 从错误响应体里取出 message 字段。
//
// 解析失败时把原文（截断后）作为兜底：宁可记一段不完美的原始文本，
// 也不要让审计的 error 列空白——空白意味着"不知道为什么失败"。
func extractErrorMessage(body []byte) string {
	var payload struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &payload); err == nil && payload.Error.Message != "" {
		return payload.Error.Message
	}
	const maxRaw = 200
	if len(body) > maxRaw {
		return string(body[:maxRaw]) + "…"
	}
	return string(body)
}

// auditMiddleware 记录有副作用请求的审计。
//
// 挂在已认证的分组上（能拿到当前用户）；未登录的请求不记，
// 因为"谁做的"这一栏填不出来，记了也无法追责。
func auditMiddleware(w auditWriter, log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
			// 非副作用方法直接放行，连包装都不做。
			// 包装会改变 ResponseWriter 的动态类型，某些依赖
			// Hijacker/Flusher 的下游（如日志中间件）可能受影响，
			// 对 GET 这种高频路径不值得承担任何风险。
			if !isMutatingMethod(r.Method) {
				next.ServeHTTP(rw, r)
				return
			}

			start := time.Now()
			rec := &statusRecorder{ResponseWriter: rw, status: http.StatusOK}

			// 预先读出请求体里的子动作（仅 stack action 需要）。
			//
			// 为什么必须在 handler 之前读：编排的 up/down/pull/restart
			// 全都走同一条路径 /stacks/{name}/actions，区别只在请求体的
			// action 字段。从路径取只能永远得到 "actions"——
			// 审计里所有编排操作长得一模一样，等于没记。
			//
			// 读完之后**必须把 body 还原**，否则 handler 拿到空 body，
			// 解码失败返回 400——这是最容易踩的坑：
			// 加审计反而把业务弄坏了。
			subAction := ""
			if isStackActionPath(r.URL.Path) && r.Body != nil {
				raw, err := io.ReadAll(io.LimitReader(r.Body, maxAuditBodyPeek))
				if err == nil {
					// 还原：无论解析成功与否都要还原，
					// 否则解析失败时 handler 也读不到内容。
					r.Body = io.NopCloser(bytes.NewReader(raw))
					subAction = decodeActionField(raw)
				}
				_ = r.Body.Close()
			}

			// 统一在 defer 里记录，并且**自己处理 panic**。
			//
			// 为什么不能依赖外层 recoverer：defer 是后进先出，
			// 审计中间件的 defer 注册在内层，会比外层 recoverer 先执行。
			// 即 recoverer 还没把 panic 变成 500 时审计已经记完了，
			// 此时 rec.status 还是默认的 200 —— panic 会被记成 success。
			// "操作崩了但审计说成功"比没有记录更危险：事故复盘会走偏。
			//
			// 做法：recover 后显式标记失败，记录一次，再原样 re-panic
			// 交给外层 recoverer 生成 500 响应。外层行为完全不变。
			defer func() {
				p := recover()
				if p != nil {
					rec.status = http.StatusInternalServerError
					rec.errorMessage = "处理过程中发生内部错误"
				}
				// 无论正常还是 panic，都只记录一次。
				writeAuditRecord(w, log, r, rec, time.Since(start), subAction)
				if p != nil {
					panic(p)
				}
			}()

			next.ServeHTTP(rec, r)
		})
	}
}

// writeAuditRecord 从请求上下文拼出审计记录并写入。
//
// 本函数**不返回错误**，也不做任何影响响应的动作：
// 到这一步响应已经写完了，任何失败都只能记日志。
// 这个签名本身就在向调用者传达"审计是旁路"。
func writeAuditRecord(w auditWriter, log *slog.Logger, r *http.Request, rec *statusRecorder, dur time.Duration, subAction string) {
	// 从 context 取当前用户。
	//
	// authenticate 中间件已经把用户放进 context；取不到说明这条请求
	// 走的是不需要认证的路由（例如登录接口）。此时仍要记录——
	// 登录失败尝试是安全审计的重要内容——只是 user_id 留空。
	u, hasUser := userFrom(r.Context())

	pattern := routePattern(r)
	route := resolveAuditRoute(pattern, r.Method, r.URL.Path)

	// 目标对象：优先取路由参数（容器名、编排名等）。
	target := ""
	if route.TargetParam != "" {
		target = chi.URLParam(r, route.TargetParam)
	}
	// 未登记路由时把原始路径记进 target：
	// 否则审计里只有 unknown.POST 而对端无从知道是哪条路径，
	// 补映射时还得去翻日志。
	if pattern == "" {
		target = r.URL.Path
	}

	// 编排放合并动作时，把子动作（down/up/pull…）写进 detail。
	// 子动作来自请求体的 action 字段，由调用方预先解析好传进来。
	detail := ""
	if route.SubAction {
		detail = subAction
	}

	result := audit.ResultSuccess
	errMsg := ""
	// 判定失败的依据有两层：
	//
	//  1. 状态码 >= 400 —— 用户实际感知到的失败；
	//  2. HTTP 200 但响应体里 sync_ok:false —— **保存成功、下发失败**。
	//
	// 第二条是补丁性的但必要的：编排保存是"存记录 + 下发给 Agent"两步，
	// 下发失败时接口仍返回 200（记录确实存了，重试保存是错的）。
	// 只看状态码会把这种情况记成 success，等于声称"部署成功了"，
	// 而线上根本没变。事故复盘时看到"某人成功发布了编排"却被线上
	// 打脸，比完全没有记录更糟。
	//
	// 用状态码而非错误码字符串判断仍然是第一原则（新增错误码不影响这里）。
	if rec.status >= http.StatusBadRequest {
		result = audit.ResultFailed
		errMsg = rec.errorMessage
	} else if rec.sawSyncOK && !rec.syncOK {
		result = audit.ResultFailed
		// detail 要说清"哪部分成了、哪部分没成"，不能只说失败——
		// 记录确实保存了，若审计只说"失败"，运维会以为保存也没成，
		// 于是重新保存一遍，那是多余且可能覆盖别人改动的操作。
		errMsg = "已保存，但下发到服务器失败"
		if rec.errorMessage != "" {
			errMsg += "：" + rec.errorMessage
		}
	}

	r2 := audit.Record{
		ID:         audit.NewID(),
		UserID:     u.ID,
		Username:   u.Username,
		AgentID:    chi.URLParam(r, "agentID"),
		AgentName:  "",
		Action:     route.Action,
		Target:     target,
		Detail:     detail,
		Result:     result,
		Error:      errMsg,
		DurationMs: dur.Milliseconds(),
		CreatedAt:  time.Now().UTC(),
	}
	if !hasUser {
		r2.UserID = ""
		r2.Username = ""
	}

	// **写失败只打日志，绝不改变响应。**
	// 用户的操作已经成功了，此时因为"审计写不进去"返回错误，
	// 会让用户重复操作（比如再删一次），造成真正的副作用。
	// 审计的价值远低于一次正确执行的业务操作。
	if err := w.WriteAudit(r2); err != nil {
		log.Error("写入审计失败（不影响业务响应）",
			"err", err, "action", r2.Action, "path", r.URL.Path)
	}
}

// routePattern 取当前请求匹配到的路由模式。
//
// 必须在 handler 执行**之后**调用：chi 在路由匹配阶段才把模式
// 写进 RouteContext，之前取不到。
func routePattern(r *http.Request) string {
	rc := chi.RouteContext(r.Context())
	if rc == nil {
		return ""
	}
	return rc.RoutePattern()
}

// 关于"为什么不记录请求体"的说明，写在这里以免后来人以为是遗漏：
//
// 审计表没有请求体字段，而且请求体可能含密码（创建用户、重置密码接口），
// 记录它有泄露风险。审计要回答的是"谁在什么时候对什么做了什么、成功了没有"，
// 这三问不需要请求体细节；若将来确实需要（比如记录编排内容变更的前后对比），
// 应该单独设计一个白名单式的字段，而不是无差别落盘整个 body。
//
// 唯一的例外是 stack action 的子动作名：它是**白名单里的单个枚举字段**，
// 不可能是密码，也不含内容。见 decodeActionField。

// maxAuditBodyPeek 是审计预读请求体的字节上限。
//
// 需要上限的原因：编排保存的请求体可能含整份 docker-compose.yml（几十 KB），
// 无上限读取会让审计把大文件整个读进内存。子动作名在 JSON 开头附近，
// 8KB 足够覆盖；超出部分不解析，退化成没有子动作。
//
// 注意：被截断时 handler 仍会拿到**完整**内容——
// 因为还原用的是原始 raw，不是截断后的片段。
const maxAuditBodyPeek = 8192

// stackActionSuffix 是编排子动作路径的固定后缀。
const stackActionSuffix = "/actions"

// isStackActionPath 判断请求路径是否为编排子动作。
//
// **不能在这里用 routePattern**：chi 的 RoutePattern 只有 handler 执行过
// 之后才有值，而本函数必须在 handler 之前调用（要提前读请求体）。
// 之前写成查 routePattern 的结果是恒为匹配失败，detail 永远是空——
// 而且不报错，只是审计里所有编排操作都缺了子动作，
// 属于"静默失效"，只能靠测试发现。
//
// 因此这里按 URL 路径判断：形如
//
//	/api/agents/{id}/stacks/{name}/actions
//
// 的路径，层级正好是 6 段，且第 1/2 段为 api/agents、第 5 段为 actions。
func isStackActionPath(p string) bool {
	p = strings.TrimSuffix(p, "/")
	parts := strings.Split(strings.TrimPrefix(p, "/"), "/")
	// api / agents / {id} / stacks / {name} / actions
	if len(parts) != 6 {
		return false
	}
	return parts[0] == "api" && parts[1] == "agents" &&
		parts[3] == "stacks" && parts[5] == stackActionSuffix[1:]
}

// decodeActionField 从请求体里只解码 action 一个字段。
//
// **只解码白名单里的这一个字段**，绝不落盘整个 body：
// body 里可能有编排内容、镜像地址、服务列表等，既无必要又有泄露风险。
// 用最小结构体接收，Go 的 json 解码会忽略其他所有字段，
// 这是"白名单"在代码层面的落实。
//
// 解析失败返回空串：审计记不到子动作是遗憾，
// 但绝不能因为审计解析失败而影响业务请求。
func decodeActionField(raw []byte) string {
	var payload struct {
		Action string `json:"action"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return ""
	}
	// 长度截断：防止异常长的 action 值污染审计表。
	const maxLen = 32
	a := strings.TrimSpace(payload.Action)
	if len(a) > maxLen {
		a = a[:maxLen]
	}
	return a
}
