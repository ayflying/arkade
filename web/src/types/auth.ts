/**
 * 认证相关类型
 *
 * 对应后端契约：
 * - POST /api/auth/login  → { user: User }
 * - GET  /api/auth/me     → { user: User }
 * - POST /api/auth/logout → 204
 */

/** 用户角色（后端首期只有 admin；保留普通用户以便权限分级扩展） */
export type UserRole = 'admin' | 'operator' | 'viewer'

/** 后端返回的当前登录用户；字段名与后端一致使用 snake_case */
export interface User {
  id: number
  username: string
  display_name: string
  /** 角色：后端可能返回契约外的值，前端按 string 收下并在权限判断处兜底 */
  role: UserRole | string
}

/** 登录/取当前用户接口的响应体 */
export interface UserEnvelope {
  user: User
}

/** 登录请求体 */
export interface LoginPayload {
  username: string
  password: string
}
