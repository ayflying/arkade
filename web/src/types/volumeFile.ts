/**
 * 存储卷文件管理类型
 *
 * 契约来源：Lead 定稿的单 endpoint 设计。
 * `POST /api/agents/{id}/volume-files`，一个接口按 `operation` 分派。
 *
 * ## 为什么是单 endpoint 而不是 REST 多路径
 *
 * 卷内路径是**任意深度的相对路径**，放进 URL 路径段会被 `..`、`%2f`、
 * Windows 反斜杠、超长路径反复咬到。放进 JSON body 则由后端统一做
 * `filepath.Clean` + 越界校验，前端不必为编码规则猜两遍。
 *
 * ## 敏感数据
 *
 * 卷里通常是数据库文件、上传文件、密钥。因此：
 * - 所有接口要求该服务器的 **manage** 权限；
 * - 响应 `Cache-Control: no-store`，前端也**绝不**把内容写进
 *   localStorage / sessionStorage / Pinia 持久化 / URL / console。
 */

/**
 * 条目类型。
 *
 * `symlink` 与 `unsupported` 只展示，**不提供任何读写删除操作**——
 * 符号链接可能指向卷外（甚至宿主机任意位置），跟进去读等于绕过卷边界；
 * `unsupported`（设备文件、socket、FIFO 等）没有「文件内容」这个概念。
 */
export type VolumeEntryKind = 'directory' | 'file' | 'symlink' | 'unsupported'

/** 目录条目的运行时可判定性：这两个类型不可读写 */
export function isEntryReadable(kind: VolumeEntryKind): boolean {
  return kind === 'file'
}

/** 只有目录能进入 */
export function isEntryNavigable(kind: VolumeEntryKind): boolean {
  return kind === 'directory'
}

/** 目录项 */
export interface VolumeEntry {
  /** 条目名（不含路径），如 `app.log` */
  name: string
  kind: VolumeEntryKind
  /** 字节数；目录/未知时为 0，不要当真实大小展示 */
  size: number
  /** 修改时间（**Unix 秒**，与本项目其他接口一致） */
  modified_at: number
  /** 权限位，如 `0644` / `drwxr-xr-x` 形态由后端给，原样展示 */
  mode: string
}

/** 操作类型 */
export type VolumeFileOperation = 'list' | 'read' | 'write' | 'mkdir' | 'rename' | 'delete'

/**
 * 请求体。
 *
 * 各操作实际用到的字段：
 * - `list`：volume + path（目录相对路径，根目录为空串）
 * - `read`：volume + path（文件相对路径）
 * - `write`：volume + path + data_base64 +（可选）expected_sha256 / create_only
 * - `mkdir`：volume + path
 * - `rename`：volume + path + destination
 * - `delete`：volume + path
 */
export interface VolumeFileRequest {
  /** 卷名（不是挂载点路径） */
  volume: string
  operation: VolumeFileOperation
  /** 相对卷根的路径；根目录用空串 */
  path: string
  /** rename 的目标路径（相对卷根） */
  destination?: string
  /** write 的内容，UTF-8 文本的 base64 编码 */
  data_base64?: string
  /**
   * write 的乐观锁：写入前读到的 sha256。
   *
   * **写已有文件必须带**。不带就等于「不管别人有没有改过，我直接覆盖」，
   * 在多个管理员同时操作同一份配置时会静默吞掉对方的修改。
   * 后端据此返回 409，前端提示「文件已被他人修改，请重新读取」。
   */
  expected_sha256?: string
  /**
   * write 的「仅新建」标志。
   *
   * `true` 时目标已存在则拒绝（建文件用）。
   * 不带这个标志就是覆盖写，必须有 expected_sha256 兜底。
   */
  create_only?: boolean
}

/** 结果体 */
export interface VolumeFileResult {
  /** 本次操作的目标路径（后端 Clean 后的规范化结果，用于回填界面） */
  path: string
  /** 仅 list 有：目录条目 */
  entries?: VolumeEntry[]
  /** 仅 read 有：文件内容的 base64（UTF-8 文本） */
  data_base64?: string
  /**
   * 内容的 sha256。
   *
   * - `read` 返回：**必须留存**，作为后续 `write` 的 `expected_sha256`；
   * - `write` 返回：写入后新内容的哈希（可用于连续编辑不回读）。
   */
  sha256?: string
  /** 内容字节数（read/write） */
  size?: number
  /** 人类可读的结果说明（如「已创建目录」），可直接展示 */
  message?: string
}

export interface VolumeFileResponse {
  result: VolumeFileResult
}

/**
 * 单文件上限 4 MiB。
 *
 * 这是**契约硬限制**，不是前端随便定的：
 * - 内容走 base64 进 JSON，4 MiB 原文编码后约 5.4 MiB，已是单个 JSON
 *   响应能接受的量级；
 * - 更大说明这不是「文本配置」而是数据文件，应该走下载而不是在浏览器里编辑。
 */
export const VOLUME_FILE_MAX_BYTES = 4 * 1024 * 1024

/** 单目录条目上限 2000，超过后端会截断或报错，前端要能提示 */
export const VOLUME_DIR_MAX_ENTRIES = 2000
