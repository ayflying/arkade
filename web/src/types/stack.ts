/**
 * 容器编排（compose stack）相关类型
 *
 * 契约来源：**后端 task-4/task-5 的实际产出**（已与 Lead 逐条核对）。
 * 页面组件只依赖本文件与 api/stacks.ts，后端再变也只改这两处。
 *
 * 时间字段约定：与项目其他接口一致，时间为 **Unix 秒**，
 * 展示时一律走 utils/formatTime 换算，绝不当毫秒直接 new Date()。
 */

/** compose 项目整体状态；Agent 离线时后端返回 'offline' */
export type StackState = 'running' | 'stopped' | 'partial' | 'offline' | 'unknown'

/** 编排列表项 */
export interface StackSummary {
  /** 编排名（compose 项目名），也是后续所有子接口的路径参数 */
  name: string
  /** 整体状态 */
  status: StackState
  /** Hub 记录的配置哈希 */
  config_hash: string
  /**
   * Agent 本机文件的哈希。
   * 与 config_hash 不同即为 drift；单独暴露出来便于排查「差在哪」。
   */
  remote_hash: string
  /**
   * 漂移标记：Hub 记录的 compose.yml 哈希与 Agent 本机文件哈希不一致。
   * true 表示「有人绕过 Hub 直接改了服务器上的文件」，
   * 前端必须显式告警——否则用户会以为 Hub 里的配置就是线上真实配置。
   *
   * 注意判断用后端给的 drift 字段，不要自己比较两个 hash：
   * 后端可能对空值/未同步状态有额外判定规则。
   */
  drift: boolean
  /** compose 文件里声明的服务名列表 */
  services: string[]
  /**
   * 运行中的服务数。
   * 字段名就是 `running`（不是 services_running），
   * 与 `total` 配对使用。
   */
  running: number
  /** 服务总数 */
  total: number
  /** 配置版本号，乐观锁用 */
  version: number
  /** 最后更新时间（Unix 秒） */
  updated_at: number
  /** 最后一次成功同步到 Agent 的时间（Unix 秒）；0 表示从未同步成功 */
  synced_at: number
  /** 所属 Agent id */
  agent_id: string
}

/**
 * 服务器上已存在、但未被 Arkade 纳管的 compose 项目。
 *
 * 来源：Agent 扫描引擎标签发现的外部项目（如用户直接用 docker compose 起的）。
 * Arkade **只读不写**：不读取其 compose 内容、不覆盖、不执行任何动作、不自动纳管。
 *
 * 为什么必须单独成表而不是只在顶部放几个标签：
 * 用户反馈「编排列表是空的但设备上明明有很多编排」——真实原因就是这些项目
 * 都在 Arkade 工作目录之外。只在顶部列个名字，主表仍然是空的，
 * 用户会认为功能坏了。必须让它们在页面上**可见地存在**，并说明为什么不能操作。
 */
export type ExternalStack = {
  /** 项目名（compose project name） */
  name: string
  /** 项目状态；后端可能给 running/exited/partial 等，未知值按原样展示 */
  status: string
  /** compose 中声明的服务数；后端给不出时可为 0 */
  services: number
  /** 实际运行中的容器数 */
  containers_running: number
  /** 该项目下的容器总数 */
  containers_total: number
}

/**
 * 编排列表响应。
 *
 * `orphans` 是「Agent 上有、Hub 数据库里没有」的编排名：
 * 这类编排无法通过本界面管理（界面数据来自 Hub），
 * 但**必须显示出来**，否则用户永远发现不了它们的存在。
 *
 * 注意与 `external_stacks` 的区别——两者都是「未纳管」，但信息量不同：
 * - `orphans` 只有名字（string[]），兼容旧后端，只说得出「有这个项目」；
 * - `external_stacks` 带状态与服务/容器数，能画成一张有信息量的表。
 * 后端提供 `external_stacks` 时优先用它渲染；只有 `orphans` 时退化成名字列表。
 */
