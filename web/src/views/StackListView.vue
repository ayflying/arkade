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
import StackImportModal from '@/components/stack/StackImportModal.vue'
import AutoAdoptStatus from '@/components/stack/AutoAdoptStatus.vue'
import { listStacks } from '@/api/stacks'
import { useCurrentAgent } from '@/composables/useCurrentAgent'
import { usePolling } from '@/composables/usePolling'
import type { ExternalStack, StackSummary } from '@/types/stack'
import { ApiError } from '@/types/api'
import { formatAbsoluteTime, formatRelativeTime } from '@/utils/formatTime'

const router = useRouter()
const { agentId, isOnline: storeOnline, displayName } = useCurrentAgent()

/** 导入纳管弹窗 */
const importShow = ref(false)
/** 要导入的项目名（来自被点的那一行） */
const importProjectName = ref('')

/** 打开导入弹窗 */
function openImport(projectName: string): void {
  importProjectName.value = projectName
  importShow.value = true
}

const stacks = ref<StackSummary[]>([])
/** 未纳管编排：Agent 上有、Hub 里没有（只有名字，旧字段兼容） */
const orphans = ref<string[]>([])
/** 未纳管项目的详情（新字段，可用于画成表格）；后端未提供时为空 */
const externalStacks = ref<ExternalStack[]>([])
/** 后端告知的 Agent 在线状态（离线时接口仍返回 200） */
const agentOnlineFromApi = ref(true)
const loading = ref(false)
const errorText = ref('')
const loaded = ref(false)

/**
 * 拉取编排列表；失败保留旧数据只置错误文案，避免刷新失败把表格清空。
 *
 * @param silent 静默刷新：自动轮询用，不显示 loading（否则每 10 秒闪一次），
 *               且失败不写 errorText——自动刷新偶发失败不该打断用户当前操作。
 *
 * 注意 agent_online：后端在 Agent 离线时**仍然返回 200**，
 * 所以这里不能把「请求成功」等同于「服务器在线」。
 */
