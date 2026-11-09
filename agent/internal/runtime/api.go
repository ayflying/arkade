package runtime

// Docker 兼容 HTTP API 的端点常量，集中维护。
//
// Docker 与 Podman 的容器相关端点路径一致（Podman 实现了 Docker 兼容层），
// 因此两个实现共用同一组常量，禁止在实现里手写路径字符串。
const (
	pathVersion         = "/version"         // 版本与引擎类型探测
	pathPing            = "/_ping"           // 轻量存活探测，返回 200 与 "OK"
	pathContainers      = "/containers/json" // 容器列表，?all=1 含已停止容器
	pathContainerPrefix = "/containers/"     // 容器操作前缀，后接 {id}/action

	// 镜像 / 网络 / 存储卷端点（阶段三新增）。
	// 路径与容器端点同属 Docker 兼容 API，Podman 也实现了它们。
	pathImages   = "/images/json" // 镜像列表
	pathNetworks = "/networks"    // 网络列表（注意：不是 /networks/json）
	pathVolumes  = "/volumes"     // 存储卷列表

	actionStart   = "/start"
	actionStop    = "/stop"    // 支持 ?t=<秒> 优雅退出超时
	actionRestart = "/restart" // 支持 ?t=<秒> 停止超时，之后再启动
	actionKill    = "/kill"    // force 停止走 kill
	actionRemove  = "/delete"  // 实为 DELETE 方法，路径为容器自身
	actionLogs    = "/logs"
)

// 日志拉取参数：必须"按行尾取"，并允许 ANSI 控制字符原样返回。
const (
	logQueryFollow     = "false" // 控制面只取快照，不跟随实时流
	logQueryTailPrefix = "tail="
	logQueryStdout     = "stdout=1"
	logQueryStderr     = "stderr=1"
	logQueryTimestamps = "timestamps=1"
)
