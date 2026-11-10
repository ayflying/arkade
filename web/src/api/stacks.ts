/**
 * 容器编排（stacks）接口
 *
 * 契约来源：**后端 handlers_compose.go 的真实实现**，逐字段核对过。
 * 页面组件只依赖本文件与 types/stack.ts，后端再变也只改这两处。
 *
 * ## 本文件的两个职责（都是为了让页面不用记后端形状）
 *
 * 1. **解包**：后端把编排信息包在 `stack` 里（`{stack, content}`、`{stack, sync_ok}`），
 *    页面不该每处都记得多剥一层。统一在这里拆平。
 * 2. **动作名映射**：前端用 `start/recreate/delete` 这类直白语义，
 *    后端只认 `up/rebuild/remove`。翻译只在这一处，写错就是运行时「不支持的 action」。
 *
 * 所有路径参数都经 encodeURIComponent：
 * 编排名可能含 `/`、`#` 等字符（用户自定义 compose 项目名），不编码会拼出坏 URL。
 */

import { httpDelete, httpGet, httpPost, httpPut } from './request'
import type {
  AutoAdoptStatus,
  CreateStackRequest,
  CreateStackResponse,
  DeleteStackResponse,
  ExternalStack,
  ImportPreviewRequest,
  ImportPreviewResponse,
  ImportStackRequest,
  ImportStackResponse,
  StackAction,
  StackActionRequest,
  StackActionResponse,
  StackConfig,
  StackDetail,
  StackListResponse,
  StackLogsResponse,
  StackPsResponse,
  StackSummary,
  UpdateStackRequest,
  UpdateStackResponse
} from '@/types/stack'

/**
 * 前端动作名 → 后端 action 名。
 *
 * 后端 switch 只接受 up/down/stop/restart/pull/rebuild/remove/backup
 * （见 handlers_compose.go 的 handleStackAction）。
 * 之前前端直接发 start/recreate/delete，全部落到 default 分支报
 * 「不支持的 action」——这是拿自造 mock 当契约的典型后果。
 *
 * 注意 stop 与 down 的区别：`stop` 只停容器，`down` 会移除容器与网络。
 * 保持 stop 用 `stop`，语义最贴合「停止」且破坏性最小。
 */
const BACKEND_ACTION: Record<StackAction, string> = {
  start: 'up',
  stop: 'stop',
  restart: 'restart',
  pull: 'pull',
  recreate: 'rebuild',
  backup: 'backup',
  delete: 'remove'
}

/** 拼出 /agents/{id}/stacks 基础路径，避免每个函数重复编码逻辑 */
function stacksBase(agentId: string): string {
  return `/agents/${encodeURIComponent(agentId)}/stacks`
}

/** 拼出单个编排的路径 */
function stackPath(agentId: string, name: string): string {
  return `${stacksBase(agentId)}/${encodeURIComponent(name)}`
}

/** 后端对详情/写入类接口的包装：编排信息在 `stack` 下 */
interface StackEnvelope {
  stack: StackSummary
  sync_ok?: boolean
  sync_hint?: string
}

/**
 * 兜底一个 StackSummary。
 *
 * ## 「未知」和「0」必须分开
 *
 * `running`/`total`/`services` 缺失时**不能填 0 或空数组**：
 * 0 是真实结论（「一个都没在跑」），空数组是真实结论（「没有服务」）。
 * 字段缺失只是「不知道」。把未知伪装成 0，用户会以为服务全挂了并去排查，
 * 而实际情况可能只是 Agent 离线。
 *
 * 因此这三个字段用 `null` 表示未知，界面据此显示「—」。
 * 其余字段（name/status/hash/version/时间）给空值或 0 是安全的，
 * 因为它们不参与「有没有在跑」这类事实判断。
 */
