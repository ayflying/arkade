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
 * 容器来源（归属）标记，见 docs/01-architecture.md 4.2：
 * - arkade     Arkade 下发创建，可自动重建
 * - adopted    外部工具部署后被接管，编排只读
 * - discovered 被发现但未接管，仅展示与基础操作
 */
export type ContainerOrigin = 'arkade' | 'adopted' | 'discovered'

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
  origin: ContainerOrigin | string
  stack_id: string
  /** Unix 秒级时间戳 */
  created: number
  /** 端口映射文本，如 "0.0.0.0:8080->80/tcp"；可能为空字符串 */
  ports: string
  /**
   * 结构化的端口映射。
   *
   * ⚠️ 旧版 Agent 不返回这个字段（undefined）。**不要**去解析 `ports` 文本
   * 猜出端口映射——文本格式在 docker 各版本间有差异，猜错就会给用户
   * 一个打不开的链接。拿不到就老老实实显示「无端口映射」。
   */
  published_ports?: PublishedPort[]
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
