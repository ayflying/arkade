package envelope

// payload_compose_source.go：外部编排"配置来源读取"（compose.source）。
//
// ============================ 这个东西为什么存在 ============================
//
// Agent 跑在容器里，读不到宿主机上的编排文件。以往接管外部项目需要用户
// 手动把 compose 内容粘进界面。而 compose v2 会把项目的工作目录与配置文件
// 路径作为**容器标签**写在容器上，于是 Agent 可以自己去读。
//
// ============================ 它**不是**权限边界 ============================
//
// 标签是 daemon 侧的元数据，能写标签的人（宿主机管理员 / 有 docker 权限者）
// 同样能直接做任何事。因此：
//   - 标签**不是**"这个路径可以安全读"的证明，只是"用户想接管哪个项目"的线索；
//   - 真正的约束是 Agent 侧的路径校验 + helper 的只读挂载 + 谁都无法借它
//     读取项目工作目录之外的任何东西。
//
// 换句话说：信任边界仍在宿主机管理员一侧，本方法只保证
// "**不把可读范围扩大到该编排项目工作目录之外**"。

// ComposeSourcePayload 是 compose.source 的请求。
//
// 只带项目名：配置路径**必须**由 Agent 自己从引擎标签反查，
// 绝不接受调用方传入的宿主路径——那等于把任意读文件的能力开放给 Hub 侧。
type ComposeSourcePayload struct {
	// Name 是 compose 项目名（来自容器标签 com.docker.compose.project）。
	Name string `json:"name"`
}

// ComposeSourceResult 是读出的一份编排配置。
//
// 刻意**不含**任何宿主路径：路径既无必要出现在前端，也不该进 Hub 数据库。
// Blockers 只放固定的脱敏文案（字段名 + 原因），绝不含标量值原文。
type ComposeSourceResult struct {
	Name       string   `json:"name"`
	Content    string   `json:"content,omitempty"`
	ConfigHash string   `json:"config_hash,omitempty"`
	Blockers   []string `json:"blockers,omitempty"`
}
