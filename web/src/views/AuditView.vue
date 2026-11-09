<script setup lang="ts">
/**
 * 操作审计页 /audit
 *
 * 列表：时间、操作人、动作、目标、结果、耗时、详情。
 * 筛选：时间范围、操作人、服务器、动作类型、结果。
 *
 * ## 分页方式
 * 接口用 **limit/offset**，页面只维护「第几页」，
 * 换算 offset=(page-1)*limit 在这里一次性完成，避免每个请求点各算一遍。
 *
 * ## 动作下拉来自接口字典
 * `GET /api/audit/actions` 提供 {action, label}。
 * 不在前端硬编码动作清单——动作由后端定义，硬编码必然随版本漂移，
 * 表现为「后端记录了但筛选里选不到」，用户会以为该动作没被记录。
 *
 * ## 关于后端接口尚未就绪
 * 接口返回 404 时明确提示「审计功能暂不可用」并保留筛选 UI，
 * 而不是白屏或假装没有数据——假装空列表会让用户以为「最近没有任何操作」。
 */
import { computed, h, onMounted, ref } from 'vue'
import {
  NAlert,
  NButton,
  NDataTable,
  NDatePicker,
  NInput,
  NPagination,
  NSelect
} from 'naive-ui'
import type { DataTableColumns, SelectOption } from 'naive-ui'
import EmptyState from '@/components/common/EmptyState.vue'
import PageHeader from '@/components/common/PageHeader.vue'
import { listAudit, listAuditActions } from '@/api/audit'
import { useAgentsStore } from '@/stores/agents'
import type { AuditActionOption, AuditEntry, AuditQuery } from '@/types/audit'
import { AUDIT_ACTION_USER_LOGIN, AUDIT_RESULT_FAILED, AUDIT_RESULT_SUCCESS } from '@/types/audit'
import { ApiError } from '@/types/api'
import { agentDisplayName } from '@/utils/agentDisplay'
import { formatAbsoluteTime, formatRelativeTime } from '@/utils/formatTime'

const agentsStore = useAgentsStore()

/** 每页条数：审计场景一屏能扫完的常见默认 */
const PAGE_SIZE = 20

const entries = ref<AuditEntry[]>([])
const total = ref(0)
const loading = ref(false)
const errorText = ref('')
/** 是否已成功加载过：区分「还没加载」与「确实没有记录」 */
const loaded = ref(false)
/** 后端尚未实现审计接口时的专门标记，用于给出更准确的提示 */
const notImplemented = ref(false)

/** 筛选条件 */
const page = ref(1)
const userInput = ref('')
const agentId = ref<string>('')
const actionValue = ref<string>('')
const resultValue = ref<string>('')
/** 时间范围（毫秒），NDatePicker 用的是毫秒；提交时转成 Unix 秒 */
const rangeMs = ref<[number, number] | null>(null)

/** 动作字典（来自后端接口） */
const actionOptions = ref<AuditActionOption[]>([])

/** 服务器下拉选项 */
const agentSelectOptions = computed<SelectOption[]>(() =>
  agentsStore.agents.map((a) => ({ label: agentDisplayName(a), value: a.id }))
)

/** 动作下拉选项 */
const actionSelectOptions = computed<SelectOption[]>(() =>
  actionOptions.value.map((a) => ({ label: a.label || a.action, value: a.action }))
)

/**
 * 结果下拉。
 *
 * 取值必须是 **success / failed**（后端校验这两个字面量，见 handlers_audit.go）。
 * 曾经写成 failure，会导致筛选请求被后端判为非法值。
 */
const RESULT_OPTIONS: SelectOption[] = [
  { label: '成功', value: AUDIT_RESULT_SUCCESS },
  { label: '失败', value: AUDIT_RESULT_FAILED }
]

/**
 * 组装当前查询条件。
 *
 * 关键换算：页码 → offset。接口不认 page，只认 offset。
 */
function currentQuery(): AuditQuery {
  const query: AuditQuery = {
    limit: PAGE_SIZE,
    offset: (page.value - 1) * PAGE_SIZE
  }

  if (rangeMs.value !== null) {
    // NDatePicker 返回毫秒，接口要 Unix 秒
    query.from = Math.floor(rangeMs.value[0] / 1000)
    query.to = Math.floor(rangeMs.value[1] / 1000)
  }
  if (userInput.value.trim() !== '') query.user_id = userInput.value.trim()
  if (agentId.value !== '') query.agent_id = agentId.value
  if (actionValue.value !== '') query.action = actionValue.value
  if (resultValue.value !== '') query.result = resultValue.value

  return query
}

/**
 * 拉取审计记录。
 *
 * 失败时保留旧数据只置错误文案，避免翻页失败把已经看到的记录清空。
 */