function normalizeStack(raw: StackSummary | undefined | null): StackSummary {
  const s = raw ?? ({} as StackSummary)
  return {
    ...s,
    name: typeof s.name === 'string' ? s.name : '',
    status: typeof s.status === 'string' ? s.status : 'unknown',
    config_hash: typeof s.config_hash === 'string' ? s.config_hash : '',
    remote_hash: typeof s.remote_hash === 'string' ? s.remote_hash : '',
    // 未知 → null，不伪造「0 个服务」
    services: Array.isArray(s.services) ? s.services : null,
    // 未知 → null，不伪造「0 个在跑」
    running: typeof s.running === 'number' ? s.running : null,
    total: typeof s.total === 'number' ? s.total : null,
    drift: s.drift === true,
    agent_id: typeof s.agent_id === 'string' ? s.agent_id : '',
    version: typeof s.version === 'number' ? s.version : 0,
    updated_at: typeof s.updated_at === 'number' ? s.updated_at : 0,
    synced_at: typeof s.synced_at === 'number' ? s.synced_at : 0,
    /*
     * 未同步原因（仅自动接管失败时由 Hub 带上）。
     * 缺失就是没有——**不编一个默认值**：Hub 重启后内存里的原因会丢，
     * 前端此时要退回通用文案，而不是把缺字段伪装成「有原因」。
     */
    ...(typeof s.sync_hint === 'string' && s.sync_hint !== '' ? { sync_hint: s.sync_hint } : {})
  }
}

/**
 * 合法的自动接管状态集合。
 *
 * 后端将来新增状态值时，前端**降级为 unknown 而不是原样透传**：
 * 原样透传会让 `autoAdoptStatusView` 拿不到映射，界面要么空白要么显示英文枚举，
 * 而 unknown 有明确的「这台 Hub 没给信息」语义，界面据此完全不显示接管文案。
 */
const AUTO_ADOPT_STATUSES: readonly AutoAdoptStatus[] = ['pending', 'skipped', 'failed', 'unknown']

/** 把任意值收敛成合法状态；不认识的、缺失的一律 unknown */
function normalizeAutoAdoptStatus(value: unknown): AutoAdoptStatus {
  return typeof value === 'string' && (AUTO_ADOPT_STATUSES as readonly string[]).includes(value)
    ? (value as AutoAdoptStatus)
    : 'unknown'
}

/**
 * 兜底一个外部（未纳管）项目。
 *
 * 与 normalizeStack 同一原则：缺失就是未知（null），不是 0。
 * `managed` 后端恒定返回 false，缺失时也按 false（外部项目本就不受纳管）。
 */
function normalizeExternal(raw: ExternalStack): ExternalStack {
  const status = normalizeAutoAdoptStatus(raw.auto_adopt_status)
  const reason = typeof raw.auto_adopt_reason === 'string' ? raw.auto_adopt_reason : ''
  return {
    name: typeof raw.name === 'string' ? raw.name : '',
    status: typeof raw.status === 'string' ? raw.status : 'unknown',
    services: Array.isArray(raw.services) ? raw.services : null,
    running: typeof raw.running === 'number' ? raw.running : null,
    total: typeof raw.total === 'number' ? raw.total : null,
    managed: raw.managed === true,
    auto_adopt_status: status,
    /*
     * 旧 Hub（status 为 unknown）**不保留 reason**：
     * 契约里 reason 只在 skipped/failed 时有意义，unknown 下带着只会被误显示成
     * 「这台 Hub 说有原因」，反而误导。宁可丢掉也不推测。
     */
    auto_adopt_reason: status === 'unknown' ? '' : reason
  }
}

/**
 * 列出某台服务器上的全部编排。
 *
 * 注意：Agent 离线时本接口**仍返回 200**，各条 `status` 为 'offline'，
 * 且 `agent_online` 为 false。因此调用方不能靠"请求是否失败"判断在线状态，
 * 要看 `agent_online`。
 */
