<script setup lang="ts">
/**
 * 外部项目的容器列表
 *
 * ## 为什么这个组件非加不可
 *
 * 概览原来只显示「N 个容器」这个数字。用户点进详情就是为了看细节，
 * 而接口明明返回了名称/镜像/状态/端口/创建时间——只给一个数等于没看。
 *
 * ## 两个容易踩的坑
 *
 * 1. **`NDataTable` 必须显式 import**。Naive UI 在本项目没有全局注册，
 *    `vue-tsc` 对未注册的 kebab-case 标签**不报错**，表格会静默渲染成空。
 *    以前卷文件管理器就因为这个漏过——测试当时只断言源码，没发现界面是空的。
 *
 * 2. **`created` 是 Unix 秒**，不是毫秒。直接塞给 Date 会得到 1970 年。
 *
 * 另外：容器已由 Hub 按 StackID 过滤好，这里**不再二次筛选**，
 * 前端自己筛等于把归属规则实现两遍，迟早与后端不一致。
 */
import { computed, h } from 'vue'
import { useRouter } from 'vue-router'
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
  /** 服务器直连地址；端口链接靠它生成，空表示不可生成 */
  directAddress: string
}

const props = defineProps<Props>()

const router = useRouter()

/** 进入容器详情（与主容器列表一致的入口） */
function goDetail(containerId: string): void {
  if (props.agentId === '' || containerId === '') return
  void router.push({
    name: 'agent-container-detail',
    params: { id: props.agentId, containerId }
  })
}

/** 端口列已改用 ContainerPortCell，这里不再解析 ports 文本 */

const columns = computed<DataTableColumns<Container>>(() => [
  {
    title: '名称',
    key: 'name',
    minWidth: 180,
    // 名称可点击进详情；缺失时退回显示 ID，总比一格空白有用
    render: (row) =>
      h(
        'a',
        { class: 'arkade-link arkade-cell-mono', onClick: () => goDetail(row.id) },
        row.name !== '' ? row.name : row.id
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
    size="small"
    :row-key="(row: Container) => row.id"
    :scroll-x="820"
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
