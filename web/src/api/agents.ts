import type {
  Agent,
  AgentListResponse,
  AgentToggleResponse,
  CreateAgentRequest,
  CreateAgentResponse,
  DeleteAgentResponse,
  ResetAgentTokenResponse,
  UpdateAgentRequest,
  UpdateAgentResponse
} from '@/types/agent'
import { httpDelete, httpGet, httpPatch, httpPost } from './request'

/**
 * 服务器（Agent）接口
 *
 * 对应契约：
 * - GET    /api/agents                     列表
 * - POST   /api/agents                     预创建（返回一次性专属密钥）
 * - PATCH  /api/agents/{id}                编辑（显示名/备注/标签）
 * - DELETE /api/agents/{id}                移除纳管关系（**不删服务器上的容器**）
 * - POST   /api/agents/{id}/disable        禁用
 * - POST   /api/agents/{id}/enable         启用
 * - POST   /api/agents/{id}/token/reset    重置专属密钥
 *
 * 关于「添加服务器」：Hub 仍然无法主动连进内网里的服务器，
 * Agent 必须是主动出去连 Hub 的那一方。所谓「添加」实际是
 * **预创建一条 pending 记录 + 发一把专属密钥**，用户拿密钥去那台机器上启动 Agent，
 * 连上后这条记录才从 pending 变成 online。因此创建成功不等于服务器已经接入。
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
 * 预创建一台服务器，并获得它的专属密钥。
 *
 * ## 安全约定（api 层就要守住）
 *
 * 返回值里的 `enrollment.token` 是明文密钥、且**只此一次**。
 * 本函数不做任何缓存/日志/持久化，原样交给调用方；
 * 调用方必须把它放在组件局部状态里、弹窗关闭即清空。
 * 这里也刻意不 console 打印任何请求或响应体——
 * 一旦有人在开发时顺手 log 一次，密钥就进了浏览器日志。
 *
 * 重名（409）由调用方捕获并用 `ApiError.status` 区分提示。
 */
export async function createAgent(payload: CreateAgentRequest): Promise<CreateAgentResponse> {
  return httpPost<CreateAgentResponse>('/agents', payload)
}

/**
 * 重置某台服务器的专属密钥。
 *
 * ⚠️ 旧密钥**立即失效**，且 Hub 会主动断开该 Agent 的现存连接：
 * 那台机器会一直连不上，直到用户把新密钥更新到它的部署配置里。
 * 因此页面必须先做二次确认，把这个后果写清楚再调用。
 */
export async function resetAgentToken(agentId: string): Promise<ResetAgentTokenResponse> {
  return httpPost<ResetAgentTokenResponse>(`${agentPath(agentId)}/token/reset`)
}

/**
 * 编辑服务器信息。
 *
 * 只提交改动的字段（PATCH 语义），避免把未改的字段一起回写造成覆盖。
 * 返回**解包后的 Agent 对象**（后端响应是 `{ agent: {...} }` 包装），
 * 调用方可以直接用它更新本地状态，省一次列表请求。
 *
 * `direct_address` 的格式由后端校验（只允许主机部分，IPv6 用方括号）。
 * 前端只做「去掉首尾空白」这类无争议处理，不做格式猜测——
 * 猜错会让用户以为已保存，实际被后端拒绝。
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
