/**
 * 认证相关类型
 *
 * 对应后端契约：
 * - POST /api/auth/login  → { user: User }
 * - GET  /api/auth/me     → { user: User }
 * - POST /api/auth/logout → 204
 */

/** 用户角色（后端只有 admin / user 两档，见 hub/internal/user/permission.go） */
export type UserRole = 'admin' | 'user'

/** 后端返回的当前登录用户；字段名与后端一致使用 snake_case */
export interface User {
  id: string
  must_change_password?: boolean
  local_password?: boolean
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
