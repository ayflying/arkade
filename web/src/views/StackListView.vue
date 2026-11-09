<script setup lang="ts">
/**
 * 容器编排列表页 /agents/:id/stacks
 *
 * 功能：列出编排（名称/状态/服务数/最后更新/drift 告警），可进入详情、可新建。
 *
 * ## 三件必须显式告知用户的事
 *
 * 1. **drift（配置漂移）**：Hub 记录的 compose.yml 哈希与 Agent 本机文件哈希不一致，
 *    也就是「有人绕过 Hub 直接改了服务器上的文件」。
 *    不提示的话用户会以为 Hub 里看到的就是线上真实配置，
 *    后续在 Hub 里编辑保存还会覆盖掉别人手改的内容——最危险的静默失败。
 *
 * 2. **orphans（未纳管编排）**：Agent 本机存在、但 Hub 数据库里没有的编排。
 *    它们无法通过本界面管理（界面数据来自 Hub），**不显示的话用户永远发现不了**。
 *
 * 3. **agent_online**：离线时接口仍返回 200、各条 status 为 'offline'。
 *    不能靠"请求是否失败"判断在线状态，否则会把「本地缓存的旧列表」
 *    误当成实时数据。
 */
import { computed, h, onMounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import { NAlert, NButton, NDataTable, NIcon, NTag } from 'naive-ui'
import type { DataTableColumns } from 'naive-ui'
import AppIcon from '@/components/common/AppIcon.vue'
import EmptyState from '@/components/common/EmptyState.vue'
import PageHeader from '@/components/common/PageHeader.vue'
import StackStateTag from '@/components/stack/StackStateTag.vue'
import { listStacks } from '@/api/stacks'
import { useCurrentAgent } from '@/composables/useCurrentAgent'
import type { StackSummary } from '@/types/stack'
import { ApiError } from '@/types/api'
import { formatAbsoluteTime, formatRelativeTime } from '@/utils/formatTime'

const router = useRouter()
const { agentId, isOnline: storeOnline, displayName } = useCurrentAgent()

const stacks = ref<StackSummary[]>([])
/** 未纳管编排：Agent 上有、Hub 里没有 */
const orphans = ref<string[]>([])
/** 后端告知的 Agent 在线状态（离线时接口仍返回 200） */
const agentOnlineFromApi = ref(true)
const loading = ref(false)
const errorText = ref('')
const loaded = ref(false)

/**
 * 拉取编排列表；失败保留旧数据只置错误文案，避免刷新失败把表格清空。
 *
 * 注意 agent_online：后端在 Agent 离线时**仍然返回 200**，
 * 所以这里不能把「请求成功」等同于「服务器在线」。
 */
async function refresh(): Promise<void> {
  if (agentId.value === null) return
  loading.value = true
  errorText.value = ''
  try {
    const res = await listStacks(agentId.value)
    stacks.value = res.stacks
    orphans.value = res.orphans ?? []
    agentOnlineFromApi.value = res.agent_online
    loaded.value = true
  } catch (err) {
    errorText.value = err instanceof ApiError ? err.message : '加载编排列表失败'
  } finally {
    loading.value = false
  }
}

/**
 * 最终在线状态：接口说了算，但接口还没回来时先用 store 的判断兜底。
 *
 * 为什么以接口为准：store 里的 Agent 状态来自列表接口，可能已经过期；
 * 编排接口返回的 agent_online 是这次请求时 Agent 的真实状态。
 */
const isOnline = computed<boolean>(() => (loaded.value ? agentOnlineFromApi.value : storeOnline.value))

/** 有漂移的编排数量：>0 时顶部给醒目汇总 */
const driftCount = computed(() => stacks.value.filter((s) => s.drift).length)

const subtitle = computed(() => {
  if (!loaded.value) return '正在加载编排…'
  const total = stacks.value.length
  const running = stacks.value.filter((s) => s.status === 'running').length
  const driftText = driftCount.value > 0 ? `，${driftCount.value} 个存在配置漂移` : ''
  const orphanText = orphans.value.length > 0 ? `，${orphans.value.length} 个未纳管` : ''
  return `${displayName.value} · 共 ${total} 个编排，运行中 ${running} 个${driftText}${orphanText}`
})

/** 进入详情 */
function goDetail(row: StackSummary): void {
  if (agentId.value === null) return
  void router.push({
    name: 'agent-stack-detail',
    params: { id: agentId.value, name: row.name }
  })
}

/** 去新建页 */
function goCreate(): void {
  if (agentId.value === null) return
  void router.push({ name: 'agent-stack-create', params: { id: agentId.value } })
}

/**
 * 最后一列展示同步信息。
 *
 * 用 updated_at（Hub 里的更新时间）为主，synced_at（最后一次成功下发到服务器）
 * 放在 tooltip 里。两者差异大往往意味着「改了但没同步成功」，值得让用户能查到。
 */
function renderSyncCell(row: StackSummary): ReturnType<typeof h> {
  const hasSync = row.synced_at > 0
  const syncText = hasSync ? `已同步：${formatAbsoluteTime(row.synced_at)}` : '从未同步到服务器'
  return h(
    'span',
    { title: `更新时间：${formatAbsoluteTime(row.updated_at)}\n${syncText}`, class: 'arkade-cell-muted' },
    formatRelativeTime(row.updated_at)
  )
}

const columns = computed<DataTableColumns<StackSummary>>(() => [
  {
    title: '名称',
    key: 'name',
    minWidth: 200,
    render: (row) => {
      const nodes = [h('a', { class: 'arkade-link', onClick: () => goDetail(row) }, row.name)]
      // drift 标记紧跟在名字后面：这是最重要的警告，不能藏进操作列
      if (row.drift) {
        nodes.push(
          h(
            NTag,
            {
              size: 'tiny',
              bordered: false,
              type: 'warning',
              title: 'Hub 记录的配置与服务器本机文件不一致，可能有人直接修改了文件'
            },
            {
              default: () => '配置漂移',
              icon: () => h(NIcon, { size: 12 }, { default: () => h(AppIcon, { name: 'warning' }) })
            }
          )
        )
      }
      return h('span', { class: 'arkade-inline-tags' }, nodes)
    }
  },
  {
    title: '状态',
    key: 'status',
    minWidth: 100,
    render: (row) => h(StackStateTag, { state: row.status })
  },
  {
    title: '服务数',
    key: 'running',
    minWidth: 110,
    // 后端字段是 running / total（不是 services_running / services_total）
    render: (row) => {
      const running = row.running
      const total = row.total
      // 全部运行绿色、部分运行橙色、全停灰色，便于快速扫视
      const color = total > 0 && running === total ? '#18a058' : running === 0 ? '#8b9099' : '#f0a020'
      return h('span', { style: `color:${color}` }, `运行 ${running} / 共 ${total}`)
    }
  },
  {
    title: '最后更新',
    key: 'updated_at',
    minWidth: 140,
    render: (row) => renderSyncCell(row)
  },
  {
    title: '操作',
    key: 'actions',
    width: 110,
    fixed: 'right',
    render: (row) =>
      h(
        NButton,
        { size: 'tiny', quaternary: true, onClick: () => goDetail(row) },
        { default: () => '查看详情' }
      )
  }
])

onMounted(() => {
  void refresh()
})
</script>

<template>
  <div class="stack-page">
    <page-header title="容器编排" :subtitle="subtitle">
      <template #actions>
        <n-button size="small" :disabled="!isOnline" :loading="loading" @click="refresh">
          刷新
        </n-button>
        <n-button size="small" type="primary" @click="goCreate">新建编排</n-button>
      </template>
    </page-header>

    <!--
      离线提示：后端在 Agent 离线时仍返回 200，这里必须明确告知，
      否则用户看到的是本地缓存的旧列表，会误以为是实时状态。
    -->
    <div v-if="loaded && !isOnline" class="stack-page__alert">
      <n-alert type="warning" :bordered="false" title="服务器离线">
        以下编排信息来自 Hub 数据库，可能不是服务器上的实时状态。服务器恢复在线后请刷新。
      </n-alert>
    </div>

    <!-- 漂移汇总：有漂移时始终在顶部亮出来，不指望用户自己逐行看 -->
    <div v-if="driftCount > 0" class="stack-page__alert">
      <n-alert type="warning" :bordered="false" title="检测到配置漂移">
        有 {{ driftCount }} 个编排的 compose.yml 与 Hub 记录不一致，通常是因为有人直接在服务器上修改了文件。
        在 Hub 上编辑保存会覆盖那些改动，请先确认线上真实配置。
      </n-alert>
    </div>

    <!--
      未纳管编排：Agent 本机有、Hub 数据库里没有。
      这类编排无法通过本界面管理（界面数据来自 Hub），
      不显示的话用户永远发现不了它们，所以单独列出来。
    -->
    <div v-if="orphans.length > 0" class="stack-page__alert">
      <n-alert type="info" :bordered="false" title="存在未纳管的编排">
        以下编排存在于服务器上，但未登记在 Hub 中，因此无法在此界面管理：
        <div class="stack-page__orphans">
          <n-tag
            v-for="name in orphans"
            :key="name"
            size="small"
            :bordered="false"
            type="info"
            class="stack-page__orphan-tag"
          >
            {{ name }}
          </n-tag>
        </div>
        如需接管，请在服务器上确认其 compose.yml 后重新纳管。
      </n-alert>
    </div>

    <div v-if="errorText !== ''" class="stack-page__alert stack-page__alert--row">
      <n-alert type="error" closable>{{ errorText }}</n-alert>
      <n-button size="small" quaternary @click="refresh">重试</n-button>
    </div>

    <div class="table-card arkade-table-scroll">
      <n-data-table
        v-if="stacks.length > 0 || loading"
        :columns="columns"
        :data="stacks"
        :loading="loading"
        :row-key="(row: StackSummary) => row.name"
        :bordered="false"
        :single-line="false"
        size="medium"
        :scroll-x="900"
      />
      <empty-state
        v-else-if="loaded"
        description="这台服务器上还没有容器编排。新建一个编排来批量管理多个容器。"
      >
        <template #extra>
          <n-button size="small" type="primary" @click="goCreate">新建编排</n-button>
        </template>
      </empty-state>
      <div v-else class="table-skeleton">
        <n-tag :bordered="false" size="small">正在加载编排列表…</n-tag>
      </div>
    </div>
  </div>
</template>

<style scoped>
.stack-page__alert {
  margin-bottom: 16px;
}

/* 错误提示 + 重试按钮并排 */
.stack-page__alert--row {
  display: flex;
  align-items: center;
  gap: 8px;
}

.stack-page__alert--row :deep(.n-alert) {
  flex: 1;
}

/* 未纳管编排的名字标签：换行排列，长名字不会撑破卡片 */
.stack-page__orphans {
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
  margin: 8px 0;
}

.stack-page__orphan-tag {
  font-family: var(--arkade-mono-font);
}

.table-card {
  background: #fff;
  border-radius: 8px;
  padding: 4px;
}

.table-skeleton {
  padding: 32px;
  text-align: center;
}
</style>
