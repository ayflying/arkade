/**
 * 容器相关类型
 *
 * 对应后端契约：
 * - GET  /api/agents/{id}/containers                     → { agent_id, containers: Container[] }
 * - POST /api/agents/{id}/containers/{cid}/start         → { container_id, state }
 * - POST /api/agents/{id}/containers/{cid}/stop          → { container_id, state }
 * - GET  /api/agents/{id}/containers/{cid}/logs?tail=200 → { lines: string[], truncated: boolean }
 */

/**
 * 容器来源（归属）标记，见 docs/01-architecture.md 4.2：
 * - arkade     Arkade 下发创建，可自动重建
 * - adopted    外部工具部署后被接管，编排只读
 * - discovered 被发现但未接管，仅展示与基础操作
 */
export type ContainerOrigin = 'arkade' | 'adopted' | 'discovered'

export interface Container {
  /** 短 ID 或长 ID，后端给哪个用哪个 */
  id: string
  name: string
  image: string
  /** 运行时状态字符串，如 running / exited / paused / created */
  state: string
  /** 人类可读状态，如 "Up 3 hours" */
  status: string
  origin: ContainerOrigin | string
  stack_id: string
  /** Unix 秒级时间戳 */
  created: number
  /** 端口映射文本，如 "0.0.0.0:8080->80/tcp"；可能为空字符串 */
  ports: string
}

/** GET /api/agents/{id}/containers 响应体 */
export interface ContainerListResponse {
  agent_id: string
  containers: Container[]
}

/** 启停容器接口的响应体 */
export interface ContainerActionResponse {
  container_id: string
  /** 操作后的新状态，如 running / exited */
  state: string
}

/** GET .../logs 响应体 */
export interface ContainerLogsResponse {
  lines: string[]
  /** 日志是否被截断（tail 限制导致） */
  truncated: boolean
}
