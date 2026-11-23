import { listContainers } from '@/api/containers'
import { listImages, listNetworks, listVolumes } from '@/api/resources'
import { listStacks } from '@/api/stacks'
import { ApiError } from '@/types/api'
import type { AgentOverview } from '@/types/overview'

/** Resource lookups are only for the explicitly selected server, not fleet health. */
export async function loadAgentOverview(agentId: string, signal: AbortSignal): Promise<AgentOverview> {
  const overview: AgentOverview = { agentId, counts: {}, driftedStacks: [], orphanStacks: [], failures: [] }
  const { counts, failures } = overview
  async function read(resource: string, load: () => Promise<void>): Promise<void> {
    try {
      await load()
    } catch (err) {
      if (!signal.aborted) failures.push({ resource, message: err instanceof ApiError ? err.message : '请求失败' })
    }
  }
  await Promise.all([
    read('容器', async () => {
      const containers = await listContainers(agentId, signal)
      counts.containers = containers.length
      counts.runningContainers = containers.filter((container) => container.state === 'running').length
    }),
    read('编排', async () => {
      const stacks = await listStacks(agentId, signal)
      counts.stacks = stacks.stacks.length
      if (!stacks.agent_online) {
        failures.push({ resource: '编排状态', message: '无法读取服务器上的编排，配置漂移与未纳管项目暂不可确认' })
        return
      }
      overview.driftedStacks = stacks.stacks.filter((stack) => stack.drift === true).map((stack) => stack.name)
      overview.orphanStacks = stacks.external_stacks.map((stack) => stack.name)
      counts.unmanagedStacks = overview.orphanStacks.length
    }),
    read('镜像', async () => { counts.images = (await listImages(agentId, signal)).images?.length ?? 0 }),
    read('网络', async () => { counts.networks = (await listNetworks(agentId, signal)).networks?.length ?? 0 }),
    read('存储卷', async () => { counts.volumes = (await listVolumes(agentId, signal)).volumes?.length ?? 0 })
  ])
  return overview
}
