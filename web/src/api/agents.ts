import type {
  Agent,
  AgentListResponse,
  AgentToggleResponse,
  DeleteAgentResponse,
  UpdateAgentRequest,
  UpdateAgentResponse
} from '@/types/agent'
import { httpDelete, httpGet, httpPatch, httpPost } from './request'

/**
 * 服务器（Agent）接口
 *
 * 对应契约：
 * - GET    /api/agents                    列表
 * - PATCH  /api/agents/{id}               编辑（显示名/备注/标签）
 * - DELETE /api/agents/{id}               移除纳管关系（**不删服务器上的容器**）
 * - POST   /api/agents/{id}/disable       禁用
 * - POST   /api/agents/{id}/enable        启用
 *
 * 注意：没有「新增服务器」接口——Agent 是主动向外连接 Hub 的，
 * Hub 无法反向添加。详见「添加服务器」引导说明。
 */

/** 拼出单个 Agent 的路径，统一 URL 编码避免 id 中的特殊字符拼坏路径 */
function agentPath(agentId: string): string {
  return `/agents/${encodeURIComponent(agentId)}`
}

/** 拉取全部 Agent 列表（首期无分页，后端一次性返回） */
export async function listAgents(): Promise<Agent[]> {
  const res = await httpGet<AgentListResponse>('/agents')
  // 兜底：后端异常返回 null 时不让页面拿到 undefined 崩在 .map 上
  return Array.isArray(res.agents) ? res.agents : []
}

/**
 * 编辑服务器信息。
 *
 * 只提交改动的字段（PATCH 语义），避免把未改的字段一起回写造成覆盖。
 * 返回**解包后的 Agent 对象**（后端响应是 `{ agent: {...} }` 包装），
 * 调用方可以直接用它更新本地状态，省一次列表请求。
 */
export async function updateAgent(
  agentId: string,
  payload: UpdateAgentRequest
): Promise<Agent> {
  const res = await httpPatch<UpdateAgentResponse>(agentPath(agentId), payload)
  // 解包统一在这里做：调用方拿到的永远是 Agent，不用记得多剥一层
  return res.agent
}

/**
 * 移除服务器纳管关系。
 *
 * ⚠️ 语义是「从 Arkade 中移除」，**不会删除服务器上的任何容器**。
 * 确认框必须写清这一点，否则用户不敢点。
 *
 * 返回体里的 `message` 是后端写好的中文说明，直接展示；
 * `orphan_stacks` > 0 说明有编排配置仍留在 Hub（与编排列表的 orphans 方向相反），
 * 必须提示用户，且**不能说「可在编排列表中查看」**——删完服务器后界面上已无入口。
 *
 * 另注意：删除在线 Agent 后后端会断开其连接，Agent 会重新注册并再次出现。
 * 想阻止重新接入应使用 disable，确认框里必须讲清这一点。
 */
export async function deleteAgent(agentId: string): Promise<DeleteAgentResponse> {
  const res = await httpDelete<DeleteAgentResponse>(agentPath(agentId))
  // 兜底：后端异常时不让 undefined 崩在后续的 .orphan_stacks 读取上
  return res ?? { orphan_stacks: 0, message: '已移除该服务器的纳管关系' }
}

/**
 * 禁用服务器：禁用后其上的操作入口全部不可用。
 *
 * 返回解包后的 Agent（方便调用方就地更新那一行）。
 */
export async function disableAgent(agentId: string): Promise<Agent> {
  const res = await httpPost<AgentToggleResponse>(`${agentPath(agentId)}/disable`)
  return res.agent
}

/** 启用服务器：恢复可操作状态 */
export async function enableAgent(agentId: string): Promise<Agent> {
  const res = await httpPost<AgentToggleResponse>(`${agentPath(agentId)}/enable`)
  return res.agent
}