async function refresh(silent = false): Promise<void> {
  if (agentId.value === null) return
  if (!silent) loading.value = true
  if (!silent) errorText.value = ''
  try {
    const res = await listStacks(agentId.value)
    stacks.value = res.stacks
    orphans.value = Array.isArray(res.orphans) ? res.orphans : []
    externalStacks.value = Array.isArray(res.external_stacks) ? res.external_stacks : []
    agentOnlineFromApi.value = res.agent_online
    loaded.value = true
    if (silent) errorText.value = ''
  } catch (err) {
    if (silent) {
      // 交给 usePolling 计入连续失败并退避
      throw err
    }
    errorText.value = err instanceof ApiError ? err.message : '加载编排列表失败'
  } finally {
    if (!silent) loading.value = false
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

/**
 * 未纳管项目名集合（优先用 external_stacks，缺字段时退回 orphans）。
 *
 * 两个字段来源相同（Agent 上有、Hub 里没有），但信息量不同：
 * - external_stacks 带状态与服务数，可画表；
 * - orphans 只有名字，只能列标签。
 * 后端只要提供了 external_stacks 就用它，避免同一批项目出现两份。
 */
const unmanagedNames = computed<string[]>(() =>
  externalStacks.value.length > 0
    ? externalStacks.value.map((s) => s.name)
    : orphans.value
)

/** 未纳管数量 */
const unmanagedCount = computed(() => unmanagedNames.value.length)

/**
 * 已纳管数量：只算 Hub 里有记录的。
 *
 * 为什么分开统计：用户看到「编排数」时会把它当成「服务器上跑着的全部编排」，
 * 而实际上 Arkade 只能管到纳管的那部分。混在一起报一个总数会误导——
 * 用户会以为界面上列出了全部，然后对「为什么有的点不进去」感到困惑。
 */
const managedCount = computed(() => stacks.value.length)

const subtitle = computed(() => {
  if (!loaded.value) return '正在加载编排…'
  const running = stacks.value.filter((s) => s.status === 'running').length
  const driftText = driftCount.value > 0 ? `，${driftCount.value} 个存在配置漂移` : ''
  // 未纳管单独加一句，且用「未纳管」而不是并入总数，让两个口径都清楚
  const unmanagedText = unmanagedCount.value > 0 ? `，另有 ${unmanagedCount.value} 个未纳管` : ''
  return `${displayName.value} · 已纳管 ${managedCount.value} 个（运行中 ${running} 个）${unmanagedText}${driftText}`
})

/**
 * 是否需要显示「自动接管」列。
 *
 * 旧 Hub 不返回该字段，所有行都是 unknown。此时**整列不显示**，
 * 否则会多出一列全是空白，用户会以为功能坏了。
 * 任一行动起来才显示整列，保证表格结构稳定（不会逐行时有时无）。
 */
const showAutoAdoptColumn = computed(() =>
  externalStacks.value.some((s) => (s.auto_adopt_status ?? 'unknown') !== 'unknown')
)

/**
 * 未纳管项目表格列。
 *
 * 除「导入纳管」外不放任何操作：
 * 这些是用户自己在服务器上起的项目，Arkade 不该假装能编辑/启停它们
 * （会打到不存在的接口，或者更糟——误伤线上服务）。
 */
const unmanagedColumns = computed<DataTableColumns<ExternalStack>>(() => [
  {
    title: '编排名',
    key: 'name',
    minWidth: 180,
    render: (row) => h('span', { class: 'arkade-cell-mono' }, row.name)
  },
  {
    title: '状态',
    key: 'status',
    width: 110,
    render: (row) => h(StackStateTag, { state: row.status })
  },
  // 自动接管情况：仅当后端确实给了信息时才出现（见 showAutoAdoptColumn）
  ...(showAutoAdoptColumn.value
    ? [
        {
          title: '自动接管',
          key: 'auto_adopt',
          minWidth: 200,
          render: (row: ExternalStack) =>
            h(AutoAdoptStatus, {
              status: row.auto_adopt_status ?? 'unknown',
              reason: row.auto_adopt_reason ?? ''
            })
        }
      ]
    : []),
  {
    title: '服务',
    key: 'services',
    width: 160,
    /*
     * 后端给的是 services: string[]（服务名列表），不是数字。
     * 之前这里写 row.services > 0 是按错误类型写的：
     * 数组和数字比较永远为 false，于是永远显示「—」。
     *
     * 显示数量、名字进 tooltip：服务名可能很长，
     * 全列出来会把表格撑爆，但用户排查时需要知道具体是哪些。
     *
     * null = 未知（字段缺失/Agent 离线）→ 显示「—」；
     * 空数组 = 确实没有服务 → 显示「0 个」。两者不能混。
     */
    render: (row) => {
      const names = row.services
      if (!Array.isArray(names)) return '—'
      if (names.length === 0) {
        return h('span', { class: 'arkade-cell-muted' }, '0 个')
      }
      return h(
        'span',
        { title: names.join('、'), class: 'arkade-cell-muted' },
        `${names.length} 个`
      )
    }
  },
  {
    title: '容器数',
    key: 'containers',
    width: 110,
    /*
     * 运行中/总数：只给总数看不出服务到底活没活。
     * 任一为 null（未知）就整体显示「—」——
     * 既不能拼出「2 / undefined」，也不能把未知当 0 显示成「0 / 0」。
     */
    render: (row) => {
      const running = row.running
      const total = row.total
      if (typeof running !== 'number' || typeof total !== 'number') return '—'
      if (!Number.isFinite(running) || !Number.isFinite(total)) return '—'
      return `${running} / ${total}`
    }
  },
  {
    title: '操作',
    key: 'actions',
    width: 110,
    /*
     * 一律显示入口，**不按角色隐藏**。
     *
     * 后端权限是「用户组 × 该 Agent」授予的 view/operate/manage，
     * 普通用户被授予 manage 后同样可以导入。前端没有接口能查到自己在某台
     * Agent 上的档位，按 admin/user 角色猜会让有权限的用户永远看不到入口。
     * 真正的拦截在后端（import 要 manage），被拒时按 403 提示。
     */
    render: (row) =>
      h(
        NButton,
        {
          size: 'tiny',
          tertiary: true,
          onClick: (e: MouseEvent) => {
            e.stopPropagation()
            openImport(row.name)
          }
        },
        () => '导入纳管'
      )
  }
])

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
    title: '同步',
    key: 'synced',
    minWidth: 120,
    /*
     * 未同步必须在列表里**一眼可见**，不能只藏在 tooltip 里。
     *
     * 自动接管失败的记录会先落 Hub 再尝试下发；下发失败时它仍然出现在
     * `stacks` 里，`synced_at` 为 0。此时它看起来和正常编排一模一样，
     * 用户点「启动/重建」就会与 Agent 侧不存在的东西打架。
     *
     * 有 `sync_hint`（Hub 内存里记的原因）就挂到 title 上，
     * 让用户能在列表里直接看到为什么失败；Hub 重启后原因为空也不影响
     * tag 本身继续显示——**始终不伪装成同步成功**。
     */
    render: (row) => {
      if (row.synced_at > 0) {
        return h('span', { class: 'arkade-cell-muted' }, '已同步')
      }
      const hint = typeof row.sync_hint === 'string' ? row.sync_hint.trim() : ''
      return h(
        NTag,
        {
          size: 'tiny',
          bordered: false,
          type: 'error',
          title: hint !== '' ? hint : '配置已记录在 Hub，但尚未成功下发到服务器'
        },
        {
          default: () => '未同步'
        }
      )
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
    /*
     * 后端字段是 running / total（不是 services_running / services_total）。
     * 任一为 null（未知）就显示「—」——不能把未知当成 0 渲染成「运行 0 / 共 0」，
     * 那会让用户以为服务全挂了。
     */
    render: (row) => {
      const running = row.running
      const total = row.total
      if (typeof running !== 'number' || typeof total !== 'number') {
        return h('span', { class: 'arkade-cell-muted' }, '—')
      }
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

/**
 * 状态自动刷新：每 10 秒静默拉一次编排列表。
 *
 * 比容器列表的 5 秒慢一倍，因为编排状态变化本就低频
 * （启停整个编排是重操作，用户不会每秒都在做），没必要那么勤。
 *
 * 即使服务器离线也照样轮询：编排接口在离线时仍返回 200 且带 agent_online=false，
 * 这个字段本身就是要被刷新的——用户可能正等着「服务器恢复在线」的信号。
 */
usePolling(() => refresh(true), {
  name: 'stack-list',
  interval: 10000,
  // 首屏由 onMounted 的 refresh() 负责
  immediate: false
})

onMounted(() => {
  void refresh()
})
</script>

<template>
  <div class="stack-page">
    <page-header title="容器编排" :subtitle="subtitle">
      <template #actions>
        <n-button size="small" :disabled="!isOnline" :loading="loading" @click="refresh()">
          刷新
        </n-button>
        <!--
          新建编排一律显示：写权限是「用户组 × 该 Agent」授予的 manage，
          前端查不到自己在单台服务器上的档位，不能按角色猜。
          没有权限时后端会 403，提示由页面统一处理。
        -->
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
      未纳管项目：Agent 扫描到、但 Hub 里没有登记。
      这类项目无法通过本界面管理（界面数据来自 Hub），但**必须完整展示**：
      用户反馈「编排列表是空的但设备上明明有很多编排」，真实原因就是
      这些项目都在 Arkade 工作目录之外。只在顶部列几个标签、主表仍然空白，
      用户会认为功能坏了。所以这里给它们一张真正的表。
    -->
    <div v-if="loaded && unmanagedCount > 0" class="stack-page__unmanaged">
      <div class="stack-page__unmanaged-head">
        <span class="stack-page__unmanaged-title">服务器已有编排（未纳管）</span>
        <n-tag size="small" :bordered="false" type="info">{{ unmanagedCount }} 个</n-tag>
      </div>

      <!--
        说明为什么这些项目还在这里、以及自动接管是什么。
        必须讲清「不重启不重建」，否则用户会担心自动接管有副作用而不敢用。
      -->
      <p class="stack-page__unmanaged-desc">
        这些是服务器上已存在、但未登记在 Arkade 中的 compose 项目，通常是你直接使用
        <code>docker compose</code> 启动的。Arkade 会<strong>自动接管兼容项目，不重启或重建</strong>容器；
        暂不能接管的原因见下方。未自动接管的项目仍可手动导入。
      </p>

      <!--
        external_stacks 可用时画表（有状态/服务数/容器数，信息量大）；
        只有旧的 orphans 名字列表时退化成标签云，不能因此什么都不显示。
      -->
      <div v-if="externalStacks.length > 0" class="arkade-table-scroll">
        <n-data-table
          :columns="unmanagedColumns"
          :data="externalStacks"
          :row-key="(row: ExternalStack) => row.name"
          :bordered="false"
          :single-line="false"
          size="small"
          :scroll-x="showAutoAdoptColumn ? 760 : 520"
        />
      </div>
      <div v-else class="stack-page__orphans">
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
    </div>

    <div v-if="errorText !== ''" class="stack-page__alert stack-page__alert--row">
      <n-alert type="error" closable>{{ errorText }}</n-alert>
      <n-button size="small" quaternary @click="refresh()">重试</n-button>
    </div>

    <!-- 已纳管编排的标题：与未纳管表区分开，用户一眼看出哪些是能操作的 -->
    <div v-if="loaded && unmanagedCount > 0 && stacks.length > 0" class="stack-page__section">
      已纳管编排
      <n-tag size="small" :bordered="false">{{ managedCount }} 个</n-tag>
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
      <!--
        空态分两种情况，否则会给出误导性的「还没有编排」：
        服务器上明明有未纳管项目时，要说清「有，但不归 Arkade 管」。
      -->
      <empty-state
        v-else-if="loaded && unmanagedCount > 0"
        description="这台服务器上还没有被 Arkade 纳管的编排。上方列出的项目已存在于服务器，可以直接使用，但不受 Arkade 管理。"
      >
        <template #extra>
          <n-button size="small" type="primary" @click="goCreate">新建编排</n-button>
        </template>
      </empty-state>
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

    <!--
      导入纳管：把服务器上已有项目的配置复制进 Arkade 管理区。
      导入**不会启动、停止或重建任何容器**，弹窗里有明确说明。
    -->
    <stack-import-modal
      v-model:show="importShow"
      :agent-id="agentId ?? ''"
      :project-name="importProjectName"
      @imported="refresh()"
    />
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

/* 未纳管项目区块：视觉上与已纳管表格区分开，但不用告警色（不是错误） */
.stack-page__unmanaged {
  background: #f7f8fa;
  border: 1px solid var(--arkade-border-color);
  border-radius: 8px;
  padding: 14px 16px;
  margin-bottom: 20px;
  min-width: 0;
}

.stack-page__unmanaged-head {
  display: flex;
  align-items: center;
  gap: 8px;
  margin-bottom: 6px;
}

.stack-page__unmanaged-title {
  font-size: 14px;
  font-weight: 600;
  color: var(--arkade-text-primary);
}

.stack-page__unmanaged-desc {
  margin: 0 0 12px;
  font-size: 12px;
  line-height: 1.7;
  color: var(--arkade-text-tertiary);
}

/* 行内代码样式：项目名/命令用等宽字体，便于和技术术语区分 */
.stack-page__unmanaged-desc code {
  padding: 1px 4px;
  font-family: var(--arkade-mono-font);
  font-size: 11px;
  background: #eceef1;
  border-radius: 3px;
}

/* 已纳管区块标题：与未纳管表并列时出现，明确「这些才是能操作的」 */
.stack-page__section {
  display: flex;
  align-items: center;
  gap: 8px;
  margin-bottom: 8px;
  font-size: 14px;
  font-weight: 600;
  color: var(--arkade-text-primary);
}

/* 未纳管编排的名字标签：换行排列，长名字不会撑破卡片 */
.stack-page__orphans {
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
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
