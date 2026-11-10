package httpx

// audit_routes.go 是「路由模式 → 审计动作」的映射表。
//
// 为什么用路由模式查表而不是在每个 handler 里手动写审计调用：
// handler 里加调用**必然会漏**。新增接口时忘写一次，就是一条静默的空洞——
// 没有报错、没有测试失败，只有事后查审计时才发现"这段历史没了"。
// 中间件挂在路由上，所有接口自动覆盖，新增路由时忘加映射也只是
// 记成 unknown.POST 这种一眼可见的原始标识，不会静默丢失。
//
// 键是 chi 的 RoutePattern()（形如 /api/agents/{agentID}/containers/{containerID}），
// 不是真实路径。用模式做键的好处是 /containers/abc123 和 /containers/def456
// 命中同一条规则，不需要为每个实例写一行。

import (
	"strings"

	"arkade/hub/internal/audit"
)

// auditRoute 描述一条路由的审计方式。
type auditRoute struct {
	// Action 是审计动作标识。
	Action string
	// TargetParam 是路由参数名，其值作为 target 字段（容器名、编排名等）。
	// 空表示该路由没有"目标对象"，target 留空。
	TargetParam string
	// SubAction 为 true 时，把路径最后一段当作子动作写进 detail。
	// 用于编排的 9 个子动作合并成 stack.action 的场景。
	SubAction bool
}

