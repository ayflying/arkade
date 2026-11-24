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
 * 被调用方主动中止时的错误码。
 *
 * 与 `network` 严格区分：`network` 是"真的连不上"，要报给用户；
 * 主动中止是用户自己放弃的（关面板、切页面），提示它纯属噪音，
 * 而且会覆盖掉面板上更有用的状态。
 */
export const ABORTED_CODE = 'aborted'

/** 构造"请求已取消"错误 */
function abortedError(): ApiError {
  return new ApiError(0, ABORTED_CODE, '请求已取消')
}

/** 判断错误是否为主动中止（调用方据此静默收尾，不展示错误） */
export function isAborted(err: unknown): boolean {
  return err instanceof ApiError && err.code === ABORTED_CODE
}

/**
 * 判断异常是否来自 AbortSignal。
 *
 * 不能只依赖 `instanceof DOMException`：不同浏览器/测试环境（Node 的
 * node:test 里没有 DOMException）抛出的类型不同，但都带 name === 'AbortError'。
 */
function isAbortSignalError(cause: unknown): boolean {
  if (cause instanceof DOMException) return cause.name === 'AbortError'
  const name = (cause as { name?: unknown } | null)?.name
  return name === 'AbortError'
}

function networkError(cause: unknown): ApiError {
  const detail = cause instanceof Error ? cause.message : String(cause)
  return new ApiError(0, 'network', `无法连接服务器：${detail}`)
}

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
    // fetch 只有在网络层失败时才 reject（DNS、连接被拒、被 abort 打断）
    if (isAbortSignalError(cause)) throw abortedError()
    throw networkError(cause)
  }

  // 204 / 205：无响应体，直接返回 undefined，绝不调 response.json()（会抛 SyntaxError）
  if (response.status === 204 || response.status === 205) {
    return undefined as T
  }

  let text: string
  try {
    text = await response.text()
  } catch (cause) {
    // 响应头已到但正文被中断：abort 优先按"取消"处理，不当成网络故障
    if (isAbortSignalError(cause)) throw abortedError()
    // 响应头可能已到，但 Hub 重启会截断响应体。保留已收到的错误状态。
    if (!response.ok) {
      throw new ApiError(response.status, `http_${response.status}`, translateErrorCode(`http_${response.status}`, ''))
    }
    throw networkError(cause)
  }
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
export function httpGet<T>(path: string, init: Pick<RequestInit, 'signal' | 'cache'> = {}): Promise<T> {
  return request<T>(path, { ...init, method: 'GET' })
}

/**
 * POST 请求；body 会被序列化为 JSON。
 *
 * `init.signal` 用于**主动放弃**：用户关掉面板、切到别的页面时 abort 掉请求，
 * 让浏览器立刻断开连接。这一步不是礼貌性清理——Hub 依赖"连接断开"判定调用方
 * 已放弃，才会通知 Agent 停止处理并释放资源（详见 api/volumeFiles.ts）。
 */
export function httpPost<T>(
  path: string,
  body?: unknown,
  init: Pick<RequestInit, 'signal'> = {}
): Promise<T> {
  return request<T>(path, {
    ...init,
    method: 'POST',
    body: body === undefined ? undefined : JSON.stringify(body)
  })
}

/** PUT 请求：用于「整体替换」语义，如保存编排配置、设置组成员 */
export function httpPut<T>(
  path: string,
  body?: unknown,
  init: Pick<RequestInit, 'signal'> = {}
): Promise<T> {
  return request<T>(path, {
    ...init,
    method: 'PUT',
    body: body === undefined ? undefined : JSON.stringify(body)
  })
}

/** PATCH 请求：用于「局部更新」语义，如只改用户显示名而不动其他字段 */
export function httpPatch<T>(
  path: string,
  body?: unknown,
  init: Pick<RequestInit, 'signal'> = {}
): Promise<T> {
  return request<T>(path, {
    ...init,
    method: 'PATCH',
    body: body === undefined ? undefined : JSON.stringify(body)
  })
}

/**
 * DELETE 请求。
 *
 * 返回类型默认 void：契约里多数删除类接口返回 204 无响应体，
 * request() 已在 204 分支返回 undefined，避免调用方误以为还能拿到被删除的对象。
 *
 * 但**部分删除接口是有响应体的**（例如 DELETE /api/agents/{id} 会返回
 * { orphan_stacks, message } 告诉调用方「顺带产生了什么后果」）。
 * 因此这里允许显式指定 T；传了 T 就说明调用方确认该接口有响应体。
 */
export function httpDelete<T = void>(
  path: string,
  init: Pick<RequestInit, 'signal'> = {}
): Promise<T> {
  return request<T>(path, { ...init, method: 'DELETE' })
}
