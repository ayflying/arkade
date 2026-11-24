<script setup lang="ts">
/**
 * 容器挂载点表
 *
 * 挂载会暴露宿主路径，属于 manage 权限范围，所以无权限时不渲染表格
 * 而是给一句说明——渲染一张空表会让人以为「这个容器没有挂载」，
 * 那是个错误的结论。
 *
 * ## 为什么只有 volume 类型能点开
 *
 * 卷文件模块（`/agents/:id/volumes/:name/files`）的模型是「helper 容器挂载
 * 一个**具名卷**」：Agent 先 inspect 卷拿到 daemon 给的 Mountpoint，再以
 * bind 方式把它挂进 helper。整条链路只认卷名。
 *
 * bind 挂载的 Source 是任意宿主绝对路径，没有卷名可查，也没有对应的
 * inspect 对象——要支持它必须新增一条「按宿主路径读取」的通道，并配套
 * 路径白名单与符号链接逃逸防护，那是另一套安全模型。所以这里如实标成
 * 「不能在线打开」，而不是给一个点了必然失败的入口。
 *
 * 判断依据是后端给的 `name`，**不是**从 `source` 截路径：volume 的 source
 * 是宿主目录，卷名可以合法地包含下划线和点，截取既会截错、也会把宿主路径
 * 当成卷名去查。
 */
import { computed, h } from 'vue'
import { NButton, NDataTable, NTag } from 'naive-ui'
import type { DataTableColumns } from 'naive-ui'
import type { ContainerMount } from '@/types/container'

interface Props {
  mounts: ContainerMount[]
  forbidden: boolean
}

const props = defineProps<Props>()

const emit = defineEmits<{
  /** 用户点了某条挂载：交给父组件做跳转（保持表格不依赖路由） */
  open: [mount: ContainerMount]
}>()

/**
 * 该挂载能否在「卷文件」里打开。
 *
 * 只认 volume + 非空卷名：tmpfs 根本不在磁盘上，bind 没有卷名。
 */
function isOpenable(mount: ContainerMount): boolean {
  return mount.type === 'volume' && mount.name !== ''
}

const columns = computed<DataTableColumns<ContainerMount>>(() => [
  {
    title: '类型',
    key: 'type',
    width: 100,
    render: (row) => h(NTag, { size: 'small', bordered: false }, { default: () => row.type || '—' })
  },
  {
    title: '宿主来源',
    key: 'source',
    minWidth: 220,
    ellipsis: { tooltip: true },
    render: (row) => h('span', { class: 'arkade-cell-mono' }, row.source || '—')
  },
  {
    title: '容器内路径',
    key: 'destination',
    minWidth: 220,
    ellipsis: { tooltip: true },
    render: (row) => h('span', { class: 'arkade-cell-mono' }, row.destination || '—')
  },
  {
    title: '读写',
    key: 'read_only',
    width: 90,
    render: (row) => (row.read_only ? '只读' : '可写')
  },
  {
    title: '文件',
    key: 'actions',
    width: 140,
    render: (row) =>
      isOpenable(row)
        ? h(
            NButton,
            {
              size: 'tiny',
              quaternary: true,
              onClick: () => emit('open', row)
            },
            { default: () => '打开文件' }
          )
        : h('span', { class: 'mounts__hint' }, row.type === 'volume' ? '无卷名' : '不支持')
  }
])
</script>

<template>
  <div v-if="forbidden" class="mounts__empty">查看挂载需要该服务器的管理权限。</div>
  <div v-else-if="mounts.length === 0" class="mounts__empty">该容器没有挂载任何目录或卷。</div>
  <div v-else>
    <n-data-table
      :columns="columns"
      :data="mounts"
      :bordered="false"
      :single-line="false"
      size="medium"
      :row-key="(row: ContainerMount) => `${row.type}:${row.source}:${row.destination}`"
      :scroll-x="820"
    />
    <!--
      说清为什么 bind 不能点开：用户看到「不支持」需要原因，
      否则会当成功能没做完。
    -->
    <p v-if="props.mounts.some((m) => !isOpenable(m))" class="mounts__note">
      具名卷（volume）可以直接打开浏览文件。宿主目录（bind）与临时文件系统（tmpfs）不提供在线打开：
      它们没有卷名，文件模块只处理具名卷。
    </p>
  </div>
</template>

<style scoped>
.mounts__empty {
  padding: 16px 0;
  font-size: 13px;
  color: var(--arkade-text-tertiary);
}

.mounts__hint {
  font-size: 12px;
  color: var(--arkade-text-tertiary);
}

.mounts__note {
  margin: 8px 0 0;
  font-size: 12px;
  line-height: 1.6;
  color: var(--arkade-text-tertiary);
}
</style>
