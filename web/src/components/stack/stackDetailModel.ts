import type { Container } from '@/types/container'

/** Pure rendering policy: unknown is not an address; host networking has no container IP. */
export function containerNetworkLines(container: Pick<Container, 'networks' | 'network_mode'>): string[] {
  if (container.network_mode === 'host' || container.networks?.some(network => network.name === 'host')) return ['主机网络']
  if (!container.networks?.length) return ['—']
  return container.networks.map(network => `${network.name || '—'} / ${[network.ip_address, network.ipv6_address].filter(Boolean).join(' / ') || '—'}`)
}

/** Snapshot guard shared by the route data channel. */
export function sameStackTarget(a: readonly [string, string], b: readonly [string, string]): boolean {
  return a[0] === b[0] && a[1] === b[1]
}
