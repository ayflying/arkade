/**
 * 外部（未纳管）compose 项目详情相关类型
 *
 * 契约来源：Hub 的 `/api/agents/{id}/external-stacks/{name}` 系列接口。
 * 与已纳管编排（types/stack.ts）是**两套东西**：
 * - 已纳管：配置在 Hub，Agent 按 Hub 下发的内容运行；
 * - 外部：配置在服务器自己的目录里，Hub 只读，写操作只落在 **Hub 草稿**。
 *
 * 本文件只描述外部项目，不复用 StackSummary —— 两者的「有配置」含义不同。
 */
import type { Container } from './container'
import type { ExternalStack } from './stack'

/**
 * GET /external-stacks/{name} 响应。
 *
 * 容器由 Hub 按其 StackID 过滤好再返回，前端直接展示，不再二次筛选——
 * 前端自己筛等于在两边各实现一次归属规则，迟早不一致。
 */
export interface ExternalStackDetailResponse {
  project: ExternalStack
  containers: Container[]
}

/**
 * 外部项目配置响应。
 *
 * ⚠️ `config` 相关接口需要 **manage** 权限（内容可能含 secret），
 * 而详情/容器/日志只需要 **view**。因此页面必须能处理
 * 「看得到详情但读不到配置」这种正常状态，并把 403 原因讲清楚。
 */
export interface ExternalStackConfigResponse {
  /** 能否读到配置内容。false 时 `content` 为空，原因在 `reason` */
  readable: boolean
  /** 读不到时的人话原因（权限、文件不存在、非 compose 项目等） */
  reason?: string
  /** 原始 compose 内容。`readable: false` 时是空串 */
  content?: string
  /** 当前磁盘文件的哈希，用于应用/复制预检 */
  config_hash?: string
  /**
   * 应用预检的阻塞项（人话）。
   *
   * 注意：`build` 这类不支持的写法会让 `blockers` 非空，
   * 但**配置本身依然可以查看、可以存草稿**——
   * blockers 只影响「登记到 Arkade」这个动作。
   */
  blockers?: string[]
  /** 已存在的草稿（用户在 Hub 上存过但还没应用的内容） */
  draft?: ExternalStackDraft
}

/** Hub 侧保存的草稿 */
export interface ExternalStackDraft {
  content: string
  /** 草稿基于哪一版磁盘内容创建，用于检测磁盘已被改动 */
  source_hash: string
  /** CAS 版本号，从 0 开始；并发保存时后端返回 409 */
  version: number
  /** Unix 秒 */
  updated_at: number
}

/** PUT /external-stacks/{name}/draft 请求体 */
export interface SaveDraftRequest {
  content: string
  source_hash: string
  /** 首次保存传 0；之后必须与当前版本一致，否则 409 */
  expected_version: number
}

export interface SaveDraftResponse {
  draft: ExternalStackDraft
}

/** POST /external-stacks/{name}/apply-preview 请求体 */
export interface ApplyPreviewRequest {
  content: string
}

/** 应用预检结果。结构对齐已有导入预检，便于复用同一套判断逻辑 */
export interface ApplyPreviewResponse {
  can_import: boolean
  config_hash: string
  blockers: string[]
  warnings: string[]
}

/** POST /external-stacks/{name}/apply 请求体 */
export interface ApplyRequest {
  content: string
  /** 来自预检的 config_hash，确保登记的就是预检过的那份内容 */
  expected_content_hash: string
  /** 硬性要求：登记会写入 Hub，必须显式确认 */
  confirmation: true
}

export interface ApplyResponse {
  stack: unknown
  sync_ok: boolean
  sync_hint?: string
  adopted: boolean
}

/** POST /external-stacks/{name}/copy 请求体 */
export interface CopyConfigRequest {
  target_agent_id: string
  /** 目标服务器上的编排名 */
  target_name: string
  content: string
  confirmation: true
}

/**
 * 复制配置的结果。
 *
 * ⚠️ 这是**只复制 compose 配置**，不是完整迁移：
 * 不复制 `.env`、不复制 `build` 上下文、不复制数据卷、不复制镜像，
 * 且**不启动**任何容器，源配置保持不动。
 *
 * 这些字段由后端**显式给出**而不是让前端推断，
 * 因为前端绝不能承诺一件自己没做的事。
 */
export interface CopyConfigResponse {
  stack: unknown
  /** 本次实际复制的内容种类，目前恒为 `['config']` */
  scope: string[]
  /** 是否包含数据卷；恒为 false，显式给出以免前端误以为含数据 */
  data_included: boolean
  /** 是否在目标上启动了容器；恒为 false */
  started: boolean
  /** 源配置是否保留；恒为 true */
  source_preserved: boolean
  /** 后端给的中文说明 */
  message?: string
}
