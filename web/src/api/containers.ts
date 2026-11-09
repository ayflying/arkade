import type {
  Container,
  ContainerActionResponse,
  ContainerListResponse,
  ContainerLogsResponse
} from '@/types/container'
import { httpDelete, httpGet, httpPost } from './request'

/**
 * 容器接口
 *
 * 对应契约（agent_id / container_id 都需 URL 编码，防止 ID 中出现特殊字符）：
 * - GET    /api/agents/{id}/containers
 * - POST   /api/agents/{id}/containers/{cid}/start
 * - POST   /api/agents/{id}/containers/{cid}/stop
 * - POST   /api/agents/{id}/containers/{cid}/restart
 * - DELETE /api/agents/{id}/containers/{cid}?force=&remove_volumes=
 * - GET    /api/agents/{id}/containers/{cid}/logs?tail=N
 */

/** 拼出单个容器的路径 */
function containerPath(agentId: string, containerId: string): string {
  return `/agents/${encodeURIComponent(agentId)}/containers/${encodeURIComponent(containerId)}`
}

/** 某台 Agent 上的容器列表 */
export async function listContainers(agentId: string): Promise<Container[]> {
  const res = await httpGet<ContainerListResponse>(
    `/agents/${encodeURIComponent(agentId)}/containers`
  )
  return Array.isArray(res.containers) ? res.containers : []
}

/** 启动容器，返回操作后的新状态 */
export function startContainer(agentId: string, containerId: string): Promise<ContainerActionResponse> {
  return httpPost<ContainerActionResponse>(`${containerPath(agentId, containerId)}/start`)
}

/** 停止容器，返回操作后的新状态（通常为 exited） */
export function stopContainer(agentId: string, containerId: string): Promise<ContainerActionResponse> {
  return httpPost<ContainerActionResponse>(`${containerPath(agentId, containerId)}/stop`)
}

/** 重启容器，返回操作后的新状态（通常仍是 running） */
export function restartContainer(
  agentId: string,
  containerId: string
): Promise<ContainerActionResponse> {
  return httpPost<ContainerActionResponse>(`${containerPath(agentId, containerId)}/restart`)
}

/**
 * 删除容器。
 *
 * @param force          运行中的容器必须 force=true 才能删（后端会返回
 *                       409 runtime_failed 拒绝）。前端在确认框里要说明
 *                       「该容器正在运行，将强制停止并删除」。
 * @param removeVolumes  是否同时删除容器的匿名数据卷。
 *                       **默认 false**：删卷是数据丢失，和编排删除同一原则——
 *                       危险默认关闭，由用户在确认框里主动勾选。
 *
 * ## ⚠️ 只传 "true" 或不传，绝不传 "false"
 *
 * 后端对这两个参数的**非法值是 400 而不是宽松当 false 处理**，这是刻意的：
 * `remove_volumes=ture`（拼错）若被当成 true 会直接丢数据；
 * `=yes` 若被当成 false，用户以为删了其实没删。
 *
 * 因此这里用「只有 true 才拼参数」的写法，从源头杜绝出现非法值：
 * 不勾选 = 参数完全不存在 = 后端走默认 false，语义最清晰也最安全。
 */
export async function deleteContainer(
  agentId: string,
  containerId: string,
  options: { force?: boolean; removeVolumes?: boolean } = {}
): Promise<void> {
  const params = new URLSearchParams()
  // 严格 === true：传 undefined / false / 其他值都视为「不传」，不产生非法参数
  if (options.force === true) params.set('force', 'true')
  if (options.removeVolumes === true) params.set('remove_volumes', 'true')
  const query = params.toString()
  const suffix = query === '' ? '' : `?${query}`
  return httpDelete(`${containerPath(agentId, containerId)}${suffix}`)
}

/** 拉取容器日志尾部若干行 */
export async function fetchContainerLogs(
  agentId: string,
  containerId: string,
  tail = 200
): Promise<ContainerLogsResponse> {
  const res = await httpGet<ContainerLogsResponse>(
    `${containerPath(agentId, containerId)}/logs?tail=${tail}`
  )
  return {
    lines: Array.isArray(res.lines) ? res.lines : [],
    truncated: res.truncated === true
  }
}
