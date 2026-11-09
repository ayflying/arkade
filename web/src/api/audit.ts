import type {
  AuditActionsResponse,
  AuditListResponse,
  AuditQuery
} from '@/types/audit'
import { httpGet } from './request'

/**
 * 操作审计接口
 *
 * 契约：
 *   GET /api/audit?limit=&offset=&user_id=&agent_id=&action=&result=&from=&to=
 *   GET /api/audit/actions → { actions: [{action, label}] }
 *
 * 分页用 limit/offset（不是 page/page_size）：
 * 页码到 offset 的换算集中在页面层，这里只做参数拼装。
 *
 * 走 GET + 查询参数而不是把筛选条件放进 body：
 * 审计查询是无副作用的读操作，用 GET 才能在刷新时保持条件。
 */

/**
 * 把查询条件拼成查询串。
 *
 * 空值不带上，避免后端把空串当成有效筛选条件
 * （例如 user_id='' 可能被当成「查 id 为空的记录」而返回空列表）。
 */
function buildAuditQuery(query: AuditQuery): string {
  const params = new URLSearchParams()

  // 分页
  if (query.limit !== undefined && query.limit > 0) params.set('limit', String(query.limit))
  if (query.offset !== undefined && query.offset > 0) params.set('offset', String(query.offset))

  // 筛选项：空串一律视为不筛选
  if (query.user_id !== undefined && query.user_id !== '') params.set('user_id', query.user_id)
  if (query.agent_id !== undefined && query.agent_id !== '') params.set('agent_id', query.agent_id)
  if (query.action !== undefined && query.action !== '') params.set('action', query.action)
  if (query.result !== undefined && query.result !== '') params.set('result', query.result)

  // 时间范围：统一传 Unix 秒（后端也接受 RFC3339，但秒没有时区歧义）
  if (query.from !== undefined && query.from > 0) params.set('from', String(query.from))
  if (query.to !== undefined && query.to > 0) params.set('to', String(query.to))

  const text = params.toString()
  return text === '' ? '' : `?${text}`
}

/** 拉取审计记录（分页 + 筛选） */
export function listAudit(query: AuditQuery = {}): Promise<AuditListResponse> {
  return httpGet<AuditListResponse>(`/audit${buildAuditQuery(query)}`)
}

/**
 * 拉取动作类型字典，用于筛选下拉。
 *
 * 用接口返回而不是在前端硬编码动作清单：
 * 动作由后端定义，前端硬编码一份必然随版本漂移
 * （后端加了新动作，前端筛选里选不到，用户会以为该动作没被记录）。
 */
export async function listAuditActions(): Promise<AuditActionsResponse> {
  return httpGet<AuditActionsResponse>('/audit/actions')
}
