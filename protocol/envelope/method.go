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
	MethodVolumeFiles   = "volume.files"
	MethodComposeSource = "compose.source"
	// 编排镜像更新：异步任务，start 立即返回 task_id，get 增量轮询输出。
	MethodComposePullStart = "compose.pull.start"
	MethodComposePullGet   = "compose.pull.get"
	// 连接与会话（Agent → Hub 建连后握手）
	MethodHello        = "agent.hello"         // Agent 上线握手（request/response）
	MethodHeartbeat    = "agent.heartbeat"     // 心跳（event），携带简要状态摘要
	MethodSnapshotPush = "agent.snapshot.push" // 重连后的全量状态快照（event），用于 Hub 对账

	// 容器操作
	MethodContainerList    = "container.list"    // 列出容器
	MethodContainerStart   = "container.start"   // 启动容器
	MethodContainerStop    = "container.stop"    // 停止容器
	MethodContainerRestart = "container.restart" // 重启容器（等价 stop + start，但保留容器本身）
	MethodContainerRemove  = "container.remove"  // 删除容器（危险操作）
	MethodContainerLogs    = "container.logs"    // 拉取日志（有限行数，避免撑爆连接）
	MethodContainerInspect = "container.inspect" // 容器详情（只读，白名单字段）

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

	// 外部编排的显式纳管（用户主动导入）。
	//
	// 与 compose.up 的区别是**语义**，不是实现便利：
	//   - preview 只读，回答"能不能导入、为什么不能"；
	//   - adopt 只落盘，**绝不 up/down/pull**——
	//     外部项目正在跑生产负载，导入不应该顺带改动它。
	// 两者都只依据引擎容器标签与本地工作区状态判断，
	// 不读取宿主机上的任何配置文件（Agent 在容器内本就读不到）。
	MethodComposePreview = "compose.preview" // 预检：能否导入某个已发现的外部编排
	MethodComposeAdopt   = "compose.adopt"   // 导入：把外部编排显式纳管（只落盘，不启动）

	// 镜像传输：拉取 / 导出 / 导入（跨 Agent 迁移的字节搬运）。
	//
	// ============================ 为什么是分块 RPC ============================
	//
	// 镜像归档是几百 MB 到几 GB 的二进制。控制连接是**信令通道**：
	// 单条入站消息上限 8 MiB、单条写超时 10 秒、出站队列仅 32 条。
	// 把归档塞进一个 JSON 信封（base64 还会再膨胀 33%）必然先在几十 MB 处
	// 撞上这三条限制，而且首次失败的现场是"连接被断开"，
	// 排查起来会误以为是网络问题。
	//
	// 因此字节走**分块 RPC**：每块 ≤ 256 KiB，单块远低于消息上限，
	// 也远小于任何合理的内存预算。好处是：
	//   - Agent 侧**永远不把整个归档读进内存**（流式落盘 / 流式读出）；
	//   - 每块天然可重试，断线只需重发当前块；
	//   - **不新增入站端口**，保持 Agent 反连 Hub 的既有网络形态
	//     （Agent 在 NAT 后面，任何"让 Hub 连 Agent"的方案都不成立）。
	//
	// ============================ 任务生命周期 ============================
	//
	//	pull / save / load.begin  →  返回 task（pending → running → 终态）
	//	chunk.put（load 方向）    →  按 offset 幂等追加
	//	chunk.get（save 方向）    →  按 offset 读一块
	//	load.commit               →  校验后异步 load，返回 task
	//	task.status               →  轮询进度（也可由 Agent 主动推）
	//	task.cancel / task.release → 取消 / 释放资源
	//
	// 任务只绑定**当前 Agent 的管理器**：task_id 由 Agent 自己生成，
	// 因此 release/cancel 只可能作用于本进程创建的任务，
	// 不存在"凭外部 id 操作别人的任务"这种越权路径。
	MethodImagePull        = "image.pull"         // 从公共 registry 拉取镜像
	MethodImageSave        = "image.save"         // 导出镜像为归档（Agent 侧落盘）
	MethodImageLoadBegin   = "image.load.begin"   // 开始一次导入：声明大小与校验和
	MethodImageChunkPut    = "image.chunk.put"    // 上传一块归档字节（load 方向）
	MethodImageLoadCommit  = "image.load.commit"  // 提交导入：校验后异步 load
	MethodImageChunkGet    = "image.chunk.get"    // 读取一块归档字节（save 方向）
	MethodImageTaskStatus  = "image.task.status"  // 查询任务进度
	MethodImageTaskCancel  = "image.task.cancel"  // 取消任务
	MethodImageTaskRelease = "image.task.release" // 释放任务占用的临时归档
)

// Hub → Agent 的本地通道方法名（Agent 内部 CLI 复用同一套处理器）。
// 这些字符串就是上面的方法名本身：Agent 的处理器注册表不区分请求来自
// Hub 连接还是本地 CLI，离线自治才能与在线行为完全一致，不存在两套实现。
const (
	LocalMethodContainerList    = MethodContainerList
	LocalMethodContainerStart   = MethodContainerStart
	LocalMethodContainerStop    = MethodContainerStop
	LocalMethodContainerRestart = MethodContainerRestart
	LocalMethodContainerRemove  = MethodContainerRemove
	LocalMethodContainerLogs    = MethodContainerLogs
	LocalMethodRuntimeInfo      = MethodRuntimeInfo

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

	LocalMethodComposePreview = MethodComposePreview
	LocalMethodComposeAdopt   = MethodComposeAdopt

	// 镜像传输同样走同一套处理器：本地 CLI 也能导出/导入，
	// 与在线行为逐字节一致（决策 D-05）。
	LocalMethodImagePull        = MethodImagePull
	LocalMethodImageSave        = MethodImageSave
	LocalMethodImageLoadBegin   = MethodImageLoadBegin
	LocalMethodImageChunkPut    = MethodImageChunkPut
	LocalMethodImageLoadCommit  = MethodImageLoadCommit
	LocalMethodImageChunkGet    = MethodImageChunkGet
	LocalMethodImageTaskStatus  = MethodImageTaskStatus
	LocalMethodImageTaskCancel  = MethodImageTaskCancel
	LocalMethodImageTaskRelease = MethodImageTaskRelease
)
