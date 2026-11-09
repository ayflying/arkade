import type { LoginPayload, User, UserEnvelope } from '@/types/auth'
import { ApiError } from '@/types/api'
import { LOGIN_FAILED_MESSAGE } from './errorMessages'
import { httpGet, httpPost } from './request'

/**
 * 认证接口
 *
 * 对应契约：
 * - POST /api/auth/login  200 { user } / 401 { error }
 * - POST /api/auth/logout 204
 * - GET  /api/auth/me     200 { user } / 401
 */

/**
 * 登录。
 *
 * 特殊处理：登录接口的 401 语义是「账号密码错误」，而非「登录已失效」，
 * 所以这里把 401 的文案改写为 LOGIN_FAILED_MESSAGE，避免给用户错误暗示。
 */
export async function login(payload: LoginPayload): Promise<User> {
  try {
    const res = await httpPost<UserEnvelope>('/auth/login', payload)
    return res.user
  } catch (err) {
    if (err instanceof ApiError && err.status === 401) {
      throw new ApiError(401, 'unauthorized', LOGIN_FAILED_MESSAGE)
    }
    throw err
  }
}

/** 退出登录；后端返回 204 无响应体 */
export async function logout(): Promise<void> {
  await httpPost<void>('/auth/logout')
}

/** 获取当前登录用户；未登录会抛 status=401 的 ApiError，供路由守卫判断 */
export async function fetchMe(): Promise<User> {
  const res = await httpGet<UserEnvelope>('/auth/me')
  return res.user
}