// auditRoutes 是核心映射表。
//
// 只登记**有副作用**的路由。GET 一律不在这里——
// 中间件本身也会按 HTTP 方法先筛一遍，这里是第二道保险：
// 即使有人误把 GET 路由登记进来，也不会被记录。
var auditRoutes = map[string]auditRoute{
	// ---- 容器 ----
	"/api/agents/{agentID}/containers/{containerID}/start": {
		Action:      audit.ActionContainerStart,
		TargetParam: "containerID",
	},
	"/api/agents/{agentID}/containers/{containerID}/stop": {
		Action:      audit.ActionContainerStop,
		TargetParam: "containerID",
	},
	"/api/agents/{agentID}/containers/{containerID}/restart": {
		Action:      audit.ActionContainerRestart,
		TargetParam: "containerID",
	},
	"/api/agents/{agentID}/containers/{containerID}": {
		Action:      audit.ActionContainerRemove,
		TargetParam: "containerID",
	},

	// ---- 编排 ----
	// 注意：创建用 POST /stacks，更新用 PUT /stacks/{name}
	// （不是 PATCH），删除用 DELETE /stacks/{name}。
	// 三种方法共用一个路径模式，但语义完全不同，必须分别登记。
	"/api/agents/{agentID}/stacks": {
		Action: audit.ActionStackCreate,
	},
	"/api/agents/{agentID}/stacks/import": {
		Action: audit.ActionStackCreate,
	},
	"/api/agents/{agentID}/stacks/import-preview": {
		Action: audit.ActionStackAction,
	},
	// 编排的子动作（up/down/restart/pull…）统一记为 stack.action，
	// 具体子动作写进 detail。理由见 audit/actions.go 的注释：
	// 拆成十几个动作会让审计列表难以浏览，而用户关心的是"谁动了我的编排"。
	"/api/agents/{agentID}/stacks/{name}/actions": {
		Action:      audit.ActionStackAction,
		TargetParam: "name",
		SubAction:   true,
	},

	// 外部项目只记录操作标识，不记录配置草稿或敏感正文。
	"/api/agents/{agentID}/external-stacks/{name}/draft":         {Action: audit.ActionStackDraft, TargetParam: "name"},
	"/api/agents/{agentID}/external-stacks/{name}/apply-preview": {Action: audit.ActionStackPreview, TargetParam: "name"},
	"/api/agents/{agentID}/external-stacks/{name}/apply":         {Action: audit.ActionStackAdopt, TargetParam: "name"},
	"/api/agents/{agentID}/external-stacks/{name}/copy":          {Action: audit.ActionStackCopyConfig, TargetParam: "name"},
	// ---- 镜像异步任务：不记录归档正文/仓库凭据 ----
	"/api/agents/{agentID}/images/pull":    {Action: "image.pull"},
	"/api/agents/{agentID}/images/export":  {Action: "image.export"},
	"/api/agents/{agentID}/images/import":  {Action: "image.import"},
	"/api/agents/{agentID}/images/migrate": {Action: "image.migrate"},
	"/api/image-tasks/{taskID}/cancel":     {Action: "image.cancel", TargetParam: "taskID"},
	// 内容不进入审计；统一记录卷文件操作结果，不复制请求体。
	"/api/agents/{agentID}/volume-files": {Action: "volume.files"},
	// ---- 资源 ----
	"/api/agents/{agentID}/images/{imageID}": {
		Action:      audit.ActionImageRemove,
		TargetParam: "imageID",
	},
	"/api/agents/{agentID}/networks/{networkID}": {
		Action:      audit.ActionNetworkRemove,
		TargetParam: "networkID",
	},
	// 卷的参数名是 {name} 而不是 {volumeName}，与路由定义保持一致。
	// 写错参数名的后果很隐蔽：chi.URLParam 取不到值会返回空串，
	// 审计照样记录成功，只是 target 列永远是空的——没有任何报错。
	"/api/agents/{agentID}/volumes/{name}": {
		Action:      audit.ActionVolumeRemove,
		TargetParam: "name",
	},

	// ---- 认证 ----
	// 登出也记录：它是会话生命周期的关键事件，排查"我什么时候被登出的"
	// 以及"某账号是否发生了异常登出"时需要这条记录。
	// 登录本身不记：它发生在认证之前（auditMiddleware 挂在认证之后的分组里），
	// 而且登录成功/失败已由认证服务自己的日志覆盖。
	"/api/auth/logout": {
		Action: audit.ActionUserLogout,
	},

	// ---- 服务器 ----
	// PATCH /api/agents/{agentID} 是"修改"（改名/备注/标签），
	// DELETE 同路径是"删除"，后者在 auditDeleteOverrides 里覆盖。
	"/api/agents/{agentID}": {
		Action: audit.ActionAgentUpdate,
	},
	"/api/agents/{agentID}/disable": {
		Action: audit.ActionAgentDisable,
	},
	"/api/agents/{agentID}/enable": {
		Action: audit.ActionAgentEnable,
	},
	// 预创建服务器与重置接入密钥。
	//
	// 只用路由模式与 agent_id 作 target：**密钥本身绝不进审计**。
	// detail 也不填请求体内容——请求体里有 name/note/labels，
	// 记录它们等于把用户资料复制到一张会被导出备份的表里。
	"/api/agents": {
		Action: audit.ActionAgentCreate,
	},
	"/api/agents/{agentID}/token/reset": {
		Action:      audit.ActionAgentTokenReset,
		TargetParam: "agentID",
	},

	// ---- 用户 ----
	"/api/users": {
		Action: audit.ActionUserCreate,
	},
	// PATCH 是修改，DELETE 由 auditDeleteOverrides 覆盖。
	"/api/users/{userID}": {
		Action:      audit.ActionUserUpdate,
		TargetParam: "userID",
	},
	"/api/users/{userID}/password": {
		Action:      audit.ActionUserUpdate,
		TargetParam: "userID",
	},

	// ---- 用户组 ----
	"/api/groups": {
		Action: audit.ActionGroupCreate,
	},
	// PATCH 是修改，DELETE 由 auditDeleteOverrides 覆盖。
	"/api/groups/{groupID}": {
		Action:      audit.ActionGroupUpdate,
		TargetParam: "groupID",
	},
	"/api/groups/{groupID}/members": {
		Action:      audit.ActionGroupUpdate,
		TargetParam: "groupID",
	},
	"/api/groups/{groupID}/grants": {
		Action:      audit.ActionGroupUpdate,
		TargetParam: "groupID",
	},
}

