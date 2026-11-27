import type { Container } from '@/types/container'
import type { UnifiedStack } from '@/types/stack'

/** 忙碌时汇总未采样，优先用独立查询得到的实际容器列表。 */
export function stackContainerCounts(
  stack: Pick<UnifiedStack, 'status' | 'running' | 'total'>,
  containers: readonly Pick<Container, 'state'>[],
  containersError: string
) {
  if (stack.status !== 'busy') return { running: stack.running, total: stack.total }
  if (containersError) return { running: null, total: null }
  return { running: containers.filter(({ state }) => state === 'running').length, total: containers.length }
}

/**
 * 容器网络信息的一行：网络名与地址分开。
 *
 * ## 为什么不再拼成一条字符串
 *
 * 以前这里返回 `"allinssl_default / 172.29.0.2"`，全部塞在一个单元格里。
 * 两个问题：
 * - 网络名长一点（`allinssl_default` 这种带项目前缀的很常见）就会把
 *   地址挤到看不见的地方，而地址恰恰是排查时真正要抄走的那个值；
 * - 网络名和地址是**两类信息**，混在一格就只能整格复制，
 *   没法只取地址。
 *
 * 所以拆成结构化字段，由表格分两列渲染。
 * 一个容器可能加入多个网络，因此返回数组，每个网络一行。
 */
export interface ContainerNetworkEntry {
  /** 网络名；拿不到时为空串，由界面显示「—」 */
  name: string
  /** 该网络下的容器 IP（IPv4）；无地址时为空串 */
  ipv4: string
  /** IPv6 地址；通常为空 */
  ipv6: string
}

/**
 * 解析容器的网络信息。
 *
 * 三种边界必须先判掉，它们不是「地址未知」而是**根本没有容器 IP**：
 * - `host` 网络：容器不拥有独立网络栈，拿不到容器 IP；
 * - 没有任何网络：尚未启动或已停止。
 *
 * 把这两种显示成「—」会让人以为是读取失败，所以用 `kind` 明确区分出来。
 */
export type ContainerNetworkView =
  | { kind: 'host' }
  | { kind: 'none' }
  | { kind: 'networks'; entries: ContainerNetworkEntry[] }

export function containerNetworkView(
  container: Pick<Container, 'networks' | 'network_mode'>
): ContainerNetworkView {
  if (container.network_mode === 'host' || container.networks?.some((network) => network.name === 'host')) {
    return { kind: 'host' }
  }
  if (!container.networks?.length) return { kind: 'none' }
  return {
    kind: 'networks',
    entries: container.networks.map((network) => ({
      name: network.name ?? '',
      ipv4: network.ip_address ?? '',
      ipv6: network.ipv6_address ?? ''
    }))
  }
}

/** 单个网络的地址展示：IPv4 优先，没有 IPv4 时退回 IPv6，都没有给空串 */
export function networkAddressText(entry: ContainerNetworkEntry): string {
  if (entry.ipv4 !== '') return entry.ipv4
  return entry.ipv6
}

/** Snapshot guard shared by the route data channel. */
export function sameStackTarget(a: readonly [string, string], b: readonly [string, string]): boolean {
  return a[0] === b[0] && a[1] === b[1]
}
