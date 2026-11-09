package envelope

// 方法名常量的唯一来源。Hub 的路由表、Agent 的处理器注册、前端调用都必须引用这里，
// 禁止在业务代码里手写字符串，否则改一处漏一处的经典问题会立刻出现。
//
// 命名规范：<域>.<动作>。域与运行时抽象层（runtime）的职责对应，
// 新增运行时时方法名不需要变化，变化只在 Agent 侧的实现。
//
// 关于握手：agent.hello 使用 request/response 语义而不是 event——
// Hub 必须把分配的 agent_id 通过应答回传，event 没有应答通道。
const (
	// 连接与会话（Agent → Hub 建连后握手）
	MethodHello        = "agent.hello"         // Agent 上线握手（request/response）
	MethodHeartbeat    = "agent.heartbeat"     // 心跳（event），携带简要状态摘要
	MethodSnapshotPush = "agent.snapshot.push" // 重连后的全量状态快照（event），用于 Hub 对账

	// 容器操作
	MethodContainerList   = "container.list"   // 列出容器
	MethodContainerStart  = "container.start"  // 启动容器
	MethodContainerStop   = "container.stop"   // 停止容器
	MethodContainerRemove = "container.remove" // 删除容器（危险操作）
	MethodContainerLogs   = "container.logs"   // 拉取日志（有限行数，避免撑爆连接）

	// 运行时探测
	MethodRuntimeInfo = "runtime.info" // 上报运行时类型与版本（docker / podman）
)

// Hub → Agent 的本地通道方法名（Agent 内部 CLI 复用同一套处理器）。
// 这些字符串就是上面的方法名本身：Agent 的处理器注册表不区分请求来自
// Hub 连接还是本地 CLI，离线自治才能与在线行为完全一致，不存在两套实现。
const (
	LocalMethodContainerList   = MethodContainerList
	LocalMethodContainerStart  = MethodContainerStart
	LocalMethodContainerStop   = MethodContainerStop
	LocalMethodContainerRemove = MethodContainerRemove
	LocalMethodContainerLogs   = MethodContainerLogs
	LocalMethodRuntimeInfo     = MethodRuntimeInfo
)
