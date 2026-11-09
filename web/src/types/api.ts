/**
 * 通用 API 类型（错误体、状态映射等）
 *
 * 后端错误体统一为：{ "error": { "code": "...", "message": "..." } }
 */

/** 后端错误码：契约内列出的集合（保留 string 兜底，后端新增错误码不至于类型报错） */
export type ApiErrorCode =
  | 'unauthorized'
  | 'forbidden'
  | 'not_found'
  | 'agent_offline'
  | 'timeout'
  | 'runtime_failed'
  | 'internal'
  | (string & {})

/** 后端错误体 */
export interface ApiErrorBody {
  error: {
    code: ApiErrorCode
    message: string
  }
}

/**
 * 请求层抛出的统一错误对象。
 *
 * 之所以不直接用 Error：页面需要区分「未登录(401)」「无权限(403)」「Agent 离线」
 * 等场景做不同提示，用字段比解析 message 字符串可靠。
 */
export class ApiError extends Error {
  /** HTTP 状态码；网络层失败（fetch reject）时为 0 */
  readonly status: number
  /** 后端错误码；网络层失败时为 'network'，响应体不合规时为 'unknown' */
  readonly code: ApiErrorCode
  /** 后端原始 message（英文/中文未知），用于排障，不直接展示给用户 */
  readonly rawMessage: string

  constructor(status: number, code: ApiErrorCode, rawMessage: string) {
    // Error.message 存已翻译的中文文案，便于兜底直接展示
    super(rawMessage)
    this.name = 'ApiError'
    this.status = status
    this.code = code
    this.rawMessage = rawMessage
  }
}
