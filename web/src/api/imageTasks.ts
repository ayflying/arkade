/**
 * 镜像任务接口（pull / export / import / migrate）
 *
 * ## 为什么单独开一个文件、不动 request.ts
 *
 * `request.ts` 是纯 JSON 封装：强制 `Accept: application/json`，
 * 有 body 就强制 `Content-Type: application/json`，且非 JSON 响应直接报错。
 * 镜像导入要发**原始二进制**（`application/x-tar`）并需要上传进度，
 * 这跟 JSON 通道的假设正好冲突。
 *
 * 如果为了导入去改 request.ts，就得给每个调用点加分支判断——
 * 迟早会出现「某个 JSON 接口被误当成二进制」。所以这里**平行**提供：
 * - JSON 类（pull/export/migrate/任务查询/取消）仍走 request.ts 的 httpGet/httpPost；
 * - 二进制上传走本文件自己的 XHR 实现。
 * 两条通道互不影响，request.ts 一个字符都不用改。
 */

import { httpGet, httpPost } from './request'
import type {
  ExportImageRequest,
  ImageTask,
  ImageTaskAcceptedResponse,
  ImageTaskKind,
  ImageTaskListResponse,
  ImageTaskResponse,
  ImageTaskStatus,
  MigrateImageRequest,
  PullImageRequest
} from '@/types/imageTask'

/** 与 request.ts 保持同一前缀，开发期由 Vite 代理 */
const API_BASE = '/api'

/**
 * 上传大小上限：2 GiB。
 *
 * 前端先拦一道，是为了在**选择文件之后立刻**告诉用户「太大了」，
 * 而不是让他上传几分钟后才被后端拒。
 * 真正的限制仍以后端为准（后端同样会拒）。
 */
export const IMPORT_MAX_BYTES = 2 * 1024 * 1024 * 1024

/** 拼出某台服务器上的镜像操作路径 */
function imagesBase(agentId: string): string {
  return `/agents/${encodeURIComponent(agentId)}/images`
}

/** 拼出单个任务的路径 */
function taskPath(taskId: string): string {
  return `/image-tasks/${encodeURIComponent(taskId)}`
}

/**
 * 拉取镜像到某台服务器。
 *
 * 返回 202 + task：表示**已接受并开始执行**，不是已完成。
 * 调用方必须用返回的 task.id 继续轮询。
 */
export async function pullImage(
  agentId: string,
  payload: PullImageRequest
): Promise<ImageTaskAcceptedResponse> {
  return httpPost<ImageTaskAcceptedResponse>(`${imagesBase(agentId)}/pull`, payload)
}

/**
 * 导出镜像为 tar，供浏览器下载。
 *
 * 导出完成后 task.download_ready 为 true，
 * 下载地址是 `GET /api/image-tasks/{id}/download`——
 * 走登录 Cookie 的普通链接，**不是预签名 URL**，
 * 所以下载时会重新校验权限；链接 30 分钟后过期。
 */
export async function exportImage(
  agentId: string,
  payload: ExportImageRequest
): Promise<ImageTaskAcceptedResponse> {
  return httpPost<ImageTaskAcceptedResponse>(`${imagesBase(agentId)}/export`, payload)
}

/**
 * 跨服务器迁移镜像。
 *
 * 语义边界（必须在 UI 上讲清）：
 * - **不启动容器、不删源**；
 * - 目标上若有同名 tag 可能被更新。
 */
export async function migrateImage(
  agentId: string,
  payload: MigrateImageRequest
): Promise<ImageTaskAcceptedResponse> {
  return httpPost<ImageTaskAcceptedResponse>(`${imagesBase(agentId)}/migrate`, payload)
}

/** 列出某台服务器的镜像任务（Hub 内存中，重启不保留） */
export async function listImageTasks(agentId: string): Promise<ImageTask[]> {
  const res = await httpGet<ImageTaskListResponse>(
    `/image-tasks?agent_id=${encodeURIComponent(agentId)}`
  )
  return Array.isArray(res.tasks) ? res.tasks.map(normalizeTask) : []
}

