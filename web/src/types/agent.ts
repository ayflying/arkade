/**
 * Agent（服务器）相关类型
 *
 * 对应后端契约 GET /api/agents：
 * {
 *   "agents": [{
 *     "id": "...", "name": "...", "status": "online|offline|upgrading",
 *     "version": "...", "runtime": "docker|podman|none", "runtime_version": "...",
 *     "last_seen": 1700000000, "labels": { "group": "测试" }
 *   }]
 * }
 *
 * 注意：labels 是 object（label 名 → 值），不是数组，因此用 Record<string, string>。
 */

/** Agent 连接状态 */
export type AgentStatus = 'online' | 'offline' | 'upgrading'

/** Agent 探测到的容器运行时 */
export type RuntimeKind = 'docker' | 'podman' | 'none'

export interface Agent {
  id: string
  name: string
  /** 后端可能出现契约外的状态值，用联合 + string 兜底展示，避免脏数据让页面崩 */
  status: AgentStatus | string
  version: string
  runtime: RuntimeKind | string
  runtime_version: string
  /** Unix 秒级时间戳（非毫秒） */
  last_seen: number
  labels: Record<string, string>
}

/** GET /api/agents 响应体 */
export interface AgentListResponse {
  agents: Agent[]
}
