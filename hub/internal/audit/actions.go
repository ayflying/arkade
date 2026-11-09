// Package audit 定义操作审计的动作标识与中文名映射。
//
// 为什么单独成包而不是散在 httpx 里：动作标识是审计数据的一部分，
// 一旦写入数据库就固定下来了（历史记录里存的是字符串）。
// 放在独立包里，写入方、查询接口、将来的报表导出都引用同一份常量，
// 不会出现"某处手打 container.stop、另一处写 container.Stop"导致
// 同一个动作在审计里分裂成两条不同的记录。
package audit

// 动作标识。
//
// 命名统一用 `域.动作` 形式：
//   - 域与协议方法名（container.* / compose.*）大致对应，便于和 Agent 日志对照；
//   - 但**不直接复用协议方法名**。协议是机器间契约，改名时不应该
//     连带改写历史审计记录；而审计是给人看的、面向业务的分类。
//
// 特别注意 stack.action 这个"合并项"：编排的 down/stop/restart/pull/
// rebuild/remove/backup/logs/ps 在协议层是 9 个方法，但对审计来说
// 用户视角只是"对某个编排做了一次操作"。若拆成 9 个动作，
// 审计列表会变得难以浏览，而且用户关心的往往是"谁动了我的编排"
// 而不是具体哪个子动作——子动作放进 detail 字段即可。
const (
	// 容器
	ActionContainerStart   = "container.start"
	ActionContainerStop    = "container.stop"
	ActionContainerRestart = "container.restart"
	ActionContainerRemove  = "container.remove"

	// 编排
	ActionStackCreate = "stack.create"
	ActionStackUpdate = "stack.update"
	ActionStackDelete = "stack.delete"
	ActionStackAction = "stack.action"

	// 资源
	ActionImageRemove   = "image.remove"
	ActionNetworkRemove = "network.remove"
	ActionVolumeRemove  = "volume.remove"

	// 服务器
	ActionAgentUpdate  = "agent.update"
	ActionAgentDelete  = "agent.delete"
	ActionAgentDisable = "agent.disable"
	ActionAgentEnable  = "agent.enable"

	// 用户与用户组
	ActionUserCreate  = "user.create"
	ActionUserUpdate  = "user.update"
	ActionUserDelete  = "user.delete"
	ActionUserLogin   = "user.login"
	ActionUserLogout  = "user.logout"
	ActionGroupCreate = "group.create"
	ActionGroupUpdate = "group.update"
	ActionGroupDelete = "group.delete"
)

// 结果取值。
const (
	ResultSuccess = "success"
	ResultFailed  = "failed"
)

// labels 是动作标识到中文名的映射。
//
// 为什么中文名由后端提供而不是让前端维护一份字典：
// 前端字典与后端常量分处两个仓库文件，新增动作时几乎必然漏改其中一份，
// 表现为审计列表上冒出 `container.remove` 这样的原始标识。
// 由后端在响应里带 action_label，前端零维护，且新旧动作永远一致。
var labels = map[string]string{
	ActionContainerStart:   "启动容器",
	ActionContainerStop:    "停止容器",
	ActionContainerRestart: "重启容器",
	ActionContainerRemove:  "删除容器",

	ActionStackCreate: "创建编排",
	ActionStackUpdate: "修改编排",
	ActionStackDelete: "删除编排",
	ActionStackAction: "操作编排",

	ActionImageRemove:   "删除镜像",
	ActionNetworkRemove: "删除网络",
	ActionVolumeRemove:  "删除存储卷",

	ActionAgentUpdate:  "修改服务器",
	ActionAgentDelete:  "删除服务器",
	ActionAgentDisable: "禁用服务器",
	ActionAgentEnable:  "启用服务器",

	ActionUserCreate:  "创建用户",
	ActionUserUpdate:  "修改用户",
	ActionUserDelete:  "删除用户",
	ActionUserLogin:   "登录",
	ActionUserLogout:  "退出登录",
	ActionGroupCreate: "创建用户组",
	ActionGroupUpdate: "修改用户组",
	ActionGroupDelete: "删除用户组",
}

// ActionLabel 返回动作的中文名。
//
// 未登记的标识原样返回而不是空串或「未知操作」：
// 审计列表上出现 `unknown.POST /api/xxx` 这样的原始值，
// 恰恰是"这里有新接口还没补映射"的信号，一眼就能发现。
// 若统一显示「未知操作」，反而会把这个问题藏起来。
func ActionLabel(action string) string {
	if l, ok := labels[action]; ok {
		return l
	}
	return action
}

// Known 判断动作标识是否已登记。
//
// 测试用它来确保所有导出常量都进了 labels 表——
// 加常量忘加映射是很常见的疏漏，那样审计列表会显示原始标识。
func Known(action string) bool {
	_, ok := labels[action]
	return ok
}

// All 返回全部已登记的动作标识，供测试与前端筛选下拉使用。
func All() []string {
	out := make([]string, 0, len(labels))
	for a := range labels {
		out = append(out, a)
	}
	return out
}

// Unknown 生成未登记路由的动作标识。
//
// 形如 `unknown.POST`：保留方法名便于分辨，路径另存进 target，
// 这样审计列表上能直接看出"哪个接口漏了映射"，补映射时也有据可循。
func Unknown(method string) string {
	return "unknown." + method
}
