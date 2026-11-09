import { ApiError } from '@/types/api'
import { parseErrorBody, translateErrorCode } from './errorMessages'

/**
 * 统一的 HTTP 请求层（fetch 封装）
 *
 * 为什么不用 axios：本项目只需 GET/POST + JSON + Cookie，fetch 足够，
 * 少一个依赖也少一层封装。这里集中处理：
 * 1. 所有请求带 credentials: 'include'（HttpOnly session Cookie 必须显式带上）；
 * 2. 非 2xx 统一转成 ApiError，携带 status / code / 中文文案；
 * 3. 204 无内容响应不尝试解析 JSON；
 * 4. 网络层异常（断网、后端没起来）也包装成 ApiError，调用方只需 catch 一种错误。
 */

/** 与 Hub 约定的 API 前缀，开发期由 Vite 代理到 127.0.0.1:8080 */
const API_BASE = '/api'

/** 响应体未知类型：先按 unknown 收下，由调用方用契约类型断言 */
type JsonValue = unknown

/**
 * 发起请求并解析为 T。
 *
 * @param path  相对于 /api 的路径，例如 '/agents'；调用方负责 URL 编码
 * @param init  fetch 初始化参数（method/body 等）
 */
async function request<T>(path: string, init: RequestInit = {}): Promise<T> {
  const url = `${API_BASE}${path}`

  let response: Response
  try {
    response = await fetch(url, {
      // Cookie 认证：必须带凭证，否则 401
      credentials: 'include',
      headers: {
        Accept: 'application/json',
        ...(init.body !== undefined ? { 'Content-Type': 'application/json' } : {}),
        ...init.headers
      },
      ...init
    })
  } catch (cause) {
    // fetch 只有在网络层失败时才 reject（DNS、连接被拒、被打断）
    const detail = cause instanceof Error ? cause.message : String(cause)
    throw new ApiError(0, 'network', `无法连接服务器：${detail}`)
  }

  // 204 / 205：无响应体，直接返回 undefined，绝不调 response.json()（会抛 SyntaxError）
  if (response.status === 204 || response.status === 205) {
    return undefined as T
  }

  const text = await response.text()
  let parsed: JsonValue = null
  let parseFailed = false
  if (text !== '') {
    try {
      parsed = JSON.parse(text) as JsonValue
    } catch {
      parseFailed = true
    }
  }

  if (!response.ok) {
    const err = parseErrorBody(parsed)
    // 响应体不可解析时用 HTTP 状态兜底错误码
    const code = err?.code ?? `http_${response.status}`
    const rawMessage = err?.message ?? (parseFailed ? text.slice(0, 200) : '')
    throw new ApiError(response.status, code, translateErrorCode(code, rawMessage))
  }

  if (parseFailed) {
    // 2xx 但响应体非 JSON：属于后端契约违约，明确报错而不是返回垃圾数据
    throw new ApiError(response.status, 'invalid_response', '服务器返回了无法解析的数据')
  }

  return parsed as T
}

/** GET 请求 */
export function httpGet<T>(path: string): Promise<T> {
  return request<T>(path, { method: 'GET' })
}

/** POST 请求；body 会被序列化为 JSON */
export function httpPost<T>(path: string, body?: unknown): Promise<T> {
  return request<T>(path, {
    method: 'POST',
    body: body === undefined ? undefined : JSON.stringify(body)
  })
}

/** PUT 请求：用于「整体替换」语义，如保存编排配置、设置组成员 */
export function httpPut<T>(path: string, body?: unknown): Promise<T> {
  return request<T>(path, {
    method: 'PUT',
    body: body === undefined ? undefined : JSON.stringify(body)
  })
}

/** PATCH 请求：用于「局部更新」语义，如只改用户显示名而不动其他字段 */
export function httpPatch<T>(path: string, body?: unknown): Promise<T> {
  return request<T>(path, {
    method: 'PATCH',
    body: body === undefined ? undefined : JSON.stringify(body)
  })
}

/**
 * DELETE 请求。
 *
 * 注意：契约里删除类接口返回 204 无响应体，
 * request() 已在 204 分支返回 undefined，因此这里把返回类型定为 void，
 * 避免调用方误以为还能拿到被删除的对象。
 */
export function httpDelete(path: string): Promise<void> {
  return request<void>(path, { method: 'DELETE' })
}
