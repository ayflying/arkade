/**
 * 容器相关类型
 *
 * 对应后端契约：
 * - GET  /api/agents/{id}/containers                     → { agent_id, containers: Container[] }
 * - POST /api/agents/{id}/containers/{cid}/start         → { container_id, state }
 * - POST /api/agents/{id}/containers/{cid}/stop          → { container_id, state }
 * - GET  /api/agents/{id}/containers/{cid}/logs?tail=200 → { lines: string[], truncated: boolean }
 */

/**
 * 一个已发布的端口映射（结构化）。
 *
 * 为什么必须结构化：文本形式（`0.0.0.0:8080->80/tcp`）里 `0.0.0.0`
 * 是通配绑定，**不是**能在浏览器里打开的地址，直接拼成链接会指向一个
 * 必然失败的地方。结构化后前端才能决定该替换成什么、该不该做成链接。
 *
 * 对应 protocol/envelope/payload_container_detail.go 的 PublishedPort。
 */
export interface PublishedPort {
  /** 宿主侧绑定地址；可能是 `0.0.0.0`、`::`、`127.0.0.1` 或具体 IP */
  host_ip: string
  /** 宿主端口；协议为 uint16，0 表示未实际映射 */
  host_port: number
  /** 容器内端口 */
  container_port: number
  /** `tcp` / `udp` / `sctp` */
  protocol: string
}

export interface Container {
  /** 短 ID 或长 ID，后端给哪个用哪个 */
  id: string
  name: string
  image: string
  /** 运行时状态字符串，如 running / exited / paused / created */
  state: string
  /** 人类可读状态，如 "Up 3 hours" */
  status: string
  /**
   * 归属来源。协议里仍在（Agent 目前恒为 discovered），但**界面不再展示**。
   *
   * 原因：容器没有"接管"这个状态。Arkade 里只有编排项目会被纳管/接管，
   * 容器只可能属于某个已纳管编排（看 stack_id）或不属于任何编排。
   * 之前列表里按 origin 显示"方舟部署 / 已接管 / 外部项目"，
   * 其中的"已接管"永远不会出现——Agent 侧那个常量从未被赋值——
   * 而"外部项目"又让用户以为容器在等接管。既然没有可靠且有用的信息，
   * 就不显示。保留字段只为不破坏协议形状，将来真要区分再补后端判据。
   */
  origin: string
  /**
   * 所属编排项目名（compose 的 com.docker.compose.project 标签）。
   * 空字符串表示这个容器不属于任何编排，是手工 docker run 起的。
   */
  stack_id: string
  /** Unix 秒级时间戳 */
  created: number
  /** 端口映射文本，如 "0.0.0.0:8080->80/tcp"；可能为空字符串 */
  ports: string
  /**
   * 结构化的端口映射。
   *
   * 协议使用 omitempty，无发布端口时字段可省略。**不要**去解析 `ports` 文本
   * 猜出端口映射——文本格式在 docker 各版本间有差异，猜错就会给用户
   * 一个打不开的链接。拿不到就老老实实显示「无端口映射」。
   */
  published_ports?: PublishedPort[]
  /** Summary networks; never substitute the server direct address for a container IP. */
  networks?: ContainerNetwork[]
  network_mode?: string
}

/** GET /api/agents/{id}/containers 响应体 */
export interface ContainerListResponse {
  agent_id: string
  containers: Container[]
}

/** 启停容器接口的响应体 */
export interface ContainerActionResponse {
  container_id: string
  /** 操作后的新状态，如 running / exited */
  state: string
}

/** GET .../logs 响应体 */
export interface ContainerLogsResponse {
  lines: string[]
  /** 日志是否被截断（tail 限制导致） */
  truncated: boolean
}

/** 网络配置：容器在某个网络里的地址 */
export interface ContainerNetwork {
  name: string
  ip_address: string
  ipv6_address?: string
}

/** 挂载点 */
export interface ContainerMount {
  /** `bind` / `volume` / `tmpfs` 等 */
  type: string
  /** 宿主侧路径或卷名 */
  source: string
  /** 容器内路径 */
  destination: string
  read_only: boolean
}

