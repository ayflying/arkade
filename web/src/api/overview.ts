/**
 * 概览数据聚合
 *
 * 概览页没有专属接口，这里把「服务器列表 + 每台的 5 类资源」聚合起来。
 *
 * ## 三个必须守住的约束
 *
 * 1. **不能 N+1 串行**。N 台服务器 × 5 个接口若串行发，10 台就是 50 个 RTT，
 *    页面会转很久。这里用 Promise.all 把同一台内部的 5 个请求并发，
 *    再把所有服务器并发展开——总耗时约等于「最慢的那一个请求」。
 *
 * 2. **单点失败不能拖垮整页**。任何一台离线，它那 5 个接口基本都会失败。
 *    如果整体 reject，用户明明能看到别的服务器也白搭。因此每个请求都包一层
 *    settle()，失败只记进 failures，统计照常进行。
 *
 * 3. **不能假装是全量**。服务器很多时逐个拉会打爆后端与浏览器连接数，
 *    因此限制只拉前 N 台，并明确回传 truncated=true，由界面提示
 *    「仅统计前 N 台」。
 */

import { listAgents } from '@/api/agents'
import { listContainers } from '@/api/containers'
import { listImages, listNetworks, listVolumes } from '@/api/resources'
import { listStacks } from '@/api/stacks'
import { ApiError } from '@/types/api'
import type { Agent } from '@/types/agent'
import type {
  AgentOverview,
  AgentResourceCounts,
  AggregationFailure,
  OverviewData,
  Settled
} from '@/types/overview'

/**
 * 最多参与统计的服务器台数。
 *
 * 取 20 的理由：正常运维规模下 20 台已覆盖绝大多数场景；每台 5 个请求，
 * 峰值并发 100 个，浏览器与 Hub 都能承受。超出部分不做静默忽略——
 * 界面会提示「仅统计前 N 台」。
 */
export const OVERVIEW_AGENT_LIMIT = 20

/** 把可能抛错的操作包成 Settled，错误统一转成中文文案 */
async function settle<T>(task: Promise<T>): Promise<Settled<T>> {
  try {
    return { ok: true, value: await task }
  } catch (err) {
    return { ok: false, message: err instanceof ApiError ? err.message : '请求失败' }
  }
}

/** 取容器计数：总数与运行中 */
async function fetchContainerCounts(
  agentId: string
): Promise<{ total: number; running: number }> {
  const list = await listContainers(agentId)
  return {
    total: list.length,
    running: list.filter((c) => c.state === 'running').length
  }
}

/** 取编排相关的统计：数量、漂移、未纳管 */
async function fetchStackStats(
  agentId: string
): Promise<{ total: number; drifted: string[]; orphans: string[] }> {
  const res = await listStacks(agentId)
  /*
   * 未纳管项目优先取 external_stacks（带状态等详情），
   * 后端暂未提供时退回 orphans（只有名字）。
   * 两者是同一批项目，不能相加——否则会重复统计。
   */
  const external = Array.isArray(res.external_stacks) ? res.external_stacks.map((s) => s.name) : []
  const orphans = external.length > 0 ? external : (res.orphans ?? [])
  // drift 只看后端给的布尔值，不自己比 config_hash / remote_hash——
  // 后端对空配置、未同步状态可能有额外规则，前端重复判断迟早不一致
  return {
    total: res.stacks.length,
    drifted: res.stacks.filter((s) => s.drift === true).map((s) => s.name),
    orphans
  }
}

/** 取镜像/网络/卷的数量：这三个接口都返回 { items: [...] } 包装对象 */
async function fetchImageCount(agentId: string): Promise<number> {
  const res = await listImages(agentId)
  return Array.isArray(res.images) ? res.images.length : 0
}

async function fetchNetworkCount(agentId: string): Promise<number> {
  const res = await listNetworks(agentId)
  return Array.isArray(res.networks) ? res.networks.length : 0
}

async function fetchVolumeCount(agentId: string): Promise<number> {
  const res = await listVolumes(agentId)
  return Array.isArray(res.volumes) ? res.volumes.length : 0
}

