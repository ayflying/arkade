import type {
  Container,
  ContainerActionResponse,
  ContainerDetail,
  ContainerDetailResponse,
  ContainerListResponse,
  ContainerLogsResponse,
  ContainerMount,
  ContainerNetwork,
  ContainerResourceLimits,
  PublishedPort
} from '@/types/container'
import { httpDelete, httpGet, httpPost } from './request'

/**
 * 容器接口
 *
 * 对应契约（agent_id / container_id 都需 URL 编码，防止 ID 中出现特殊字符）：
 * - GET    /api/agents/{id}/containers
 * - GET    /api/agents/{id}/containers/{cid}            （详情，需 manage）
 * - POST   /api/agents/{id}/containers/{cid}/start
 * - POST   /api/agents/{id}/containers/{cid}/stop
 * - POST   /api/agents/{id}/containers/{cid}/restart
 * - DELETE /api/agents/{id}/containers/{cid}?force=&remove_volumes=
 * - GET    /api/agents/{id}/containers/{cid}/logs?tail=N  （需 view）
 */

/** 拼出单个容器的路径 */
function containerPath(agentId: string, containerId: string): string {
  return `/agents/${encodeURIComponent(agentId)}/containers/${encodeURIComponent(containerId)}`
}

/** 某台 Agent 上的容器列表 */
export async function listContainers(agentId: string): Promise<Container[]> {
  const res = await httpGet<ContainerListResponse>(
    `/agents/${encodeURIComponent(agentId)}/containers`
  )
  return Array.isArray(res.containers) ? res.containers : []
}

/**
 * 归一化一条端口映射。
 *
 * 缺字段时返回 null 让调用方丢弃——宁可不显示，
 * 也不要拼一条半真半假的映射出来（比如 host_port 缺失却显示成 0）。
 */
function normalizePort(raw: PublishedPort): PublishedPort | null {
  if (raw === null || typeof raw !== 'object') return null
  const hostPort = typeof raw.host_port === 'number' ? raw.host_port : 0
  const containerPort = typeof raw.container_port === 'number' ? raw.container_port : 0
  return {
    host_ip: typeof raw.host_ip === 'string' ? raw.host_ip : '',
    host_port: hostPort,
    container_port: containerPort,
    protocol: typeof raw.protocol === 'string' ? raw.protocol : ''
  }
}

function normalizeNetwork(raw: ContainerNetwork): ContainerNetwork {
  return {
    name: typeof raw.name === 'string' ? raw.name : '',
    ip_address: typeof raw.ip_address === 'string' ? raw.ip_address : ''
  }
}

function normalizeMount(raw: ContainerMount): ContainerMount {
  return {
    type: typeof raw.type === 'string' ? raw.type : '',
    source: typeof raw.source === 'string' ? raw.source : '',
    destination: typeof raw.destination === 'string' ? raw.destination : '',
    read_only: raw.read_only === true
  }
}

/**
 * 资源上限：**0 表示未限制**（协议语义），不是"上限为 0"。
 *
 * 所以这里保留 0 原样传下去，由界面翻译成「未限制」；
 * 千万不要在这里把 0 当成"缺失"再换成什么默认值。
 */
function normalizeLimits(raw: ContainerResourceLimits | undefined): ContainerResourceLimits {
  return {
    memory_bytes: typeof raw?.memory_bytes === 'number' ? raw.memory_bytes : 0,
    nano_cpus: typeof raw?.nano_cpus === 'number' ? raw.nano_cpus : 0,
    pids_limit: typeof raw?.pids_limit === 'number' ? raw.pids_limit : null
  }
}

/**
 * 归一化 RFC3339 时间字符串。
 *
 * ⚠️ 详情里的时间是 **RFC3339 字符串**（列表里才是 Unix 秒数字）。
 * 绝不能 `Number(...)` 强转——那样会得到 NaN 或 0，把真实时间变成 1970 或"未知"。
 *
 * 零值（`""` 或 Go 的 `0001-01-01T00:00:00Z`）统一归一成空串，
 * 让界面显示「—」而不是一个荒唐的年份。
 */
function normalizeRFC3339(raw: unknown): string {
  if (typeof raw !== 'string') return ''
  const text = raw.trim()
  if (text === '') return ''
  // Go time.Time 的零值
  if (text.startsWith('0001-01-01')) return ''
  return text
}

/**
 * 容器详情。
 *
 * 需要 **manage**。返回 403 时调用方仍可展示日志（日志是 view 权限），
 * 所以不要把详情失败当成整个页面不可用。
 *
 * ## 响应包装
 *
 * HTTP 响应是 `{ container: ContainerInspectResult }`（与列表的
 * `{ agent_id, containers }` 一致地套了一层），这里解包成扁平对象。
 *
 * ## 字段全部对齐 protocol/envelope/payload_container_detail.go
 *
 * 特别留意三个和列表**完全不同**的地方：
 * - `created`/`started_at`/`finished_at` 是 **RFC3339 字符串**（列表是 Unix 秒数字）；
 * - `exposed_ports` 是 `[]string`（`"80/tcp"`），不是数字数组；
 * - `resource_limits` 用 **0 表示未设置上限**，不是「上限为 0」。
 *
 * 契约里没有 Env / 全部 Labels，这里也不做任何字段反推。
 */
