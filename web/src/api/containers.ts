import type {
  Container,
  ContainerActionResponse,
  ContainerListResponse,
  ContainerLogsResponse
} from '@/types/container'
import { httpGet, httpPost } from './request'

/**
 * 容器接口
 *
 * 对应契约（agent_id / container_id 都需 URL 编码，防止 ID 中出现特殊字符）：
 * - GET  /api/agents/{id}/containers
 * - POST /api/agents/{id}/containers/{cid}/start
 * - POST /api/agents/{id}/containers/{cid}/stop
 * - GET  /api/agents/{id}/containers/{cid}/logs?tail=N
 */

/** 某台 Agent 上的容器列表 */
export async function listContainers(agentId: string): Promise<Container[]> {
  const res = await httpGet<ContainerListResponse>(
    `/agents/${encodeURIComponent(agentId)}/containers`
  )
  return Array.isArray(res.containers) ? res.containers : []
}

/** 启动容器，返回操作后的新状态 */
export function startContainer(agentId: string, containerId: string): Promise<ContainerActionResponse> {
  return httpPost<ContainerActionResponse>(
    `/agents/${encodeURIComponent(agentId)}/containers/${encodeURIComponent(containerId)}/start`
  )
}

/** 停止容器，返回操作后的新状态（通常为 exited） */
export function stopContainer(agentId: string, containerId: string): Promise<ContainerActionResponse> {
  return httpPost<ContainerActionResponse>(
    `/agents/${encodeURIComponent(agentId)}/containers/${encodeURIComponent(containerId)}/stop`
  )
}

/** 拉取容器日志尾部若干行 */
export async function fetchContainerLogs(
  agentId: string,
  containerId: string,
  tail = 200
): Promise<ContainerLogsResponse> {
  const res = await httpGet<ContainerLogsResponse>(
    `/agents/${encodeURIComponent(agentId)}/containers/${encodeURIComponent(containerId)}/logs?tail=${tail}`
  )
  return {
    lines: Array.isArray(res.lines) ? res.lines : [],
    truncated: res.truncated === true
  }
}
