/**
 * 外部（未纳管）compose 项目接口
 *
 * 与 api/stacks.ts 刻意分开：这些接口打的是 `/external-stacks/`，
 * 只读服务器上现有文件，写操作只落到 **Hub 草稿**，从不改宿主机文件。
 *
 * 权限分层（很重要，页面要据此处理 403）：
 * - 详情 / 容器 / 日志 → 需要 **view**
 * - 配置读取 / 草稿 / 预检 / 登记 / 复制 → 需要 **manage**（配置可能含 secret）
 *
 * 因此「能进详情页但读不到配置」是**正常状态**，不是 bug。
 */
import { httpGet, httpPost, httpPut } from './request'
import type { Container } from '@/types/container'
import type { ExternalStack } from '@/types/stack'
import type {
  ApplyPreviewRequest,
  ApplyPreviewResponse,
  ApplyRequest,
  ApplyResponse,
  CopyConfigRequest,
  CopyConfigResponse,
  ExternalStackConfigResponse,
  ExternalStackDetailResponse,
  SaveDraftRequest,
  SaveDraftResponse
} from '@/types/externalStack'

/** 外部项目的基础路径 */
function externalBase(agentId: string): string {
  return `/agents/${encodeURIComponent(agentId)}/external-stacks`
}

/** 单个外部项目的路径 */
function externalPath(agentId: string, name: string): string {
  return `${externalBase(agentId)}/${encodeURIComponent(name)}`
}

/** 归一化项目：字段缺失即未知，不伪造 0 */
function normalizeProject(raw: ExternalStack): ExternalStack {
  return {
    name: typeof raw.name === 'string' ? raw.name : '',
    status: typeof raw.status === 'string' ? raw.status : 'unknown',
    services: Array.isArray(raw.services) ? raw.services : null,
    running: typeof raw.running === 'number' ? raw.running : null,
    total: typeof raw.total === 'number' ? raw.total : null,
    // 外部项目一律不受纳管；后端显式给 false，缺失也按 false
    managed: raw.managed === true,
    ...(typeof raw.auto_adopt_status === 'string'
      ? { auto_adopt_status: raw.auto_adopt_status }
      : {}),
    ...(typeof raw.auto_adopt_reason === 'string' ? { auto_adopt_reason: raw.auto_adopt_reason } : {})
  }
}

/** 兜底一个容器：缺字段用安全默认值，不臆造状态 */
function normalizeContainer(raw: Container): Container {
  return {
    id: typeof raw.id === 'string' ? raw.id : '',
    name: typeof raw.name === 'string' ? raw.name : '',
    image: typeof raw.image === 'string' ? raw.image : '',
    state: typeof raw.state === 'string' ? raw.state : 'unknown',
    status: typeof raw.status === 'string' ? raw.status : '',
    origin: typeof raw.origin === 'string' ? raw.origin : '',
    stack_id: typeof raw.stack_id === 'string' ? raw.stack_id : '',
    created: typeof raw.created === 'number' ? raw.created : 0,
    ports: typeof raw.ports === 'string' ? raw.ports : ''
  }
}

/**
 * 外部项目详情（含容器）。
 *
 * 容器已由 Hub 按 StackID 过滤，前端不再自己筛。
 */
export async function getExternalStack(
  agentId: string,
  name: string
): Promise<ExternalStackDetailResponse> {
  const res = await httpGet<ExternalStackDetailResponse>(externalPath(agentId, name))
  return {
    project: normalizeProject(res.project),
    containers: Array.isArray(res.containers) ? res.containers.map(normalizeContainer) : []
  }
}

/**
 * 读取外部项目配置。
 *
 * 需要 **manage**。无权限时后端返回 403，由调用方展示警告——
 * 不要把这个错误当成「配置不存在」，两者对用户的处理方式完全不同。
 */
export async function getExternalStackConfig(
  agentId: string,
  name: string
): Promise<ExternalStackConfigResponse> {
  const res = await httpGet<ExternalStackConfigResponse>(`${externalPath(agentId, name)}/config`)
  return {
    readable: res.readable === true,
    ...(typeof res.reason === 'string' && res.reason !== '' ? { reason: res.reason } : {}),
    ...(typeof res.content === 'string' ? { content: res.content } : {}),
    ...(typeof res.config_hash === 'string' && res.config_hash !== ''
      ? { config_hash: res.config_hash }
      : {}),
    blockers: Array.isArray(res.blockers) ? res.blockers : [],
    ...(res.draft !== undefined ? { draft: res.draft } : {})
  }
}

/**
 * 保存草稿到 Hub。
 *
 * 只写 Hub，**绝不改服务器上的文件**——所以用户可以放心编辑，
 * 不保存也不会碰到正在运行的业务。
 *
 * 首次保存 `expectedVersion` 传 0；之后必须带当前版本，
 * 版本不匹配时后端返回 409（说明别处已经改过草稿）。
 */
export async function saveExternalDraft(
  agentId: string,
  name: string,
  payload: SaveDraftRequest
): Promise<SaveDraftResponse> {
  return httpPut<SaveDraftResponse>(`${externalPath(agentId, name)}/draft`, payload)
}

/** 应用预检：只检查不执行，返回能否登进 Arkade 以及阻塞原因 */
export async function previewExternalApply(
  agentId: string,
  name: string,
  payload: ApplyPreviewRequest
): Promise<ApplyPreviewResponse> {
  const res = await httpPost<ApplyPreviewResponse>(
    `${externalPath(agentId, name)}/apply-preview`,
    payload
  )
  return {
    can_import: res.can_import === true,
    config_hash: typeof res.config_hash === 'string' ? res.config_hash : '',
    blockers: Array.isArray(res.blockers) ? res.blockers : [],
    warnings: Array.isArray(res.warnings) ? res.warnings : []
  }
}

/**
 * 登记到 Arkade：走现有导入逻辑，把这份配置收进 Hub 管理。
 *
 * **只登记、不启动**。界面上也因此叫「登记到 Arkade」而不是「应用」——
 * 后者会让用户以为运行状态会变化。
 */
export async function applyExternalStack(
  agentId: string,
  name: string,
  payload: ApplyRequest
): Promise<ApplyResponse> {
  const res = await httpPost<ApplyResponse>(`${externalPath(agentId, name)}/apply`, payload)
  return {
    stack: res.stack,
    sync_ok: res.sync_ok === true,
    ...(typeof res.sync_hint === 'string' ? { sync_hint: res.sync_hint } : {}),
    adopted: res.adopted === true
  }
}

/**
 * 把配置复制到另一台服务器。
 *
 * **不是完整迁移**：只复制 compose 配置本身，
 * 不含 `.env`、build 上下文、数据卷、镜像，且不在目标上启动。
 * 结果里后端显式回传 scope/data_included/started/source_preserved，
 * 前端据此如实展示，不自行推断。
 */
export async function copyExternalConfig(
  agentId: string,
  name: string,
  payload: CopyConfigRequest
): Promise<CopyConfigResponse> {
  const res = await httpPost<CopyConfigResponse>(`${externalPath(agentId, name)}/copy`, payload)
  return {
    stack: res.stack,
    scope: Array.isArray(res.scope) ? res.scope : ['config'],
    data_included: res.data_included === true,
    started: res.started === true,
    source_preserved: res.source_preserved === true,
    ...(typeof res.message === 'string' && res.message !== '' ? { message: res.message } : {})
  }
}
