/**
 * 容器编排（compose stack）相关类型
 *
 * 契约来源：**后端 task-4/task-5 的实际产出**（已与 Lead 逐条核对）。
 * 页面组件只依赖本文件与 api/stacks.ts，后端再变也只改这两处。
 *
 * 时间字段约定：与项目其他接口一致，时间为 **Unix 秒**，
 * 展示时一律走 utils/formatTime 换算，绝不当毫秒直接 new Date()。
 */

/**
 * compose 项目整体状态；Agent 离线时后端返回 'offline'。
 *
 * `busy` 表示**该项目正在执行操作**（Agent 侧的互斥锁生效期间）。
 * 这个状态的含义是「读到的是半写状态，不可信」，因此：
 * - 不能当成 running，也不能当成 stopped —— 两种情况都会误导用户；
 * - 期间必须禁用变更类操作，否则会与正在进行的操作互相踩。
 */
export type StackState = 'running' | 'stopped' | 'partial' | 'offline' | 'busy' | 'unknown'

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
  /**
   * compose 文件里声明的服务名列表。
   *
   * `null` 表示**未知**（后端没给这个字段，或 Agent 离线拿不到）——
   * 与「空数组 = 确实没有服务」是两回事，界面上要分别显示「—」和「0 个」。
   */
  services: string[] | null
  /**
   * 运行中的服务数。
   * 字段名就是 `running`（不是 services_running），与 `total` 配对使用。
   *
   * **`null` 表示未知，不是 0**：Agent 离线或字段缺失时不能伪造成「0 个在跑」，
   * 那会让用户以为服务全挂了。界面显示「—」。
   */
  running: number | null
  /** 服务总数；`null` 表示未知，理由同上 */
  total: number | null
  /** 配置版本号，乐观锁用 */
  version: number
  /** 最后更新时间（Unix 秒） */
  updated_at: number
  /** 最后一次成功同步到 Agent 的时间（Unix 秒）；0 表示从未同步成功 */
  synced_at: number
  /**
   * 未同步原因，可选。**目前只在自动接管失败时由 Hub 带上**。
   *
   * 语义要点：
   * - 该原因是 Hub **内存里**记的，不是持久化的：Hub 重启后未同步的记录
   *   仍存在（`synced_at` 为 0），但这里会变空 → 前端必须能退回通用文案，
   *   **不能因为拿不到原因就装作同步成功**；
   * - 有它时说明「配置已记录在 Hub，但没下发到服务器」，此时**不要启动或重建**；
   * - 后台下一轮扫描会自动重试，用户通常不需要手动做什么。
   *
   * 注意与 `external_stacks` 的 `auto_adopt_reason` 区分：
   * 那边是「Hub 判定这个外部项目不能接管」，这边是「接管动作已经发起但失败了」。
   */
  sync_hint?: string
  /** 所属 Agent id */
  agent_id: string
}

/**
 * 自动接管状态。
 *
 * 语义词来自后端（Hub 在 Agent 上线后约 30 秒扫描一次，尝试自动接管
 * 兼容的外部 compose 项目）。**接管不重启、不重建容器**，只是把配置纳入管理。
 *
 * `adopted` 不在这里出现：接管成功后项目会从 `external_stacks`
 * 移入正式的 `stacks` 列表，未纳管表里不该再出现它。
 *
 * `unknown` 是**前端兜底值**，不是后端语义：旧 Hub 不返回该字段时用它，
 * 表示「这台 Hub 还没这个能力」。此时界面**不显示任何接管相关文案**，
 * 绝不推测成 skipped/failed——否则旧后端会满屏假原因。
 */
export type AutoAdoptStatus = 'pending' | 'skipped' | 'failed' | 'unknown'

/**
 * 服务器上已存在、但未被 Arkade 纳管的 compose 项目。
 *
 * 来源：Agent 扫描引擎标签发现的外部项目（如用户直接用 docker compose 起的）。
 * Arkade **只读不写**：不读取其 compose 内容、不覆盖、不执行任何动作。
 *
 * 自动接管（Hub 后台扫描）会把**兼容**的项目纳入管理并移出本列表；
 * 不能接管的留在这里，并通过 `auto_adopt_status` / `auto_adopt_reason`
 * 告知用户原因。仍保留手动「导入纳管」作为不支持自动接管场景的兜底。
 *
 * 为什么必须单独成表而不是只在顶部放几个标签：
 * 用户反馈「编排列表是空的但设备上明明有很多编排」——真实原因就是这些项目
 * 都在 Arkade 工作目录之外。只在顶部列个名字，主表仍然是空的，
 * 用户会认为功能坏了。必须让它们在页面上**可见地存在**，并说明为什么不能操作。
 */
