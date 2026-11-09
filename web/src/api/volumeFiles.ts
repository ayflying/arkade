/**
 * 存储卷文件管理接口
 *
 * 单一 endpoint、按 `operation` 分派（理由见 types/volumeFile.ts）。
 *
 * ## 为什么用 httpPost 而不是新建通道
 *
 * 这里是纯 JSON：请求体是 `{volume, operation, path, ...}`，
 * 文件内容是 base64 字符串。`request.ts` 已有的 JSON 语义完全够用，
 * 不需要像镜像导入那样另开二进制通道。
 *
 * ## 敏感数据约束（api 层守住）
 *
 * - 本文件**不打印任何** console 日志：日志里出现文件内容就是把
 *   数据库密码写进了浏览器控制台；
 * - 不缓存、不持久化，读到的内容原样交给调用方，调用方必须在
 *   组件卸载/离开文件时清空。
 */

import { httpPost } from './request'
import type {
  VolumeEntry,
  VolumeFileRequest,
  VolumeFileResponse,
  VolumeFileResult
} from '@/types/volumeFile'

/** 拼出某台服务器上的卷文件接口路径 */
function volumeFilesPath(agentId: string): string {
  return `/agents/${encodeURIComponent(agentId)}/volume-files`
}

/**
 * 统一提交。
 *
 * 响应里缺 `result` 时抛错而不是返回空对象：
 * 空对象会让界面显示「空目录」，而真实情况是后端契约违约——
 * 让用户以为卷里没东西，比报错更糟。
 */
async function submit(
  agentId: string,
  payload: VolumeFileRequest
): Promise<VolumeFileResult> {
  const res = await httpPost<VolumeFileResponse>(volumeFilesPath(agentId), payload)
  if (typeof res !== 'object' || res === null || typeof res.result !== 'object' || res.result === null) {
    throw new Error('服务器返回了无法解析的数据')
  }
  return normalizeResult(res.result)
}

/** 兜底结果体：缺字段给安全默认值，避免界面出现 undefined */
function normalizeResult(raw: VolumeFileResult): VolumeFileResult {
  const out: VolumeFileResult = {
    path: typeof raw.path === 'string' ? raw.path : ''
  }
  if (Array.isArray(raw.entries)) {
    out.entries = raw.entries.map(normalizeEntry)
  }
  if (typeof raw.data_base64 === 'string') out.data_base64 = raw.data_base64
  if (typeof raw.sha256 === 'string') out.sha256 = raw.sha256
  if (typeof raw.size === 'number') out.size = raw.size
  if (typeof raw.message === 'string') out.message = raw.message
  return out
}

/** 兜底单个目录条目 */
function normalizeEntry(raw: VolumeEntry): VolumeEntry {
  const kind = raw.kind
  return {
    name: typeof raw.name === 'string' ? raw.name : '',
    // 未知类型统一降级为 unsupported：**不退化成 file**，
    // 否则界面会给一个读写不了的东西显示编辑按钮
    kind:
      kind === 'directory' || kind === 'file' || kind === 'symlink' || kind === 'unsupported'
        ? kind
        : 'unsupported',
    size: typeof raw.size === 'number' ? raw.size : 0,
    modified_at: typeof raw.modified_at === 'number' ? raw.modified_at : 0,
    mode: typeof raw.mode === 'string' ? raw.mode : ''
  }
}

/**
 * 列出目录。
 *
 * @param path 相对卷根的路径；根目录传空串（不要传 `/`，
 *             后端按相对路径处理，`/` 是否被接受由后端决定，前端统一用空串）
 */
export async function listVolumeFiles(
  agentId: string,
  volume: string,
  path: string
): Promise<VolumeFileResult> {
  return submit(agentId, { volume, operation: 'list', path })
}

/**
 * 读取文件（base64 内容 + sha256）。
 *
 * **必须保存返回的 sha256**：后续 write 要作为 expected_sha256 回传，
 * 否则后端无法判断「我读到之后有没有别人改过」。
 */
export async function readVolumeFile(
  agentId: string,
  volume: string,
  path: string
): Promise<VolumeFileResult> {
  return submit(agentId, { volume, operation: 'read', path })
}

/** 写入文件的参数 */
export interface WriteVolumeFileOptions {
  /** 目标相对路径 */
  path: string
  /** 内容（base64） */
  dataBase64: string
  /** 读到的原文件 sha256；覆盖已有文件时必传 */
  expectedSha256?: string
  /** true = 目标已存在则拒绝（新建文件用） */
  createOnly?: boolean
}

/**
 * 写入文件。
 *
 * 两种语义二选一：
 * - 新建文件：`createOnly: true`，目标存在会被后端拒绝；
 * - 覆盖已有：必须带 `expectedSha256`，后端比对不符则 409。
 *
 * 不提供「不带任何保护的盲覆盖」入口：这正是最容易丢数据的方式。
 */
export async function writeVolumeFile(
  agentId: string,
  volume: string,
  options: WriteVolumeFileOptions
): Promise<VolumeFileResult> {
  const payload: VolumeFileRequest = {
    volume,
    operation: 'write',
    path: options.path,
    data_base64: options.dataBase64
  }
  if (options.expectedSha256 !== undefined) payload.expected_sha256 = options.expectedSha256
  if (options.createOnly === true) payload.create_only = true
  return submit(agentId, payload)
}

/** 新建目录 */
export async function mkdirVolume(
  agentId: string,
  volume: string,
  path: string
): Promise<VolumeFileResult> {
  return submit(agentId, { volume, operation: 'mkdir', path })
}

/**
 * 重命名 / 移动。
 *
 * 后端**拒绝覆盖**已存在的目标（Lead 明确要求），
 * 所以前端不需要也不应该提供「覆盖」选项。
 */
export async function renameVolumeEntry(
  agentId: string,
  volume: string,
  path: string,
  destination: string
): Promise<VolumeFileResult> {
  return submit(agentId, { volume, operation: 'rename', path, destination })
}

/**
 * 删除。
 *
 * 只能删**单个文件**或**空目录**，不可恢复。
 * 非空目录后端会拒绝——这不是限制，而是防止一次误操作删掉整棵子树。
 */
export async function deleteVolumeEntry(
  agentId: string,
  volume: string,
  path: string
): Promise<VolumeFileResult> {
  return submit(agentId, { volume, operation: 'delete', path })
}
