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

	// 镜像
	MethodImageList   = "image.list"   // 列出镜像
	MethodImageRemove = "image.remove" // 删除镜像（危险操作）

	// 网络
	MethodNetworkList   = "network.list"   // 列出网络
	MethodNetworkRemove = "network.remove" // 删除网络（危险操作）

	// 存储卷
	MethodVolumeList   = "volume.list"   // 列出存储卷
	MethodVolumeRemove = "volume.remove" // 删除存储卷（危险操作，数据丢失）

	// 容器编排（docker compose）
	//
	// 设计边界：这里只管**执行**，不管编排文件存在哪。
	// 编排文件的内容由 Hub 下发（payload 里带完整 YAML），
	// Agent 负责落到本机数据目录后调用 docker compose 执行。
	// 这样 Agent 不需要访问 Hub 的数据库，也不用处理分布式同步。
	MethodComposeList    = "compose.list"    // 列出本机已有的编排项目及状态
	MethodComposeUp      = "compose.up"      // 启动/更新一个编排（内部走 compose up -d）
	MethodComposeDown    = "compose.down"    // 停止并移除容器（保留数据卷）
	MethodComposeStop    = "compose.stop"    // 停止容器（不移除）
	MethodComposeRestart = "compose.restart" // 重启容器
	MethodComposePull    = "compose.pull"    // 拉取镜像（对应"更新镜像"）
	MethodComposeRebuild = "compose.rebuild" // 强制重建（pull + up --force-recreate）
	MethodComposeRemove  = "compose.remove"  // 删除编排：down -v + 删除本机文件
	MethodComposeBackup  = "compose.backup"  // 备份编排文件与关联数据卷清单
	MethodComposeLogs    = "compose.logs"    // 查看某个服务的日志
	MethodComposePS      = "compose.ps"      // 查看该编排下的容器状态
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

	LocalMethodImageList     = MethodImageList
	LocalMethodImageRemove   = MethodImageRemove
	LocalMethodNetworkList   = MethodNetworkList
	LocalMethodNetworkRemove = MethodNetworkRemove
	LocalMethodVolumeList    = MethodVolumeList
	LocalMethodVolumeRemove  = MethodVolumeRemove

	LocalMethodComposeList    = MethodComposeList
	LocalMethodComposeUp      = MethodComposeUp
	LocalMethodComposeDown    = MethodComposeDown
	LocalMethodComposeStop    = MethodComposeStop
	LocalMethodComposeRestart = MethodComposeRestart
	LocalMethodComposePull    = MethodComposePull
	LocalMethodComposeRebuild = MethodComposeRebuild
	LocalMethodComposeRemove  = MethodComposeRemove
	LocalMethodComposeBackup  = MethodComposeBackup
	LocalMethodComposeLogs    = MethodComposeLogs
	LocalMethodComposePS      = MethodComposePS
)