async function refresh(): Promise<void> {
  loading.value = true
  errorText.value = ''
  notImplemented.value = false
  try {
    const res = await listAudit(currentQuery())
    entries.value = Array.isArray(res.items) ? res.items : []
    total.value = typeof res.total === 'number' ? res.total : entries.value.length
    loaded.value = true
  } catch (err) {
    if (err instanceof ApiError && (err.status === 404 || err.status === 501)) {
      notImplemented.value = true
      errorText.value = '审计接口尚未就绪，功能将在后端完成后自动可用。'
    } else {
      errorText.value = err instanceof ApiError ? err.message : '加载审计记录失败'
    }
  } finally {
    loading.value = false
  }
}

/**
 * 拉取动作字典。
 *
 * 失败不阻断页面：下拉退化为「只能手输」，列表照常可用。
 * 字典拿不到不该让整个审计页不可用。
 */
async function loadActionOptions(): Promise<void> {
  try {
    const res = await listAuditActions()
    actionOptions.value = Array.isArray(res.actions) ? res.actions : []
  } catch {
    actionOptions.value = []
  }
}

/** 应用筛选：条件变了必须回到第 1 页，否则会停在一个不存在的页码上 */
function applyFilter(): void {
  page.value = 1
  void refresh()
}

/** 重置筛选 */
function resetFilter(): void {
  userInput.value = ''
  agentId.value = ''
  actionValue.value = ''
  resultValue.value = ''
  rangeMs.value = null
  page.value = 1
  void refresh()
}

/** 翻页 */
function handlePageChange(next: number): void {
  page.value = next
  void refresh()
}

/**
 * 结果 → 中文与颜色。
 *
 * ⚠️ 后端取值是 **failed**（不是 failure），
 * 拼错会让所有失败记录掉进兜底分支、按原始英文展示，
 * 还可能被误判成「未知结果」。两个常量放 types/audit.ts 里统一引用。
 */
const RESULT_META: Record<string, { label: string; color: string }> = {
  [AUDIT_RESULT_SUCCESS]: { label: '成功', color: '#18a058' },
  [AUDIT_RESULT_FAILED]: { label: '失败', color: '#d03050' }
}

function resultMeta(result: string): { label: string; color: string } {
  // 未知结果原样展示，不吞掉——后端新增取值时至少要看得见
  return RESULT_META[result] ?? { label: result || '—', color: '#8b9099' }
}

/**
 * 操作人展示文案。
 *
 * 三种情况必须区分，**不能一律显示「系统」**：
 * 1. 有用户名 → 直接显示。登录失败的记录里这个名称是用户**输入**的，
 *    账号可能不存在，所以下面第 2 条仍然重要（用于区分「查无此人」）；
 * 2. 无用户名但有 user_id → 「未知用户」：身份存在但名字快照缺失；
 * 3. 两者都空 → 「未认证」：未通过认证的请求，后端把两者一起清空了。
 *
 * 关键字：**空名不伪造身份**。把空名写成「系统」等于凭空断言「这是程序干的」，
 * 而实际上可能是某个人没登录就发的请求，事故复盘时会误导结论。
 */
function operatorText(row: AuditEntry): string {
  const name = (row.username ?? '').trim()
  if (name !== '') return name
  if ((row.user_id ?? '').trim() !== '') return '未知用户'
  return '未认证'
}

const columns = computed<DataTableColumns<AuditEntry>>(() => [
  {
    title: '时间',
    key: 'created_at',
    width: 130,
    render: (row) =>
      h('span', { title: formatAbsoluteTime(row.created_at) }, formatRelativeTime(row.created_at))
  },
  {
    title: '操作人',
    key: 'username',
    width: 110,
    render: (row) => {
      const text = operatorText(row)
      /*
       * 只有「登录失败」这条记录的 username 是用户**输入**的名称、账号可能不存在
       * （见 handlers_auth.go 的 writeLoginAudit：失败时传入的是用户输入值）。
       *
       * 其余操作（容器启停等）即便失败，请求也已经过认证，
       * 其 username 来自已认证账号，是真实存在的用户——
       * 对它加「可能是输入的名称」提示反而是错误信息。
       *
       * 因此判定必须同时满足 action === 'user.login' && result === 'failed'，
       * 只判失败是不够的。
       */
      const mightNotExist =
        row.action === AUDIT_ACTION_USER_LOGIN && row.result === AUDIT_RESULT_FAILED
      return h(
        'span',
        mightNotExist ? { title: '该名称来自本次操作的输入，可能并非已存在的账号' } : {},
        text
      )
    }
  },
  {
    title: '动作',
    key: 'action',
    minWidth: 130,
    render: (row) => row.action_label || row.action
  },
  {
    title: '目标',
    key: 'target',
    minWidth: 190,
    ellipsis: { tooltip: true },
    render: (row) => {
      // 目标带上服务器名，跨服务器场景下才能分辨同名容器
      const parts = [row.agent_name, row.target].filter((v) => v !== '' && v !== undefined)
      return parts.length > 0 ? parts.join(' / ') : '—'
    }
  },
  {
    title: '结果',
    key: 'result',
    width: 90,
    render: (row) => {
      const meta = resultMeta(row.result)
      return h('span', { style: `color:${meta.color}` }, meta.label)
    }
  },
  {
    title: '耗时',
    key: 'duration_ms',
    width: 90,
    render: (row) => (typeof row.duration_ms === 'number' ? `${row.duration_ms} ms` : '—')
  },
  {
    title: '详情',
    key: 'detail',
    minWidth: 180,
    ellipsis: { tooltip: true },
    // 失败原因是最有价值的信息；成功时优先显示 detail，都没有才显示 ——
    render: (row) => row.error || row.detail || '—'
  }
])