/**
 * 资源上限。
 *
 * ⚠️ 协议注释明确：**0 表示"未设置上限"**（daemon 默认语义），不是"上限为 0"。
 * 所以界面上 0 要显示成「未限制」，绝不能显示成 "0 字节"。
 *
 * `pids_limit` 是 `*int64`（可为 null），与另外两个字段的语义不同：
 * 它是真的"没这个字段"，而内存/CPU 用 0 表达未限制。
 */
export interface ContainerResourceLimits {
  memory_bytes: number
  nano_cpus: number
  /** null = 未设置（协议里是带 omitempty 的指针） */
  pids_limit: number | null
}

/**
 * GET /api/agents/{id}/containers/{cid} 响应体：容器详情。
 *
 * 对应 protocol/envelope/payload_container_detail.go 的 ContainerInspectResult，
 * **逐字段对齐**，不要凭列表字段推测。
 *
 * 与列表的关键差异（都是真的坑）：
 * - `created` 在**详情里是 RFC3339 字符串**，而列表里是 Unix 秒数字；
 * - `started_at` / `finished_at` 也是 RFC3339 字符串，未启动/未结束时为**零值字符串**；
 * - `exposed_ports` 是 `[]string`，形如 `"80/tcp"`，不是数字数组。
 *
 * 需要 **manage** 权限（含挂载宿主路径、command 参数等敏感信息）。
 * 日志仍是 view 权限，详情 403 时日志照样能看。
 *
 * ⚠️ 契约里**没有** Env、没有全部 Labels：后端刻意裁剪，前端也不要反推。
 */
export interface ContainerDetail {
  id: string
  name: string
  image: string
  state: string
  status: string
  /** RFC3339 字符串；注意**不是** Unix 秒 */
  created: string
  /** RFC3339 字符串；未启动时为零值字符串 `""` 或 `0001-01-01T00:00:00Z` */
  started_at: string
  /** RFC3339 字符串；未结束时同上 */
  finished_at: string
  /** 退出码；协议里是 int，不是指针 */
  exit_code: number
  restart_count: number
  /** `bridge` / `host` / `none` 等 */
  network_mode: string
  /** 启动命令（Entrypoint + Cmd 合并）；空数组表示用镜像默认入口 */
  command: string[]
  networks: ContainerNetwork[]
  mounts: ContainerMount[]
  published_ports: PublishedPort[]
  /** 声明暴露但**未发布**到宿主的端口，形如 `"80/tcp"` */
  exposed_ports: string[]
  resource_limits: ContainerResourceLimits
}

/**
 * GET /api/agents/{id}/containers/{cid} 的**原始响应体**。
 *
 * 后端与其他容器接口一致地套了一层：`{ container: {...} }`。
 * API 层负责解包，页面拿到的永远是扁平的 ContainerDetail。
 */
export interface ContainerDetailResponse {
  container: ContainerDetail
}

/**
 * 容器内一次性执行命令的结果（`POST .../exec` 的载荷）。
 *
 * ## 与协议逐字段对齐
 *
 * 见 `protocol/envelope/payload_container_exec.go`。三个语义点必须记住：
 *
 * 1. `exit_code` 为 **-1 表示无法确定**（超时或查询失败），不是"命令以 -1 退出"；
 * 2. `timed_out` 为真时我们只是**停止等待**，容器内的进程可能仍在运行；
 * 3. `truncated` 为真表示输出被上限截断，用户看到的不是全部输出。
 *
 * 这三条都不能在前端"猜测兜底"：把 -1 显示成 0 会让用户以为命令成功。
 */
export interface ContainerExecResult {
  exit_code: number
  stdout: string
  stderr: string
  truncated: boolean
  timed_out: boolean
  /** 实际耗时（毫秒），用于识别慢命令 */
  duration_ms: number
}

/** `POST .../exec` 的原始响应包装 */
export interface ContainerExecResponse {
  result: ContainerExecResult
}

/**
 * 执行命令的入参。
 *
 * `command` 是**命令向量**，不经过 shell 解析：
 * 要跑 `ps aux | head -5` 必须写成 `['/bin/sh', '-c', 'ps aux | head -5']`。
 * 直接给单个字符串会被当成可执行文件名，得到"文件不存在"。
 */
export interface ContainerExecInput {
  command: string[]
  /** 空表示镜像默认用户；不要在前端硬编码 root */
  user?: string
  /** 空表示镜像默认工作目录 */
  workdir?: string
  /** 秒；后端会夹到 1..120，0 表示用默认 30 秒 */
  timeout_sec?: number
}