/** 空计数值，用于「某类资源拉取失败」时占位（失败会被计入 failures 单独提示） */
function emptyCounts(): AgentResourceCounts {
  return {
    containers: 0,
    runningContainers: 0,
    stacks: 0,
    images: 0,
    networks: 0,
    volumes: 0
  }
}

/** 服务器展示名：优先用户设置的显示名 */
function displayNameOf(agent: Agent): string {
  const display = (agent.display_name ?? '').trim()
  return display !== '' ? display : agent.name || agent.id
}

/**
 * 聚合单台服务器。
 *
 * 5 个请求并发发出（不是串行），任何一个失败都只记录不影响其它。
 */
async function aggregateOne(
  agent: Agent,
  failures: AggregationFailure[]
): Promise<AgentOverview> {
  const id = agent.id
  const name = displayNameOf(agent)

  // 同一台内部的 5 个请求并发
  const [containers, stacks, images, networks, volumes] = await Promise.all([
    settle(fetchContainerCounts(id)),
    settle(fetchStackStats(id)),
    settle(fetchImageCount(id)),
    settle(fetchNetworkCount(id)),
    settle(fetchVolumeCount(id))
  ])

  const counts = emptyCounts()

  // 每类失败单独记一条，界面提示时能说清「哪台服务器的哪类数据没统计到」
  function record(label: string, result: Settled<unknown>): void {
    if (!result.ok) {
      failures.push({ agentId: id, agentName: name, resource: label, message: result.message })
    }
  }
  record('容器', containers)
  record('编排', stacks)
  record('镜像', images)
  record('网络', networks)
  record('存储卷', volumes)

  if (containers.ok) {
    counts.containers = containers.value.total
    counts.runningContainers = containers.value.running
  }
  if (stacks.ok) {
    counts.stacks = stacks.value.total
  }
  if (images.ok) counts.images = images.value
  if (networks.ok) counts.networks = networks.value
  if (volumes.ok) counts.volumes = volumes.value

  return {
    agent,
    counts,
    driftedStacks: stacks.ok ? stacks.value.drifted : [],
    orphanStacks: stacks.ok ? stacks.value.orphans : []
  }
}

/**
 * 拉取概览数据。
 *
 * @param limit 最多统计多少台服务器，默认 OVERVIEW_AGENT_LIMIT
 */
export async function loadOverview(limit = OVERVIEW_AGENT_LIMIT): Promise<OverviewData> {
  const agents = await listAgents()

  const counted = agents.slice(0, limit)
  const failures: AggregationFailure[] = []

  // 所有服务器并发聚合：整体耗时 ≈ 最慢的那一个请求，而非 N 台累加
  const overviews = await Promise.all(counted.map((agent) => aggregateOne(agent, failures)))

  const totals: OverviewData['totals'] = {
    agents: agents.length,
    onlineAgents: agents.filter((a) => a.status === 'online').length,
    containers: 0,
    runningContainers: 0,
    stacks: 0,
    images: 0,
    networks: 0,
    volumes: 0
  }

  const problems: OverviewData['problems'] = {
    // 离线名单用**全部**服务器，不限于前 N 台：
    // 离线是最需要被看见的问题，哪怕这台没参与资源统计也必须报出来
    offlineAgents: agents.filter((a) => a.status === 'offline'),
    driftedStacks: [],
    orphanStacks: []
  }

  for (const item of overviews) {
    totals.containers += item.counts.containers
    totals.runningContainers += item.counts.runningContainers
    totals.stacks += item.counts.stacks
    totals.images += item.counts.images
    totals.networks += item.counts.networks
    totals.volumes += item.counts.volumes

    const name = displayNameOf(item.agent)
    for (const stackName of item.driftedStacks) {
      problems.driftedStacks.push({ agentId: item.agent.id, agentName: name, stackName })
    }
    for (const stackName of item.orphanStacks) {
      problems.orphanStacks.push({ agentId: item.agent.id, agentName: name, stackName })
    }
  }

  return {
    agents: overviews,
    failures,
    countedAgents: counted.length,
    totalAgents: agents.length,
    truncated: agents.length > limit,
    totals,
    problems
  }
}

export { displayNameOf }