export interface StackListResponse {
  stacks: StackSummary[]
  /** Agent 是否在线；离线时列表仍返回 200，各条 status 为 'offline' */
  agent_online: boolean
  /** 存在于 Agent 本机但未被 Hub 纳管的编排名（旧字段，兼容保留） */
  orphans?: string[]
  /** 未被纳管的 compose 项目详情（新字段，可能暂缺） */
  external_stacks?: ExternalStack[]
}

/** GET /api/agents/{id}/stacks/{name} 响应：编排详情 */
export interface StackDetail {
  name: string
  status: StackState
  /** 服务名列表 */
  services: string[]
  version: number
  updated_at: number
  drift: boolean
}

/** 编排下的单个服务（日志页下拉用；详情接口只给名字） */
export interface StackService {
  name: string
}

/** GET /api/agents/{id}/stacks/{name}/config 响应：compose.yml 原文 */
export interface StackConfig {
  /** compose.yml 文本内容（编辑器里直接展示） */
  content: string
  version: number
}

/** 编排可执行的操作 */
export type StackAction =
  | 'start' // 启动
  | 'stop' // 停止
  | 'restart' // 重启
  | 'pull' // 更新镜像
  | 'recreate' // 重建
  | 'backup' // 备份
  | 'delete' // 删除

/** POST /api/agents/{id}/stacks/{name}/actions 请求体 */
export interface StackActionRequest {
  action: StackAction
  /** 只对指定服务操作；不传表示全部服务 */
  services?: string[]
  /**
   * 删除时是否同时删除数据卷。
   * 默认为 false（保留卷）——删卷是不可恢复的数据丢失，
   * 必须由用户在确认框里显式勾选才会置为 true。
   */
  remove_volumes?: boolean
  /** 备份时是否包含数据卷（只有 backup 操作使用） */
  include_volumes?: boolean
}

/** 操作响应 */
export interface StackActionResponse {
  /** 服务端接受操作后的编排状态；异步场景下可能仍是旧状态 */
  status: StackState
  message?: string
}

/** GET /api/agents/{id}/stacks/{name}/ps 响应：该编排下的容器 */
export interface StackPsContainer {
  id: string
  name: string
  /** 所属服务名 */
  service: string
  state: string
  status: string
  image: string
}

export interface StackPsResponse {
  containers: StackPsContainer[]
}

/** GET /api/agents/{id}/stacks/{name}/logs 响应 */
export interface StackLogsResponse {
  lines: string[]
  truncated: boolean
}

/**
 * 编排写入类响应（新建 / 保存配置）共有的同步结果。
 *
 * ## 为什么单独有这组字段
 *
 * 当 Agent 离线或下发失败时，后端**不会报错**，而是照常返回 200，
 * 用 `sync_ok: false` + `sync_hint` 告知「内容已存在 Hub，但还没下发到服务器」。
 * 这是「离线也不丢用户内容」的设计：用户写的东西先落库，等 Agent 上线再同步。
 *
 * 因此前端绝对不能把 200 当成功就完事——必须看 sync_ok，
 * 否则用户会以为已经部署成功了，实际上服务器上什么都没变。
 */
export interface StackSyncResult {
  sync_ok: boolean
  /** 未同步成功时的中文说明，直接展示给用户 */
  sync_hint?: string
}

/** POST /api/agents/{id}/stacks 请求体：新建编排 */
export interface CreateStackRequest {
  name: string
  /** compose.yml 全文 */
  content: string
}

/** 新建编排响应：编排信息 + 同步结果 */
export interface CreateStackResponse extends StackSyncResult {
  name: string
  status: StackState
  version: number
}

/** PUT /api/agents/{id}/stacks/{name} 请求体：保存配置 */
export interface UpdateStackRequest {
  content: string
  /** 乐观锁版本号，必须与读取时一致 */
  version: number
}

/** 保存配置响应：需要新的 version 供连续编辑，外加同步结果 */
export interface UpdateStackResponse extends StackSyncResult {
  version: number
}