/** 查询单个任务 */
export async function getImageTask(taskId: string): Promise<ImageTask> {
  const res = await httpGet<ImageTaskResponse>(taskPath(taskId))
  return normalizeTask(res.task)
}

/**
 * 取消任务。
 *
 * ⚠️ 取消**不回滚**引擎已完成的写入：
 * 已下载的层会留下，已导入的部分不会撤回，migrate 可能已更新目标上的同名 tag。
 * 因此界面上要说「取消」而不是「撤销」。
 */
export async function cancelImageTask(taskId: string): Promise<ImageTask> {
  const res = await httpPost<ImageTaskResponse>(`${taskPath(taskId)}/cancel`)
  return normalizeTask(res.task)
}

/**
 * 导出文件的下载地址。
 *
 * 走浏览器原生下载（`<a href>` 或 `window.location`），
 * 由浏览器带上 Cookie 并写入磁盘——**不经过 fetch，不读成 blob**。
 * 镜像 tar 动辄几百 MB，读成 blob 会整份占住页面内存，
 * 大文件下直接卡死标签页。
 */
export function imageDownloadUrl(taskId: string): string {
  return `${API_BASE}${taskPath(taskId)}/download`
}

/**
 * 兜底一个任务对象。
 *
 * `progress` / `bytes_total` 的 `null` 是**有意义的值**，表示「未知」，
 * 这里绝不能把它们兜成 0——那等于把「不知道」伪装成「0%」。
 * 其余字段给安全默认值，避免界面上出现 undefined。
 *
 * `kind` / `status` 缺失时也要给合法值：
 * 界面按这两个字段查中文映射表，undefined 会渲染成空白，
 * 用户看到一行没有任何状态文字的记录，比显示「未知」更糟。
 * status 兜底成 `failed` 而不是 `pending`：未知状态不该表现得像还在跑，
 * 否则用户会一直等一个永远不会结束的任务。
 */
function normalizeTask(raw: ImageTask | undefined | null): ImageTask {
  const t = raw ?? ({} as ImageTask)
  return {
    id: typeof t.id === 'string' ? t.id : '',
    kind: isTaskKind(t.kind) ? t.kind : 'pull',
    agent_id: typeof t.agent_id === 'string' ? t.agent_id : '',
    ...(typeof t.target_agent_id === 'string' ? { target_agent_id: t.target_agent_id } : {}),
    status: isTaskStatus(t.status) ? t.status : 'failed',
    phase: typeof t.phase === 'string' ? t.phase : '',
    message: typeof t.message === 'string' ? t.message : '',
    // 保持 null 语义：null = 不知道进度
    progress: typeof t.progress === 'number' && Number.isFinite(t.progress) ? t.progress : null,
    bytes_done: typeof t.bytes_done === 'number' ? t.bytes_done : 0,
    // 同样保持 null：null = 不知道总量
    bytes_total: typeof t.bytes_total === 'number' ? t.bytes_total : null,
    created_at: typeof t.created_at === 'string' ? t.created_at : '',
    ...(typeof t.finished_at === 'string' ? { finished_at: t.finished_at } : {}),
    download_ready: t.download_ready === true
  }
}

/** 运行时的 kind 合法性检查：后端升级/降级时字段可能缺失或变成没见过的值 */
function isTaskKind(value: unknown): value is ImageTaskKind {
  return value === 'pull' || value === 'export' || value === 'import' || value === 'migrate'
}

/** 运行时的 status 合法性检查 */
function isTaskStatus(value: unknown): value is ImageTaskStatus {
  return (
    value === 'pending' ||
    value === 'running' ||
    value === 'succeeded' ||
    value === 'failed' ||
    value === 'canceled'
  )
}

/**
 * 上传镜像 tar（原始二进制，非 multipart）。
 *
 * ## 为什么用 XHR 而不是 fetch
 *
 * 上传进度只有 XHR 的 `upload.onprogress` 能给。
 * fetch 至今没有可用的上传进度事件，用 fetch 就只能显示一个没有分母的转圈。
 *
 * ## 进度诚实性
 *
 * 这里的百分比来自**真实已发送字节 / 文件总大小**，是可信的。
 * 但要注意：上传完成不等于导入完成——上传到 100% 之后，
 * 服务端还要解包、写层，那一段只能靠任务轮询的 `phase` 说明，
 * 不能继续假装百分比在涨。
 *
 * ## 超时
 *
 * 2 GiB 上传在慢网络下可能很久，所以不设请求超时；
 * 由用户主动取消（AbortSignal）兜底。
 *
 * @param signal 用于取消上传（用户关弹窗/点取消时 abort）
 * @param onProgress 已上传字节 / 总字节（来自 XHR，非估算）
 */
