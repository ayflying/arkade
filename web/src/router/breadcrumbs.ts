import type { RouteLocationRaw, RouteMeta } from 'vue-router'

declare module 'vue-router' {
  interface RouteMeta {
    breadcrumbParent?: string
    breadcrumbParam?: string
  }
}

export interface CrumbItem {
  label: string
  to?: RouteLocationRaw
}

interface BreadcrumbRoute {
  meta: RouteMeta
  params: Record<string, string | string[]>
}

export function buildBreadcrumbs(
  route: BreadcrumbRoute,
  server: { id: string; label: string } | null,
  resolveMeta: (to: RouteLocationRaw) => RouteMeta
): CrumbItem[] {
  const title = typeof route.meta.title === 'string' ? route.meta.title : '概览'
  if (!server) return [{ label: title }]

  // 服务器名只是当前操作的上下文，不应跳到某个默认列表。
  const items: CrumbItem[] = [{ label: '服务器', to: { name: 'agents' } }, { label: server.label }]
  if (route.meta.breadcrumbParent) {
    const to = { name: route.meta.breadcrumbParent, params: { id: server.id } }
    const parentTitle = resolveMeta(to).title
    if (typeof parentTitle === 'string') items.push({ label: parentTitle, to })
  }

  const raw = route.meta.breadcrumbParam ? route.params[route.meta.breadcrumbParam] : undefined
  const current = Array.isArray(raw) ? raw[0] : raw
  items.push({ label: current || title })
  return items
}