onMounted(() => {
  // 服务器下拉需要服务器列表；直接刷新本页时 store 可能为空
  if (agentsStore.agents.length === 0) void agentsStore.refresh()
  void loadActionOptions()
  void refresh()
})
</script>

<template>
  <div class="audit-page">
    <page-header
      title="操作审计"
      :subtitle="total > 0 ? `共 ${total} 条记录` : '记录所有对服务器与资源的操作'"
    >
      <template #actions>
        <n-button size="small" :loading="loading" @click="refresh()">刷新</n-button>
      </template>
    </page-header>

    <!-- 筛选区：窄屏自动换行 -->
    <div class="audit-filters">
      <n-date-picker
        v-model:value="rangeMs"
        type="datetimerange"
        size="small"
        clearable
        class="audit-filters__range"
        start-placeholder="开始时间"
        end-placeholder="结束时间"
      />
      <n-input
        v-model:value="userInput"
        size="small"
        clearable
        placeholder="操作人"
        class="audit-filters__input"
      />
      <n-select
        v-model:value="agentId"
        size="small"
        clearable
        placeholder="服务器"
        :options="agentSelectOptions"
        class="audit-filters__input"
      />
      <n-select
        v-model:value="actionValue"
        size="small"
        clearable
        filterable
        placeholder="动作类型"
        :options="actionSelectOptions"
        class="audit-filters__input"
      />
      <n-select
        v-model:value="resultValue"
        size="small"
        clearable
        placeholder="结果"
        :options="RESULT_OPTIONS"
        class="audit-filters__result"
      />
      <n-button size="small" type="primary" @click="applyFilter">查询</n-button>
      <n-button size="small" @click="resetFilter">重置</n-button>
    </div>

    <div v-if="errorText !== ''" class="audit-page__row">
      <n-alert :type="notImplemented ? 'info' : 'error'" closable>{{ errorText }}</n-alert>
      <n-button v-if="!notImplemented" size="small" quaternary @click="refresh()">重试</n-button>
    </div>

    <div class="table-card arkade-table-scroll">
      <n-data-table
        v-if="entries.length > 0 || loading"
        :columns="columns"
        :data="entries"
        :loading="loading"
        :row-key="(row: AuditEntry) => row.id"
        :bordered="false"
        :single-line="false"
        size="medium"
        :scroll-x="1020"
      />
      <empty-state
        v-else-if="loaded"
        description="没有符合条件的操作记录。调整筛选条件后再试。"
      />
      <div v-else-if="!notImplemented" class="table-skeleton">
        <span class="table-skeleton__text">正在加载审计记录…</span>
      </div>
    </div>

    <!-- 分页：只有一页时不显示，避免占位 -->
    <div v-if="total > PAGE_SIZE" class="audit-page__pager">
      <n-pagination
        :page="page"
        :page-size="PAGE_SIZE"
        :item-count="total"
        size="small"
        @update:page="handlePageChange"
      />
    </div>
  </div>
</template>

<style scoped>
.audit-filters {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 8px;
  margin-bottom: 16px;
}

/* 时间范围最宽；其余输入框等宽。用 max-width 保证能被压缩 */
.audit-filters__range {
  width: 320px;
  max-width: 100%;
}

.audit-filters__input {
  width: 150px;
  max-width: 100%;
}

/* 结果只有两个选项，不需要那么宽 */
.audit-filters__result {
  width: 110px;
  max-width: 100%;
}

@media (max-width: 767.98px) {
  .audit-filters__range,
  .audit-filters__input,
  .audit-filters__result {
    width: 100%;
  }
}

.audit-page__row {
  display: flex;
  align-items: center;
  gap: 8px;
  margin-bottom: 16px;
}

.audit-page__row :deep(.n-alert) {
  flex: 1;
}

.table-card {
  background: #fff;
  border-radius: 8px;
  padding: 4px;
}

.table-skeleton {
  padding: 48px 0;
  text-align: center;
}

.table-skeleton__text {
  font-size: 13px;
  color: #8b9099;
}

.audit-page__pager {
  display: flex;
  justify-content: flex-end;
  margin-top: 16px;
}
</style>