export function importImage(
  agentId: string,
  file: File,
  options: {
    signal?: AbortSignal
    onProgress?: (loaded: number, total: number) => void
  } = {}
): Promise<ImageTaskAcceptedResponse> {
  return new Promise<ImageTaskAcceptedResponse>((resolve, reject) => {
    const xhr = new XMLHttpRequest()
    xhr.open('POST', `${API_BASE}${imagesBase(agentId)}/import`, true)
    // 原始 tar 流：不是 multipart，所以不能用 FormData
    xhr.setRequestHeader('Content-Type', 'application/x-tar')
    xhr.setRequestHeader('Accept', 'application/json')
    /*
     * 明确告知归档的**确切字节数**。
     *
     * 为什么不能只靠 Content-Length：浏览器发大文件时常用 chunked 编码，
     * 此时请求里没有 Content-Length，Hub 就不知道要收多少——
     * 既没法做「收超了立刻中断」的保护，也没法在 chunked fallback 路径
     * 上判断是否收完。这里显式给出 File.size，是浏览器本地已知的精确值，
     * 不涉及任何计算或猜测。
     *
     * 注意这个头只是**声明**，Hub 仍须自行校验实际收到字节数是否相符——
     * 前端声明可以被绕过，不能作为唯一信任来源。
     */
    xhr.setRequestHeader('X-Archive-Size', String(file.size))
    // Cookie 会话认证：同源请求默认带，显式写出来避免将来被改成跨域时静默失效
    xhr.withCredentials = true

    const cleanup = (): void => {
      options.signal?.removeEventListener('abort', handleAbort)
    }

    function handleAbort(): void {
      xhr.abort()
    }

    if (options.signal !== undefined) {
      if (options.signal.aborted) {
        reject(new DOMException('上传已取消', 'AbortError'))
        return
      }
      options.signal.addEventListener('abort', handleAbort)
    }

    if (options.onProgress !== undefined) {
      const onProgress = options.onProgress
      xhr.upload.onprogress = (event: ProgressEvent): void => {
        // lengthComputable 为 false 时总长度未知，不编造分母
        if (!event.lengthComputable) return
        onProgress(event.loaded, event.total)
      }
    }

    xhr.onerror = (): void => {
      cleanup()
      reject(new Error('上传失败：无法连接服务器'))
    }

    xhr.onabort = (): void => {
      cleanup()
      reject(new DOMException('上传已取消', 'AbortError'))
    }

    xhr.onload = (): void => {
      cleanup()
      // 202 表示已接受并开始执行；其余状态按错误处理
      const text = xhr.responseText
      let parsed: unknown = null
      try {
        parsed = text === '' ? null : (JSON.parse(text) as unknown)
      } catch {
        parsed = null
      }

      if (xhr.status >= 200 && xhr.status < 300) {
        if (parsed === null) {
          reject(new Error('服务器返回了无法解析的数据'))
          return
        }
        resolve(parsed as ImageTaskAcceptedResponse)
        return
      }

      // 错误体沿用后端统一形状 { error: { code, message } }，取 message 展示
      const message = extractErrorMessage(parsed)
      reject(new Error(message ?? `上传失败（HTTP ${String(xhr.status)}）`))
    }

    xhr.send(file)
  })
}

/** 从后端错误体里取人类可读信息；形状不符就返回 null 让调用方用兜底文案 */
function extractErrorMessage(parsed: unknown): string | null {
  if (typeof parsed !== 'object' || parsed === null) return null
  const maybeError = (parsed as { error?: unknown }).error
  if (typeof maybeError !== 'object' || maybeError === null) return null
  const message = (maybeError as { message?: unknown }).message
  return typeof message === 'string' && message !== '' ? message : null
}