export async function listStacks(agentId: string): Promise<StackListResponse> {
  const res = await httpGet<StackListResponse>(stacksBase(agentId))
  return {
    ...res,
    stacks: Array.isArray(res.stacks) ? res.stacks.map(normalizeStack) : [],
    agent_online: res.agent_online === true,
    // 外部项目逐个兜底：字段缺失即未知，不退化成「0 个容器」
    external_stacks: Array.isArray(res.external_stacks)
      ? res.external_stacks.map(normalizeExternal)
      : []
  }
}

/**
 * 获取编排详情（状态、服务、版本号）。
 *
 * 后端返回 `{stack}` 一层包装，这里解成扁平的 StackDetail。
 * 详情**不含 compose 原文**（配置含 .env 属敏感内容，只走 config 接口），
 * 需要内容请用 getStackConfig。
 */
export async function getStack(agentId: string, name: string): Promise<StackDetail> {
  const res = await httpGet<StackEnvelope & { content?: string }>(stackPath(agentId, name))
  return {
    ...normalizeStack(res.stack),
    // 后端不再返回该字段；保留兜底以防旧响应，但没有就绝不能填成空串当内容用
    ...(typeof res.content === 'string' ? { content: res.content } : {})
  }
}

/**
 * 获取 compose.yml 与 .env 原文、版本号。
 *
 * 后端已改为 JSON 返回 `{content, env_content, version}`，且仅 manage 权限可读、
 * 响应带 `Cache-Control: no-store`（内容含 .env 里的密钥，不能被缓存）。
 *
 * 三个字段都做兜底：`.env` 可能为空串、也可能因后端版本较旧而缺失，
 * 直接 `.trim()` 会炸在 undefined 上。
 */
export async function getStackConfig(agentId: string, name: string): Promise<StackConfig> {
  const res = await httpGet<Partial<StackConfig>>(`${stackPath(agentId, name)}/config`)
  return {
    content: typeof res.content === 'string' ? res.content : '',
    env_content: typeof res.env_content === 'string' ? res.env_content : '',
    version: typeof res.version === 'number' ? res.version : 0
  }
}

/**
 * 新建编排。
 *
 * ⚠️ 返回值必须检查 `sync_ok`：Agent 离线或下发失败时后端不报错，
 * 而是返回 200 + sync_ok:false + sync_hint（内容已存 Hub，尚未下发到服务器）。
 * 调用方要用警告提示展示 sync_hint，不能当成部署成功。
 */
export async function createStack(
  agentId: string,
  payload: CreateStackRequest
): Promise<CreateStackResponse> {
  const res = await httpPost<StackEnvelope>(stacksBase(agentId), payload)
  return {
    stack: normalizeStack(res.stack),
    sync_ok: res.sync_ok === true,
    sync_hint: res.sync_hint
  }
}

/**
 * 保存编排配置。
 *
 * 必须带 version（乐观锁）：后端发现版本不一致会返回冲突(409)，
 * 前端据此提示「内容已被他人修改，请刷新」，避免静默覆盖他人编辑。
 *
 * 同样要检查 `sync_ok`：保存成功不等于已下发到服务器。
 */
export async function updateStack(
  agentId: string,
  name: string,
  payload: UpdateStackRequest
): Promise<UpdateStackResponse> {
  const res = await httpPut<StackEnvelope>(stackPath(agentId, name), payload)
  return {
    stack: normalizeStack(res.stack),
    sync_ok: res.sync_ok === true,
    sync_hint: res.sync_hint
  }
}

/**
 * 执行编排操作（启动/停止/重启/更新镜像/重建/备份/删除）。
 *
 * 前端动作名在这里翻译成后端 action 名（见 BACKEND_ACTION）。
 * 统一走 actions 接口而非每个操作一个路径：后端只需按 action 分支，
 * 前端也只需维护一份请求体结构。
 */
export async function runStackAction(
  agentId: string,
  name: string,
  payload: StackActionRequest
): Promise<StackActionResponse> {
  const backendAction = BACKEND_ACTION[payload.action]
  return httpPost<StackActionResponse>(`${stackPath(agentId, name)}/actions`, {
    ...payload,
    action: backendAction
  })
}

