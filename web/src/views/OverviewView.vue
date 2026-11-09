<script setup lang="ts">
/**
 * 全局概览页 /overview（登录后的首页）
 *
 * 一屏回答三个问题：
 * 1. **我总共有多少东西**（统计卡片）；
 * 2. **哪里出问题了**（异常提示区：离线服务器 / 配置漂移 / 未纳管编排）；
 * 3. **每台服务器怎么样**（服务器缩略列表，点击进去）。
 *
 * ## 数据来源
 * 没有专属接口，由 api/overview.ts 聚合已有接口。聚合层负责并发与容错，
 * 本页只负责渲染与「不假装是全量」的提示。
 *
 * ## 为什么异常区排在统计卡片下面、服务器列表上面
 * 用户打开概览的第一诉求通常是「有没有事」，而不是「我有几个镜像」。
 * 统计卡片给个宏观印象就够，真正需要用户动手的信息必须在折叠线以上。
 */
import { computed, h, onMounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import { NAlert, NButton, NDataTable, NSkeleton, NTag } from 'naive-ui'
import type { DataTableColumns } from 'naive-ui'
import AgentStatusTag from '@/components/agent/AgentStatusTag.vue'
import EmptyState from '@/components/common/EmptyState.vue'
import PageHeader from '@/components/common/PageHeader.vue'
import RelativeTime from '@/components/common/RelativeTime.vue'
import StatCard from '@/components/overview/StatCard.vue'
import { OVERVIEW_AGENT_LIMIT, displayNameOf, loadOverview } from '@/api/overview'
import { usePolling } from '@/composables/usePolling'
import type { AgentOverview, OverviewData } from '@/types/overview'
import { ApiError } from '@/types/api'
import { isAgentDisabled } from '@/utils/agentDisplay'

const router = useRouter()

const data = ref<OverviewData | null>(null)
const loading = ref(false)
const errorText = ref('')

/**
 * 拉取概览数据。
 *
 * @param silent 静默刷新：自动轮询用。
 *               - 不显示骨架：概览有 6 张卡片 + 一张表，骨架闪烁非常刺眼；
 *               - 失败不写 errorText：偶发失败不该把整页替换成错误提示，
 *                 连续失败交给 usePolling 的退避处理。
 *
 * 失败时保留旧数据：概览是「随便看一眼」的场景，
 * 因为一次刷新失败就清空成空白页，比显示几秒前的数据糟糕得多。
 */
async function refresh(silent = false): Promise<void> {
  if (!silent) loading.value = true
  if (!silent) errorText.value = ''
  try {
    data.value = await loadOverview()
    if (silent) errorText.value = ''
  } catch (err) {
    if (silent) {
      // 抛给 usePolling 计入连续失败
      throw err
    }
    errorText.value = err instanceof ApiError ? err.message : '加载概览数据失败'
  } finally {
    if (!silent) loading.value = false
  }
}

const totals = computed(() => data.value?.totals)
const problems = computed(() => data.value?.problems)

/** 异常总数：0 时显示「一切正常」，>0 时展开异常区 */
const problemCount = computed(() => {
  const p = problems.value
  if (!p) return 0
  return p.offlineAgents.length + p.driftedStacks.length + p.orphanStacks.length
})

/** 是否有任何服务器（决定是否显示空态引导） */
const hasAgents = computed(() => (totals.value?.agents ?? 0) > 0)

/** 进入某台服务器的容器页 */
function enterAgent(agentId: string): void {
  void router.push({ name: 'agent-containers', params: { id: agentId } })
}

/** 进入某台服务器的编排页（漂移/未纳管项要跳到能处理它的地方） */
function enterStacks(agentId: string): void {
  void router.push({ name: 'agent-stacks', params: { id: agentId } })
}

const agentColumns = computed<DataTableColumns<AgentOverview>>(() => [
  {
    title: '服务器',
    key: 'name',
    minWidth: 180,
    render: (row) =>
      h(
        'a',
        { class: 'arkade-link', onClick: () => enterAgent(row.agent.id) },
        displayNameOf(row.agent)
      )
  },
  {
    title: '状态',
    key: 'status',
    width: 110,
    render: (row) =>
      // 禁用优先于在线状态展示：禁用是管理员主动意图，比「在线/离线」更需要被看到
      isAgentDisabled(row.agent)
        ? h(NTag, { size: 'small', bordered: false }, { default: () => '已禁用' })
        : h(AgentStatusTag, { status: row.agent.status })
  },
  {
    title: '容器',
    key: 'containers',
    width: 120,
    render: (row) =>
      h(
        'span',
        { style: 'font-variant-numeric: tabular-nums' },
        `${row.counts.runningContainers} / ${row.counts.containers}`
      )
  },
  {
    title: '编排',
    key: 'stacks',
    width: 90,
    render: (row) => h('span', { style: 'font-variant-numeric: tabular-nums' }, row.counts.stacks)
  },
  {
    title: '镜像',
    key: 'images',
    width: 80,
    render: (row) => h('span', { style: 'font-variant-numeric: tabular-nums' }, row.counts.images)
  },
  {
    title: '最后心跳',
    key: 'last_seen',
    width: 130,
    render: (row) => h(RelativeTime, { seconds: row.agent.last_seen, prefix: '最后心跳' })
  },
  {
    title: '操作',
    key: 'actions',
    width: 100,
    fixed: 'right',
    render: (row) =>
      h(
        NButton,
        { size: 'tiny', quaternary: true, onClick: () => enterAgent(row.agent.id) },
        { default: () => '查看' }
      )
  }
])

/**
 * 统计自动刷新：每 30 秒静默拉一次。
 *
 * 概览是**聚合页**，一次刷新要为每台服务器发 5 个请求；
 * 间隔必须明显长于单台页面（容器 5 秒 / 编排 10 秒），否则服务器一多就是可观的负载。
 * 30 秒对「宏观数字有没有变化」这个用途完全够用。
 *
 * 用 interval 函数返回固定值而不是常量，是为了将来若要做
 * 「服务器多时自动放慢」时只改这里一处。
 */
usePolling(() => refresh(true), {
  name: 'overview',
  interval: 30000,
  // 首屏由 onMounted 的 refresh() 负责，避免重复聚合一次
  immediate: false
})

onMounted(() => {
  void refresh()
})
</script>

<template>
  <div class="overview-page">
    <page-header title="概览" subtitle="查看所有服务器的整体运行状况与需要处理的问题">
      <template #actions>
        <n-button size="small" :loading="loading" @click="refresh()">刷新</n-button>
      </template>
    </page-header>

    <div v-if="errorText !== ''" class="overview-page__row">
      <n-alert type="error" closable>{{ errorText }}</n-alert>
      <n-button size="small" quaternary @click="refresh()">重试</n-button>
    </div>

    <!-- 首次加载：用骨架屏而不是转圈，避免页面高度突然跳变 -->
    <template v-if="data === null && loading">
      <div class="stat-grid">
        <div v-for="i in 6" :key="i" class="stat-skeleton">
          <n-skeleton text :repeat="2" />
        </div>
      </div>
    </template>

    <!-- 空态：一台服务器都没有 → 引导部署 Agent（这是唯一能接入服务器的方式） -->
    <template v-else-if="data !== null && !hasAgents">
      <empty-state description="还没有任何服务器接入 Arkade。部署一个 Agent 后，它所在的服务器就会自动出现在这里。">
        <template #extra>
          <n-button size="small" type="primary" @click="router.push({ name: 'agents' })">
            去服务器页查看部署说明
          </n-button>
        </template>
      </empty-state>
    </template>

    <template v-else-if="data !== null">
      <!--
        统计范围提示：服务器超过上限时只统计前 N 台，必须明说，
        否则用户会以为卡片上是全量数字。
      -->
      <div v-if="data.truncated" class="overview-page__row">
        <n-alert type="info" :bordered="false">
          服务器较多，为避免拖慢页面，下方统计仅包含<strong>前 {{ OVERVIEW_AGENT_LIMIT }} 台</strong>服务器的资源数量（共
          {{ data.totalAgents }} 台）。离线服务器列表不受此限制。
        </n-alert>
      </div>

      <!-- 一、统计卡片 -->
      <div class="stat-grid">
        <stat-card
          label="服务器"
          :value="data.totals.agents"
          unit="台"
          :hint="`在线 ${data.totals.onlineAgents} 台`"
          :tone="data.totals.onlineAgents === data.totals.agents ? 'good' : 'normal'"
        />
        <stat-card
          label="容器"
          :value="data.totals.containers"
          unit="个"
          :hint="`运行中 ${data.totals.runningContainers} 个`"
        />
        <stat-card label="容器编排" :value="data.totals.stacks" unit="个" />
        <stat-card label="本地镜像" :value="data.totals.images" unit="个" />
        <stat-card label="网络" :value="data.totals.networks" unit="个" />
        <stat-card label="存储卷" :value="data.totals.volumes" unit="个" />
      </div>

      <!--
        二、异常提示区
        三类问题集中展示。数量为 0 时整个区都不渲染——一个「0 个问题」的空盒子
        只会占用首屏空间，反而让真正的问题不显眼。
      -->
      <div v-if="problemCount > 0" class="problem-area">
        <!-- 离线服务器 -->
        <n-alert
          v-if="data.problems.offlineAgents.length > 0"
          type="warning"
          :bordered="false"
          title="有服务器离线"
          class="problem-area__item"
        >
          <div class="problem-area__text">
            以下服务器的 Agent 已断开连接，其上的容器操作将无法执行：
          </div>
          <div class="problem-area__tags">
            <n-tag
              v-for="agent in data.problems.offlineAgents"
              :key="agent.id"
              size="small"
              :bordered="false"
              type="warning"
              class="problem-area__tag"
              @click="enterAgent(agent.id)"
            >
              {{ displayNameOf(agent) }}
            </n-tag>
          </div>
        </n-alert>

        <!-- 配置漂移 -->
        <n-alert
          v-if="data.problems.driftedStacks.length > 0"
          type="warning"
          :bordered="false"
          title="编排配置发生漂移"
          class="problem-area__item"
        >
          <div class="problem-area__text">
            以下编排的 compose.yml 与 Hub 记录不一致，通常是有人直接在服务器上改了文件。
            在 Hub 里编辑保存会覆盖那些改动，处理前请先确认线上真实配置：
          </div>
          <div class="problem-area__tags">
            <n-tag
              v-for="item in data.problems.driftedStacks"
              :key="`${item.agentId}/${item.stackName}`"
              size="small"
              :bordered="false"
              type="warning"
              class="problem-area__tag"
              @click="enterStacks(item.agentId)"
            >
              {{ item.agentName }} / {{ item.stackName }}
            </n-tag>
          </div>
        </n-alert>

        <!-- 未纳管编排 -->
        <n-alert
          v-if="data.problems.orphanStacks.length > 0"
          type="info"
          :bordered="false"
          title="存在未纳管的编排"
          class="problem-area__item"
        >
          <div class="problem-area__text">
            以下编排存在于服务器上，但未登记在 Hub 中，因此无法通过 Arkade 管理：
          </div>
          <div class="problem-area__tags">
            <n-tag
              v-for="item in data.problems.orphanStacks"
              :key="`${item.agentId}/${item.stackName}`"
              size="small"
              :bordered="false"
              type="info"
              class="problem-area__tag"
              @click="enterStacks(item.agentId)"
            >
              {{ item.agentName }} / {{ item.stackName }}
            </n-tag>
          </div>
        </n-alert>
      </div>

      <!-- 全部正常时给一句明确结论，而不是让用户自己去确认「是不是没加载出来」 -->
      <div v-else-if="!data.truncated || data.failures.length === 0" class="problem-area">
        <n-alert type="success" :bordered="false" class="problem-area__item">
          未发现离线服务器、配置漂移或未纳管编排。
        </n-alert>
      </div>

      <!--
        三、部分数据未统计到
        必须显式列出，否则用户会拿一张「少了几台」的统计表当全量用。
      -->
      <div v-if="data.failures.length > 0" class="overview-page__row">
        <n-alert type="warning" :bordered="false" title="部分数据未能统计">
          以下服务器的数据拉取失败，上方统计数字不包含它们：
          <ul class="failure-list">
            <li v-for="(item, index) in data.failures" :key="index">
              {{ item.agentName }} · {{ item.resource }}：{{ item.message }}
            </li>
          </ul>
        </n-alert>
      </div>

      <!-- 四、服务器缩略列表 -->
      <div class="section-title">服务器状态</div>
      <div class="table-card arkade-table-scroll">
        <n-data-table
          :columns="agentColumns"
          :data="data.agents"
          :loading="loading"
          :row-key="(row: AgentOverview) => row.agent.id"
          :bordered="false"
          :single-line="false"
          size="medium"
          :scroll-x="900"
        />
      </div>
    </template>
  </div>
</template>

<style scoped>
/* 统计卡片栅格：桌面 6 列，窄屏自动降列，手机 2 列 */
.stat-grid {
  display: grid;
  grid-template-columns: repeat(6, minmax(0, 1fr));
  gap: 12px;
  margin-bottom: 16px;
}

@media (max-width: 1200px) {
  .stat-grid {
    grid-template-columns: repeat(3, minmax(0, 1fr));
  }
}

@media (max-width: 640px) {
  .stat-grid {
    grid-template-columns: repeat(2, minmax(0, 1fr));
  }
}

.stat-skeleton {
  background: #fff;
  border-radius: 8px;
  padding: 16px;
}

/* 通用「提示条 + 按钮/内容并排」容器 */
.overview-page__row {
  display: flex;
  align-items: flex-start;
  gap: 8px;
  margin-bottom: 16px;
}

.overview-page__row :deep(.n-alert) {
  flex: 1;
  min-width: 0;
}

.problem-area {
  display: flex;
  flex-direction: column;
  gap: 12px;
  margin-bottom: 16px;
}

.problem-area__item {
  /* Naive UI 的 alert 内部有默认宽度行为，这里确保在窄屏下可以换行收缩 */
  min-width: 0;
}

.problem-area__text {
  line-height: 1.6;
}

/* 异常项标签：可点击跳转到对应页面，因此给出明确的可点反馈 */
.problem-area__tags {
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
  margin-top: 8px;
}

.problem-area__tag {
  cursor: pointer;
  font-family: var(--arkade-mono-font);
}

.problem-area__tag:hover {
  opacity: 0.8;
  text-decoration: underline;
}

.failure-list {
  margin: 6px 0 0;
  padding-left: 18px;
  line-height: 1.7;
}

.section-title {
  font-size: 14px;
  font-weight: 600;
  color: #1f2225;
  margin-bottom: 8px;
}

.table-card {
  background: #fff;
  border-radius: 8px;
  padding: 4px;
}
</style>