// auditUpdateOverrides 覆盖「同一路径、不同方法对应不同动作」的情况。
//
// 为什么需要这张表：/api/agents/{agentID} 和 /api/users/{userID}
// 都是 PATCH 表示"修改"、DELETE 表示"删除"，但上面的表里一个路径
// 只能登记一条规则。若不区分，删除用户会被记成"修改用户"——
// 审计里最需要看清的恰恰是"谁删了东西"。
//
// 同理 /api/agents/{agentID}/stacks/{name} 的 PUT 是"保存内容"、
// DELETE 是"删除编排"，也是两种完全不同的操作。
var auditDeleteOverrides = map[string]auditRoute{
	"/api/agents/{agentID}":               {Action: audit.ActionAgentDelete},
	"/api/users/{userID}":                 {Action: audit.ActionUserDelete},
	"/api/groups/{groupID}":               {Action: audit.ActionGroupDelete},
	"/api/agents/{agentID}/stacks/{name}": {Action: audit.ActionStackDelete, TargetParam: "name"},
}

// auditPutOverrides 覆盖 PUT 方法对应的动作。
//
// PUT 在这个项目里用于两种"整体替换"操作：保存编排内容、
// 重置密码（属于修改用户）。它们与同路径的 PATCH/DELETE 语义不同，
// 因此单独一张表而不是塞进上面的主表。
var auditPutOverrides = map[string]auditRoute{
	"/api/agents/{agentID}/stacks/{name}": {Action: audit.ActionStackUpdate, TargetParam: "name"},
	"/api/users/{userID}/password":        {Action: audit.ActionUserUpdate, TargetParam: "userID"},
	"/api/groups/{groupID}/members":       {Action: audit.ActionGroupUpdate, TargetParam: "groupID"},
	"/api/groups/{groupID}/grants":        {Action: audit.ActionGroupUpdate, TargetParam: "groupID"},
}

// resolveAuditRoute 根据路由模式与 HTTP 方法查出审计动作。
//
// 查表顺序：先按方法查覆盖表（DELETE/PUT），再查主表。
// 顺序不能反过来：主表里 /api/users/{userID} 登记的是 PATCH 的"修改"，
// 若先查主表，DELETE 会命中它而被记成"修改用户"——
// 审计里最需要看清的恰恰是"谁删了东西"。
//
// 查不到时返回 unknown.<METHOD> 并带上原始路径：
// 这样审计列表上会明确出现 `unknown.POST` 和那条路径，
// 一眼就能看出"这个接口还没登记"，补映射时也有据可循。
// 用「未知操作」之类的友好文案反而会把这个信号藏起来。
func resolveAuditRoute(pattern, method, rawPath string) auditRoute {
	if pattern == "" {
		// 没有匹配到任何路由（例如 404）：仍可能值得记录——
		// 有人尝试访问不存在的接口时要留痕。动作记 unknown，
		// target 存原始路径。
		return auditRoute{Action: audit.Unknown(method), TargetParam: ""}
	}
	// 先查方法相关的覆盖表。
	switch method {
	case "DELETE":
		if r, ok := auditDeleteOverrides[pattern]; ok {
			return r
		}
	case "PUT":
		if r, ok := auditPutOverrides[pattern]; ok {
			return r
		}
	}
	if r, ok := auditRoutes[pattern]; ok {
		return r
	}
	return auditRoute{Action: audit.Unknown(method)}
}

// isMutatingMethod 判断该 HTTP 方法是否会产生副作用。
//
// 只记 POST/PUT/PATCH/DELETE。
// GET 不记的理由：打开列表页、刷新、点进详情都会产生 GET，
// 一次列表页刷新可能就是十几个 GET。若全记，审计表会被
// 「谁看了列表」淹没，真正重要的「谁删了容器」反而要翻好几页才找到。
// 审计的价值在于可读，而不是穷尽记录。
func isMutatingMethod(method string) bool {
	switch method {
	case "POST", "PUT", "PATCH", "DELETE":
		return true
	default:
		return false
	}
}

// subActionFromPath 取路径最后一段作为子动作名。
//
// 仅用于 stack.action 这类"合并动作"。取不到时返回空串。
func subActionFromPath(p string) string {
	trimmed := strings.TrimSuffix(p, "/")
	if trimmed == "" {
		return ""
	}
	if i := strings.LastIndex(trimmed, "/"); i >= 0 {
		return trimmed[i+1:]
	}
	return trimmed
}
