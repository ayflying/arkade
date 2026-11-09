import type { Agent, AgentListResponse } from '@/types/agent'
import { httpGet } from './request'

/**
 * 服务器（Agent）接口
 *
 * 对应契约：GET /api/agents → { agents: [...] }
 */

/** 拉取全部 Agent 列表（首期无分页，后端一次性返回） */
export async function listAgents(): Promise<Agent[]> {
  const res = await httpGet<AgentListResponse>('/agents')
  // 兜底：后端异常返回 null 时不让页面拿到 undefined 崩在 .map 上
  return Array.isArray(res.agents) ? res.agents : []
}