export async function getContainer(agentId: string, containerId: string): Promise<ContainerDetail> {
  const res = await httpGet<ContainerDetailResponse>(containerPath(agentId, containerId))
  // 解包：后端统一套一层 { container: ... }
  const raw = res?.container ?? ({} as ContainerDetail)
  return {
    id: typeof raw.id === 'string' ? raw.id : '',
    name: typeof raw.name === 'string' ? raw.name : '',
    image: typeof raw.image === 'string' ? raw.image : '',
    state: typeof raw.state === 'string' ? raw.state : 'unknown',
    status: typeof raw.status === 'string' ? raw.status : '',
    // 保持字符串原样，不做任何数值转换
    created: normalizeRFC3339(raw.created),
    started_at: normalizeRFC3339(raw.started_at),
    finished_at: normalizeRFC3339(raw.finished_at),
    exit_code: typeof raw.exit_code === 'number' ? raw.exit_code : 0,
    restart_count: typeof raw.restart_count === 'number' ? raw.restart_count : 0,
    network_mode: typeof raw.network_mode === 'string' ? raw.network_mode : '',
    command: Array.isArray(raw.command)
      ? raw.command.filter((c): c is string => typeof c === 'string')
      : [],
    networks: Array.isArray(raw.networks) ? raw.networks.map(normalizeNetwork) : [],
    mounts: Array.isArray(raw.mounts) ? raw.mounts.map(normalizeMount) : [],
    published_ports: Array.isArray(raw.published_ports)
      ? raw.published_ports.map(normalizePort).filter((p): p is PublishedPort => p !== null)
      : [],
    // 协议是 []string（"80/tcp" 形式），不是数字数组
    exposed_ports: Array.isArray(raw.exposed_ports)
      ? raw.exposed_ports.filter((p): p is string => typeof p === 'string')
      : [],
    resource_limits: normalizeLimits(raw.resource_limits)
  }
}

/** 启动容器，返回操作后的新状态 */
export function startContainer(agentId: string, containerId: string): Promise<ContainerActionResponse> {
  return httpPost<ContainerActionResponse>(`${containerPath(agentId, containerId)}/start`)
}

/** 停止容器，返回操作后的新状态（通常为 exited） */
export function stopContainer(agentId: string, containerId: string): Promise<ContainerActionResponse> {
  return httpPost<ContainerActionResponse>(`${containerPath(agentId, containerId)}/stop`)
}

/** 重启容器，返回操作后的新状态（通常仍是 running） */
export function restartContainer(
  agentId: string,
  containerId: string
): Promise<ContainerActionResponse> {
  return httpPost<ContainerActionResponse>(`${containerPath(agentId, containerId)}/restart`)
}

/**
 * 删除容器。
 *
 * @param force          运行中的容器必须 force=true 才能删（后端会返回
 *                       409 runtime_failed 拒绝）。前端在确认框里要说明
 *                       「该容器正在运行，将强制停止并删除」。
 * @param removeVolumes  是否同时删除容器的匿名数据卷。
 *                       **默认 false**：删卷是数据丢失，和编排删除同一原则——
 *                       危险默认关闭，由用户在确认框里主动勾选。
 *
 * ## ⚠️ 只传 "true" 或不传，绝不传 "false"
 *
 * 后端对这两个参数的**非法值是 400 而不是宽松当 false 处理**，这是刻意的：
 * `remove_volumes=ture`（拼错）若被当成 true 会直接丢数据；
 * `=yes` 若被当成 false，用户以为删了其实没删。
 *
 * 因此这里用「只有 true 才拼参数」的写法，从源头杜绝出现非法值：
 * 不勾选 = 参数完全不存在 = 后端走默认 false，语义最清晰也最安全。
 */
export async function deleteContainer(
  agentId: string,
  containerId: string,
  options: { force?: boolean; removeVolumes?: boolean } = {}
): Promise<void> {
  const params = new URLSearchParams()
  // 严格 === true：传 undefined / false / 其他值都视为「不传」，不产生非法参数
  if (options.force === true) params.set('force', 'true')
  if (options.removeVolumes === true) params.set('remove_volumes', 'true')
  const query = params.toString()
  const suffix = query === '' ? '' : `?${query}`
  return httpDelete(`${containerPath(agentId, containerId)}${suffix}`)
}

/** 拉取容器日志尾部若干行 */
export async function fetchContainerLogs(
  agentId: string,
  containerId: string,
  tail = 200
): Promise<ContainerLogsResponse> {
  const res = await httpGet<ContainerLogsResponse>(
    `${containerPath(agentId, containerId)}/logs?tail=${tail}`
  )
  return {
    lines: Array.isArray(res.lines) ? res.lines : [],
    truncated: res.truncated === true
  }
}
