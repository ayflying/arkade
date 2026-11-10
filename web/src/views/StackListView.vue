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
import { computed, onMounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import { NAlert, NButton, NTag } from 'naive-ui'
import EmptyState from '@/components/common/EmptyState.vue'
import PageHeader from '@/components/common/PageHeader.vue'
import StackUnifiedTable from '@/components/stack/StackUnifiedTable.vue'
import StackImportModal from '@/components/stack/StackImportModal.vue'
const importShow = ref(false)
function openImport() { importShow.value = true }
import { listStacks } from '@/api/stacks'
import { useCurrentAgent } from '@/composables/useCurrentAgent'
import { usePolling } from '@/composables/usePolling'
import type { ExternalStack, StackSummary } from '@/types/stack'
import type { StackListRow } from '@/utils/stackListModel'
import { buildStackRows, computeStats } from '@/utils/stackListModel'
import { ApiError } from '@/types/api'

const router = useRouter()
const { agentId, isOnline: storeOnline, displayName } = useCurrentAgent()

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
 * 统一表的行数据：已纳管与外部项目按名称合并。
 *
 * 合并规则（同名以已纳管为准、不修改入参、运行中优先排序）都在
 * utils/stackListModel 里，那里有独立测试；页面只消费结果。
 */
const rows = computed<StackListRow[]>(() =>
  buildStackRows(stacks.value, externalStacks.value)
)

/** 统一后的统计：顶部副标题用 */
const stats = computed(() => computeStats(rows.value))

const subtitle = computed(() => {
  if (!loaded.value) return '正在加载编排…'
  const driftText = driftCount.value > 0 ? `，${driftCount.value} 个存在配置漂移` : ''
  // 未登记单独说明：它们不在 Arkade 的管理范围内，混进总数会误导
  const unregText = stats.value.unregistered > 0 ? `，${stats.value.unregistered} 个未登记` : ''
  const unsyncText = stats.value.unsynced > 0 ? `，${stats.value.unsynced} 个未同步` : ''
  return `${displayName.value} · ${stats.value.total} 个编排（运行中 ${stats.value.running} 个）${unregText}${unsyncText}${driftText}`
})

/**
 * 是否需要显示「自动接管」列。
 *
 * 旧 Hub 不返回该字段，所有行都是 unknown。此时**整列不显示**，
 * 否则会多出一列全是空白，用户会以为功能坏了。
 * 任一行动起来才显示整列，保证表格结构稳定（不会逐行时有时无）。
 */
/** 进入详情：所有行都走同一个详情页（统一的编排入口） */
function goDetail(name: string): void {
  if (agentId.value === null) return
  void router.push({
    name: 'agent-stack-detail',
    params: { id: agentId.value, name }
  })
}

/** 去新建页 */
function goCreate(): void {  if (agentId.value === null) return
  void router.push({ name: 'agent-stack-create', params: { id: agentId.value } })
}

/**
 * 最后一列展示同步信息。
 *
 * 用 updated_at（Hub 里的更新时间）为主，synced_at（最后一次成功下发到服务器）
 * 放在 tooltip 里。两者差异大往往意味着「改了但没同步成功」，值得让用户能查到。
 */

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
        <n-button size="small" @click="openImport">从配置导入</n-button>
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
      统一编排表：已纳管与服务器原目录里的项目合成一张，按名称对齐。
      以前拆成上下两张表，用户要在两个地方找同一个项目；
      同名项目还会重复出现。合并规则见 utils/stackListModel。
    -->
    <div class="table-card arkade-table-scroll">
      <stack-unified-table
        v-if="rows.length > 0 || loading"
        :rows="rows"
        :loading="loading"
        @open="goDetail"
      />
      <empty-state v-else-if="loaded" description="这台服务器上还没有容器编排。新建一个编排来批量管理多个容器。">
        <template #extra>
          <n-button size="small" type="primary" @click="goCreate">新建编排</n-button>
        </template>
      </empty-state>
      <div v-else class="table-skeleton">
        <n-tag :bordered="false" size="small">正在加载编排列表…</n-tag>
      </div>
    </div>
  </div>
  <stack-import-modal v-if="agentId" v-model:show="importShow" :agent-id="agentId" project-name="" @imported="refresh()" />
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
