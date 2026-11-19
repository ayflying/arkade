<script setup lang="ts">
/** Shared summary-only table for managed and external projects. No inspect requests. */
import { computed, h } from 'vue'
import type { VNode } from 'vue'
import { RouterLink } from 'vue-router'
import { containerNetworkView, networkAddressText } from './stackDetailModel'
import { NDataTable, NEmpty } from 'naive-ui'
import type { DataTableColumns } from 'naive-ui'
import ContainerPortCell from '@/components/container/ContainerPortCell.vue'
import ContainerStateTag from '@/components/container/ContainerStateTag.vue'
import type { Container } from '@/types/container'
import { formatAbsoluteTime, formatRelativeTime } from '@/utils/formatTime'

interface Props {
  containers: Container[]
  /** 所属服务器：名称链接要用它拼详情路由 */
  agentId: string
  /** 服务器访问地址（完整地址，含 http/https 前缀）；端口链接靠它生成，空表示不可生成 */
  directAddress: string
}

const props = defineProps<Props>()

/**
 * 渲染「网络」或「容器 IP」单元格。
 *
 * `host` / `none` 这两种不是「地址未知」，而是**这个容器根本没有容器 IP**，
 * 必须用文字说清楚；显示成「—」会让用户以为读取失败去翻日志。
 */
function renderNetworkCell(row: Container, field: 'name' | 'address'): string | VNode {
  const view = containerNetworkView(row)
  if (view.kind === 'host') return h('span', { class: 'arkade-cell-muted' }, '主机网络')
  if (view.kind === 'none') return h('span', { class: 'arkade-cell-muted' }, '—')
  // 一个容器可能加入多个网络：每个网络一行，顺序与 docker 返回一致
  return h(
    'div',
    view.entries.map((entry) => {
      const text = field === 'name' ? entry.name || '—' : networkAddressText(entry) || '—'
      return h('div', { class: 'arkade-cell-mono' }, text)
    })
  )
}

const columns = computed<DataTableColumns<Container>>(() => [
  {
    title: '名称',
    key: 'name',
    minWidth: 180,
    // 名称可点击进详情；缺失时退回显示 ID，总比一格空白有用
    render: (row) =>
      h(
        RouterLink,
        // 主名称统一用 arkade-cell-name（14px）：编排详情里的容器表和
        // 容器列表页展示的是同一种对象，字号不该因为换了页面就变。
        { class: 'arkade-link arkade-cell-name', to: { name: 'agent-container-detail', params: { id: props.agentId, containerId: row.id } } },
        { default: () => row.name || row.id }
      )
  },
  {
    title: '镜像',
    key: 'image',
    minWidth: 180,
    ellipsis: { tooltip: true },
    render: (row) => h('span', { class: 'arkade-cell-mono' }, row.image !== '' ? row.image : '—')
  },
  {
    title: '状态',
    key: 'state',
    width: 110,
    render: (row) => h(ContainerStateTag, { state: row.state })
  },
  {
    /*
     * 网络与容器 IP 分成两列。
     *
     * 以前合成一格「网络 / 容器 IP」显示成 `allinssl_default / 172.29.0.2`：
     * 网络名一长就把地址挤没了，而地址才是排查时要抄走的那个值；
     * 两格也能各自复制，不用整格复制再手工切。
     */
    title: '网络',
    key: 'networks',
    minWidth: 200,
    ellipsis: { tooltip: true },
    render: (row) => renderNetworkCell(row, 'name')
  },
  {
    title: '容器 IP',
    key: 'containerIp',
    minWidth: 170,
    ellipsis: { tooltip: true },
    render: (row) => renderNetworkCell(row, 'address')
  },
  {
    title: '端口',
    key: 'ports',
    minWidth: 200,
    /*
     * 复用与主容器列表同一个单元格：外部项目的容器也是容器，
     * 端口该不该给链接、通配地址怎么替换，规则必须完全一致。
     * 这里不再显示原始 ports 文本（`0.0.0.0:8080->80/tcp` 那种点了必坏）。
     */
    render: (row) =>
      h(ContainerPortCell, {
        container: row,
        agentId: props.agentId,
        directAddress: props.directAddress
      })
  },
  {
    title: '创建时间',
    key: 'created',
    width: 130,
    // 相对时间更直观，绝对时间放 title 里便于排查
    render: (row) =>
      row.created > 0
        ? h(
            'span',
            { title: formatAbsoluteTime(row.created), class: 'arkade-cell-muted' },
            formatRelativeTime(row.created)
          )
        : h('span', { class: 'ext-containers__muted' }, '—')
  }
])
</script>

<template>
  <!-- 空态要说清楚"这是正常的"，否则用户以为加载失败 -->
  <n-empty
    v-if="containers.length === 0"
    description="这个编排当前没有容器，可能是还没有启动过。"
    class="ext-containers__empty"
  />
  <n-data-table
    v-else
    :columns="columns"
    :data="containers"
    :bordered="false"
    :single-line="false"
    size="medium"
    :row-key="(row: Container) => row.id"
    :scroll-x="1100"
  />
</template>

<style scoped>
.ext-containers__empty {
  padding: 20px 0;
}

.ext-containers__muted {
  color: var(--arkade-text-tertiary);
}

.ext-containers__ports {
  display: inline-flex;
  flex-wrap: wrap;
  gap: 4px;
}

.ext-containers__port {
  font-family: var(--arkade-mono-font);
  font-size: 12px;
  padding: 1px 5px;
  border-radius: 3px;
  background: var(--arkade-page-bg);
  border: 1px solid var(--arkade-border-color);
}
</style>