/**
 * 导入预检：把用户粘贴的 compose / .env 交给 Agent 校验。
 *
 * **只校验，不产生任何副作用**：不读宿主路径、不保存、不执行 up/down。
 *
 * 响应里的 `config_hash` 必须保存下来，确认导入时原样带回；
 * 后端会重新算一遍哈希比对，内容变了就要求重新预检——
 * 防止「预检通过的是 A，导入的却是 B」。
 */
export async function previewImport(
  agentId: string,
  payload: ImportPreviewRequest
): Promise<ImportPreviewResponse> {
  const res = await httpPost<Partial<ImportPreviewResponse>>(
    `${stacksBase(agentId)}/import-preview`,
    payload
  )
  return {
    name: typeof res.name === 'string' ? res.name : payload.name,
    config_hash: typeof res.config_hash === 'string' ? res.config_hash : '',
    // 拿不到哈希就无法保证导入的是同一份内容，一律视为不可导入，宁可挡下
    can_import: res.can_import === true && typeof res.config_hash === 'string' && res.config_hash !== '',
    blockers: Array.isArray(res.blockers) ? res.blockers : [],
    warnings: Array.isArray(res.warnings) ? res.warnings : []
  }
}

/**
 * 确认导入（纳管外部项目）。
 *
 * 语义边界（很重要，界面上必须说清）：
 * - **只把配置复制到 Arkade 管理区**，绝不自动 up / down / rebuild；
 * - 因此导入成功后容器状态不变，要启动需用户另行确认；
 * - 若 Agent 接管失败，配置已存 Hub（`sync_ok: false`），提示用户不要急着启动。
 */
export async function importStack(
  agentId: string,
  payload: ImportStackRequest
): Promise<ImportStackResponse> {
  const res = await httpPost<StackEnvelope & { adopted?: boolean }>(
    `${stacksBase(agentId)}/import`,
    payload
  )
  return {
    stack: normalizeStack(res.stack),
    sync_ok: res.sync_ok === true,
    sync_hint: res.sync_hint,
    adopted: res.adopted === true
  }
}

/**
 * 删除编排。
 *
 * ⚠️ 后端返回 **200 + JSON**，不是 204（见 handleDeleteStack）。
 * 执行失败时后端报错并**保留管理记录**，不会返回 200。
 *
 * @param removeVolumes 是否连带删除数据卷。**默认 false**——
 *        删卷是不可恢复的数据丢失，必须由用户在确认框里显式勾选。
 *        后端已改为按这个参数执行，不再强制删卷；这里仍始终显式传值，
 *        不依赖后端默认值——两边都不擅自替用户决定。
 */
export async function deleteStack(
  agentId: string,
  name: string,
  removeVolumes = false
): Promise<DeleteStackResponse> {
  // 始终显式传：让请求本身就能看出用户的意图，不靠后端默认值兜底
  const query = `?remove_volumes=${removeVolumes ? 'true' : 'false'}`
  return httpDelete<DeleteStackResponse>(`${stackPath(agentId, name)}${query}`)
}

/** 查看编排内容器（ps） */
export async function listStackPs(agentId: string, name: string): Promise<StackPsResponse> {
  return httpGet<StackPsResponse>(`${stackPath(agentId, name)}/ps`)
}

/**
 * 查看编排日志。
 *
 * @param service 只查看某个服务；不传表示全部
 * @param tail    返回末尾多少行
 */
export async function fetchStackLogs(
  agentId: string,
  name: string,
  service?: string,
  tail = 200
): Promise<StackLogsResponse> {
  const params = new URLSearchParams({ tail: String(tail) })
  if (service !== undefined && service !== '') {
    params.set('service', service)
  }
  return httpGet<StackLogsResponse>(`${stackPath(agentId, name)}/logs?${params.toString()}`)
}