export type ExternalStack = {
  /** 项目名（compose project name） */
  name: string
  /** 项目状态；后端可能给 running/partial/stopped/unknown，未知值按原样展示 */
  status: string
  /**
   * compose 中声明的**服务名列表**（后端已排序，顺序稳定）。
   *
   * ⚠️ 是 `string[]`，不是服务数量。想显示数量请用 `services.length`。
   * 之前这里错写成 number，导致真实响应进来时整个字段是 undefined，
   * 页面上直接显示「undefined」，而自造的 mock 恰好按错的形状返回，
   * 所以测试全绿却漏检——**mock 照着前端类型编，等价于自己证明自己**。
   *
   * `null` 表示未知（字段缺失/Agent 离线），与「空数组=确实 0 个服务」区分开。
   */
  services: string[] | null
  /** 实际运行中的容器数；`null` 表示未知，**不能当成 0** */
  running: number | null
  /** 该项目下的容器总数；`null` 表示未知，**不能当成 0** */
  total: number | null
  /**
   * 恒为 false，且后端**显式给出**而不是省略。
   *
   * 用途：前端据此统一走「不显示任何操作按钮」的渲染分支，
   * 而不是靠「这个对象没有 id」来推断只读性——隐式判断在字段变更时极易失效。
   */
  managed: boolean
  /**
   * 自动接管状态。**可选**：旧 Hub 不返回该字段。
   *
   * 归一化后一定有值（缺省为 `'unknown'`），页面拿到的永远不是 undefined。
   */
  auto_adopt_status?: AutoAdoptStatus
  /**
   * 暂不能接管的原因，直接展示给用户的人话（如「服务使用了 build，不支持自动接管」）。
   *
   * 可能是整句中文，因此界面上**一行截断 + tooltip**，
   * 且必须有可点击以外的可读路径（手机上没有 hover）。
   */
  auto_adopt_reason?: string
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

/**
 * GET /api/agents/{id}/stacks/{name} 详情，**api 层解包后**的形状。
 *
 * 后端真实返回 `{ stack: {...} }` 一层包装：
 * `stack` 就是列表里的 composeStackView。
 * 见 hub/internal/httpx/handlers_compose.go 的 handleGetStack。
 *
 * ⚠️ 详情**不再返回 compose 原文**：配置（含 .env）是敏感内容，
 * 只走 `GET .../config`（manage 权限 + no-store）。
 * 因此编辑器的内容必须从 getStackConfig 取，不能指望详情里有。
 *
 * 解包放在 api/stacks.ts 里完成，页面拿到的永远是扁平的 StackDetail，
 * 否则每个调用点都要记得多剥一层，早晚有人写成 `detail.name` 拿到 undefined。
 */
export interface StackDetail extends StackSummary {
  /**
   * compose 原文。**后端已不再随详情返回**，保留字段仅为兼容旧响应，
   * 正常情况下是 undefined —— 需要内容请用 getStackConfig。
   */
  content?: string
}

/** 编排下的单个服务（日志页下拉用；详情接口只给名字） */
export interface StackService {
  name: string
}

/** GET /api/agents/{id}/stacks/{name}/config 响应：compose.yml 与 .env 原文 */
export interface StackConfig {
  /** compose.yml 文本内容（编辑器里直接展示） */
  content: string
  /**
   * 与 compose 同目录的 `.env` 文本内容（可为空串）。
   *
   * 用途：compose 里的 `${VAR}` 由它插值。**不是**把整个文件灌进容器环境——
   * 只有 compose 显式引用的变量才会进入容器。
   *
   * 后端可能暂未返回该字段（旧版本），因此读取时一律做 `?? ''` 兜底，
   * 不要直接 `.trim()`，否则会炸在 undefined 上。
   */
  env_content: string
  version: number
}

/** 编排可执行的操作（**前端语义**，与后端 action 名不同，映射见 api/stacks.ts） */
export type StackAction =
  | 'start' // 启动 → 后端 up
  | 'stop' // 停止 → 后端 stop
  | 'restart' // 重启 → 后端 restart
  | 'pull' // 更新镜像 → 后端 pull
  | 'recreate' // 重建 → 后端 rebuild
  | 'backup' // 备份 → 后端 backup
  | 'delete' // 删除 → 后端 remove

/**
 * DELETE /api/agents/{id}/stacks/{name} 响应。
 *
 * 不是 204：后端返回 200 + 这份 JSON（见 handleDeleteStack）。
 * 执行失败时不会返回 200 —— 而是报错并**保留管理记录**，
 * 避免「机器上还在跑、界面里却消失了」的不可管理状态。
 */
export interface DeleteStackResponse {
  name: string
  sync_ok: boolean
  sync_hint?: string
  /** 删除前该编排的配置哈希 */
  config_hash?: string
}

/**
 * POST /api/agents/{id}/stacks/import-preview 请求体。
 *
 * 预览**只做校验，不动任何东西**：不读宿主路径、不保存、不执行 up/down。
 * 用户要把内容贴进来是因为 Agent 跑在容器里，未必能读宿主上的 compose 路径。
 */
export interface ImportPreviewRequest {
  name: string
  content: string
  env_content: string
}

/**
 * 导入预检结果。
 *
 * `can_import` 由后端综合判断（三件事同时成立才为真：
 * Agent 认可、哈希一致、没有任何 blocker）。
 * 前端**不能自己根据 blockers 空就认为可导入**——必须直接看这个布尔值。
 */
export interface ImportPreviewResponse {
  name: string
  /** 本次提交内容的哈希；确认导入时必须原样带回，防止内容变了还被导入 */
  config_hash: string
  can_import: boolean
  /**
   * 阻塞项：必须全部解决才能导入。
   * 例如不支持的相对挂载、build、env_file、secrets 等文件依赖。
   * 这些**不做兼容伪装**——说清楚不能导，比导进去跑不起来好。
   */
  blockers: string[]
  /** 警告项：可以导入，但要提醒用户注意 */
  warnings: string[]
}

/**
 * POST /api/agents/{id}/stacks/import 请求体。
 *
 * 必须同时给出 `expected_content_hash` 与 `confirmation: true`：
 * 哈希不一致说明用户在预检后又改了内容，后端会要求重新预检。
 * 这两道是「用户确实看过预检结果并明确同意」的凭证，不是冗余字段。
 */
export interface ImportStackRequest {
  name: string
  content: string
  env_content: string
  /** 预检返回的 config_hash，原样带回 */
  expected_content_hash: string
  /** 必须为 true，表示用户已看过预检并确认导入 */
  confirmation: true
}

/**
 * 导入结果（**api 层解包后**）。
 *
 * 后端返回 `{stack, sync_ok, sync_hint, adopted}`。
 * 导入**只复制配置**，绝不自动 up/down/rebuild——
 * 所以成功后容器状态不会变，用户要自己再点启动。
 */
export interface ImportStackResponse extends StackSyncResult {
  stack: StackSummary
  /** Agent 是否成功接管（false 时配置已存 Hub，但 Agent 侧还没接上） */
  adopted: boolean
}

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

/**
 * 操作响应。
 *
 * 后端真实形状是 `{ agent_id, name, status, config_hash, output }`
 * （见 handlers_compose_common.go 的 composeOpResultToView）。
 * 之前这里写的 `message` 字段后端根本不存在，永远拿到 undefined。
 */
export interface StackActionResponse {
  /** 服务端接受操作后的编排状态；异步场景下可能仍是旧状态 */
  status: StackState | string
  /** 操作命令的输出摘要（可能被后端截断） */
  output?: string
  /** 编排名 */
  name?: string
  /** 操作后 Agent 侧计算出的配置哈希 */
  config_hash?: string
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
  /**
   * 与 compose 同目录的 `.env` 全文（可为空串）。
   *
   * 用途：给 compose 里的 `${VAR}` 提供插值值。
   * **不是**「把这些变量全部注入容器」——只有 compose 里显式引用的才会进容器。
   * 内容里可能有密码，因此绝不写入任何浏览器存储，离开页面即丢弃。
   */
  env_content: string
}

/**
 * 新建编排响应（**api 层解包后**）。
 *
 * 后端真实返回 `{ stack, sync_ok, sync_hint, result }`，
 * 其中 `stack` 是完整的 composeStackView（含 name/status/version/...）。
 * 之前这里把 name/status/version 平铺在顶层，是照着自己造的 mock 写的，
 * 真实响应里这些字段都在 `stack` 下面——必然全是 undefined。
 */
export interface CreateStackResponse extends StackSyncResult {
  /** 创建后的编排视图，含 name/status/version 等 */
  stack: StackSummary
}

/** PUT /api/agents/{id}/stacks/{name} 请求体：保存配置 */
export interface UpdateStackRequest {
  content: string
  /** 乐观锁版本号，必须与读取时一致 */
  version: number
  /**
   * `.env` 全文。语义**三种，必须区分清楚**：
   *
   * - **省略该字段** → 保留服务器上原来的 `.env` 不变（用户只改 compose 时用）；
   * - **空串 `""`** → 显式清空 `.env`；
   * - **任意字符串** → 整体覆盖。
   *
   * 之所以用「省略 vs 空串」而不是额外加一个布尔开关：
   * 布尔开关和内容可能不一致（如 `clear=true` 但还带了内容），
   * 用「字段在不在」表达意图没有这种歧义。
   *
   * 编辑 .env 时拿不准用户是「没改」还是「想清空」，
   * 因此只有在用户**真的动过** .env 编辑器时才带上这个字段。
   */
  env_content?: string
}

/**
 * 保存配置响应（**api 层解包后**）。
 *
 * 后端真实返回 `{ stack, sync_ok, sync_hint, result }`。
 * 连续编辑需要的新版本号在 `stack.version` 里，不在顶层。
 */
export interface UpdateStackResponse extends StackSyncResult {
  /** 保存后的编排视图，`version` 是新的乐观锁版本号 */
  stack: StackSummary
}
