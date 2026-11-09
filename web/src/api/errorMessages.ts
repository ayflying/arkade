import type { ApiErrorBody } from '@/types/api'

/**
 * 后端错误码 → 中文文案映射
 *
 * 为什么单独成文件：错误码会在登录页、列表页、容器页多处展示，
 * 集中一处避免每个页面各写一套文案导致口径不一致；
 * 后端新增错误码时也只改这里。
 */
const ERROR_MESSAGE_MAP: Record<string, string> = {
  unauthorized: '登录已失效，请重新登录',
  forbidden: '没有权限执行该操作',
  not_found: '请求的资源不存在',
  agent_offline: '服务器已离线，无法执行该操作',
  timeout: '操作超时，服务器未在规定时间内响应',
  runtime_failed: '容器运行时操作失败',
  internal: '服务器内部错误'
}

/** 未登录专用文案：登录接口返回 401 时其实不是「登录失效」而是「账号密码错误」 */
export const LOGIN_FAILED_MESSAGE = '用户名或密码错误'

/**
 * 把后端错误码翻译为中文。
 *
 * 边界：
 * - 未收录的错误码：退回后端原始 message（若为空则给通用兜底），保证用户总能看到点东西；
 * - 注意后端 message 可能是英文，仅在未收录错误码时兜底使用。
 */
export function translateErrorCode(code: string, rawMessage: string): string {
  const mapped = ERROR_MESSAGE_MAP[code]
  if (mapped) return mapped
  if (rawMessage.trim() !== '') return rawMessage
  return '操作失败，请稍后重试'
}

/**
 * 从错误响应体中尽力提取 { error: { code, message } }。
 *
 * 边界：后端在 panic 或中间件故障时可能返回 HTML/空体，
 * 这里必须容错返回 null，绝不因为解析失败再抛一次异常。
 */
export function parseErrorBody(body: unknown): ApiErrorBody['error'] | null {
  if (typeof body !== 'object' || body === null) return null
  const err = (body as { error?: unknown }).error
  if (typeof err !== 'object' || err === null) return null
  const code = (err as { code?: unknown }).code
  const message = (err as { message?: unknown }).message
  return {
    code: typeof code === 'string' ? code : 'unknown',
    message: typeof message === 'string' ? message : ''
  }
}
