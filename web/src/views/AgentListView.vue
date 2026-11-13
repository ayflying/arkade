<script setup lang="ts">
/**
 * 服务器（Agent）列表页
 *
 * 职责边界：
 * - 数据来自 useAgentsStore（全局缓存），并每 5 秒静默轮询一次，
 *   让「待接入」在用户部署完 Agent 后能自己变成「在线」；
 * - 行操作：查看容器、编辑、重置密钥、禁用/启用、删除；
 * - 「添加服务器」= 预创建一条 pending 记录 + 生成专属密钥，
 *   用户拿密钥去目标服务器上启动 Agent，连上后由 pending 变 online。
 *
 * 点击行进入容器页的取舍：离线服务器也允许进入（数据是 Hub 缓存的，仍可查看），
 * 只是页内的启停按钮会被禁用；如果连查看都不给，用户就完全看不到离线机器上的容器了。
 * pending 服务器例外：它从未连过，进去必然是空列表，所以行操作里禁用该入口。
 *
 * ## 密钥的持有位置
 * 密钥只在 `pendingEnrollment` 这一个 ref 里，且仅在弹窗打开期间有值。
 * 弹窗关闭（含组件卸载）立即置空。不写 Store、不写 storage、不进 URL。
 */
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import { NAlert, NButton, NDataTable, NTag } from 'naive-ui'
import AddAgentModal from '@/components/agent/AddAgentModal.vue'
import AgentEditModal from '@/components/agent/AgentEditModal.vue'
import EnrollmentTokenModal from '@/components/agent/EnrollmentTokenModal.vue'
import EmptyState from '@/components/common/EmptyState.vue'
import PageHeader from '@/components/common/PageHeader.vue'
import { agentRowProps, createAgentColumns } from '@/composables/agentColumns'
import { usePolling } from '@/composables/usePolling'
import { deleteAgent, disableAgent, enableAgent, resetAgentToken } from '@/api/agents'
import { useAgentsStore } from '@/stores/agents'
import type { Agent, CreateAgentResponse, Enrollment } from '@/types/agent'
import { ApiError } from '@/types/api'
import { agentDisplayName, isAgentDisabled } from '@/utils/agentDisplay'
import { confirmAction } from '@/utils/confirm'
import { toastError, toastSuccess, toastWarning } from '@/utils/feedback'

const store = useAgentsStore()
const router = useRouter()

/** 正在跳转的 agentId，用于按钮 loading，防止连点重复 push 路由 */
const enteringId = ref('')
/** 正在执行写操作的 agentId（删除/禁用/启用/重置密钥），用于行按钮 loading 与防连点 */
const busyId = ref('')

/** 编辑弹窗 */
const editShow = ref(false)
const editingAgent = ref<Agent | null>(null)

/** 添加服务器（预创建）弹窗 */
const addShow = ref(false)

/** 密钥展示弹窗 */
const tokenShow = ref(false)
/**
 * 一次性密钥。
 *
 * 唯一的持久位置就在这个 ref，且弹窗关闭时必须清空（见 handleTokenClosed）。
 * 也不要打印它——一旦进控制台就等于泄漏。
 */
const pendingEnrollment = ref<Enrollment | null>(null)
/** 密钥所属服务器的显示名，用于生成 .env 里的 ARKADE_AGENT_NAME */
const pendingAgentName = ref('')
/** 本次密钥是「重置」产生的吗（影响文案：旧密钥已失效） */
const tokenIsReset = ref(false)

const columns = createAgentColumns({
  onEnter: (agent: Agent) => enterAgent(agent),
  onEdit: (agent: Agent) => openEdit(agent),
  onResetToken: (agent: Agent) => void handleResetToken(agent),
  onToggleDisabled: (agent: Agent) => void handleToggleDisabled(agent),
  onDelete: (agent: Agent) => void handleDelete(agent),
  isBusy: (agentId: string) => enteringId.value === agentId || busyId.value === agentId
})

/** 顶部摘要：在线/待接入/禁用/总数，让用户一眼看到整体健康度 */
const summary = computed(() => {
  const total = store.agents.length
  const pending = store.agents.filter((a) => a.status === 'pending').length
  const disabled = store.agents.filter((a) => isAgentDisabled(a)).length
  const pendingText = pending > 0 ? `，待接入 ${pending} 台` : ''
  const disabledText = disabled > 0 ? `，已禁用 ${disabled} 台` : ''
  return `共 ${total} 台，在线 ${store.onlineCount} 台${pendingText}${disabledText}`
})

/**
 * 进入某台服务器的容器列表。
 *
 * pending 服务器直接拦住：它从未连过，容器列表必然是空的。
 * 光把行内按钮禁用不够——整行可点，用户点名称或空白处照样会进去，
 * 然后看着空列表以为这台机器上没有容器，其实只是还没接入。
 * 因此这里做最后一道拦截，并给出原因而不是静默无响应。
 */
function enterAgent(agent: Agent): void {
  if (enteringId.value !== '') return
  if (agent.status === 'pending') {
    toastWarning('这台服务器还没有接入过，暂时没有容器信息。请先在服务器上启动 Agent。')
    return
  }
  enteringId.value = agent.id
  void router.push({ name: 'agent-containers', params: { id: agent.id } }).finally(() => {
    enteringId.value = ''
  })
}

/** 打开编辑弹窗 */
function openEdit(agent: Agent): void {
  editingAgent.value = agent
  editShow.value = true
}

/**
 * 行点击：整行可点，但需要排除点击按钮/链接自身（它们已 stopPropagation）。
 *
 * pending 行不给 pointer 光标：它点进去只会被 enterAgent 拦下并提示，
 * 光标如果照样变成手型，用户会以为是页面没响应。
 * 用 `not-allowed` 把「这里点了没用」提前表达出来。
 */
function handleRowProps(row: Agent): Record<string, unknown> {
  const isPending = row.status === 'pending'
  return {
    ...agentRowProps(row),
    style: isPending ? 'cursor: not-allowed;' : 'cursor: pointer;',
    title: isPending ? '这台服务器还没有接入，暂时没有容器信息' : undefined,
    onClick: (e: MouseEvent) => {
      // 只在没有阻止冒泡的元素上触发行进入（按钮内部已 stopPropagation）
      const target = e.target
      if (target instanceof Element && target.closest('button, a')) return
      enterAgent(row)
    }
  }
}

/** 手动刷新（本期无 WebSocket 推送，靠用户主动刷新） */
async function handleRefresh(showToast: boolean): Promise<void> {
  const ok = await store.refresh()
  if (ok && showToast) {
    toastSuccess('服务器列表已刷新')
  }
}

/**
 * 删除服务器。
 *
 * 确认文案必须写清「仅移除纳管关系，不删服务器上的任何容器」——
 * 这条不写，用户根本不敢点（这是后端语义，也是最大的误解来源）。
 *
 * ## 关于「删除 vs 禁用」必须讲清楚
 *
 * 删除后会不会重新出现，**取决于这台服务器是用哪种方式接入的**，不能一概而论：
 *
 * - 用**专属密钥**接入的：删掉服务器后密钥即失效，Agent 连不进来，不会自己回来；
 * - 用**旧共享密钥**接入的：共享密钥仍然有效，Agent 断开后会重新注册，
 *   于是「删了又出现」。
 *
 * 前端目前拿不到每台服务器的接入方式（后端列表不返回该字段），
 * 因此这里只能用**条件式表述**把两种情况都说清楚，
 * 而不是像以前那样一律断言「会自动重新连接」——那对专属密钥的场景是错的，
 * 会让用户误以为删不掉，进而反复删除。
 *
 * 真想让某台服务器彻底不能再接入，无论哪种方式都该用**禁用**。
 *
 * 删除后还有两件事必须告诉用户：
 * 1. 后端返回的中文 message（明确说明未删除任何资产），直接展示，不自己重写；
 * 2. `orphan_stacks > 0` 时说明编排配置仍留在 Hub——
 *    注意这与编排列表里的 orphans 方向相反（那边是「Agent 有、Hub 没有」），
 *    不能混用「未纳管」这个词。
 */
async function handleDelete(agent: Agent): Promise<void> {
  if (agent.id === 'agent-local') {
    toastWarning('内置本机不能删除；如需关闭，请修改 Hub 的 local.enabled 配置并重启 Hub。')
    return
  }
  if (busyId.value !== '') return
  const name = agentDisplayName(agent)

  const confirmed = await confirmAction({
    title: `确认移除服务器「${name}」？`,
    content:
      '此操作仅从 Arkade 中移除纳管关系，不会删除服务器上的任何容器。\n\n' +
      '服务器上正在运行的容器、镜像、网络与数据卷都保持不变，' +
      'Agent 也会继续在服务器上运行。\n\n' +
      '⚠️ 关于删除后是否会重新出现：用专属密钥接入的服务器，密钥会随删除一起失效，' +
      '一般不会自己回来；用旧共享密钥接入的 Agent 断开后可能重新注册并再次出现在列表里。\n\n' +
      '如果你想让这台服务器不再接入 Arkade，请改用「禁用」——它对两种情况都有效。',
    positiveText: '移除纳管',
    negativeText: '取消',
    danger: true
  })
  if (!confirmed) return

  busyId.value = agent.id
  try {
    const res = await deleteAgent(agent.id)

    // 优先展示后端给的说明：它才是「到底删了什么」的权威表述
    toastSuccess(res.message || `已移除服务器「${name}」的纳管关系`)

    if (res.orphan_stacks > 0) {
      /*
       * 用词必须区分两种「孤儿」，方向正好相反：
       * - 编排列表里的 orphans = Agent 上有、Hub 里没有（可通过编排页看到并纳管）；
       * - 这里的 orphan_stacks = Hub 里有、但所属服务器已被移除。
       *   删完服务器后界面上已经没有任何入口能访问到这些编排，
       *   所以**不能说「可在编排列表中查看」**——那是骗人的。
       *
       * 也**不能断言「重新接入必然是新身份、旧配置一定恢复不了」**：
       * 这取决于接入方式——共享 token 且 data_dir 持久化时，旧 agent_id 会被复用，
       * 旧配置可能重新关联上；专属密钥接入则基本是新身份。
       * 前端拿不到每台服务器的接入方式，因此只陈述事实（配置仍保留），
       * 把「是否重新关联」交给用户人工确认，不做任何方向的承诺。
       */
      toastWarning(
        `该服务器名下有 ${res.orphan_stacks} 个编排配置仍保留在 Hub 中。` +
        '是否重新关联取决于接入身份：重新添加服务器不会自动认领这些配置，请人工确认。'
      )
    }

    await store.refresh()
  } catch (err) {
    toastError(err instanceof ApiError ? err.message : '移除服务器失败')
  } finally {
    busyId.value = ''
  }
}

/**
 * 禁用 / 启用切换。
 *
 * 禁用涉及「之后无法操作这台服务器」，属于有影响的操作，因此要确认；
 * 启用是恢复性操作，无需确认直接执行。
 */
async function handleToggleDisabled(agent: Agent): Promise<void> {
  if (busyId.value !== '') return
  const name = agentDisplayName(agent)
  const disabled = isAgentDisabled(agent)
  if (agent.id === 'agent-local' && !disabled) {
    toastWarning('内置本机不能在后台禁用；请修改 Hub 的 local.enabled 配置并重启 Hub。')
    return
  }

  if (!disabled) {
    const confirmed = await confirmAction({
      title: `确认禁用服务器「${name}」？`,
      content:
        '禁用后，该服务器的所有操作入口将不可用，无法在 Arkade 中管理其容器与编排。\n\n' +
        '不会删除任何容器，Agent 仍会保持运行；随时可以重新启用。',
      positiveText: '禁用',
      negativeText: '取消'
    })
    if (!confirmed) return
  }

  busyId.value = agent.id
  try {
    // 后端返回更新后的 Agent，直接替换本地那一行，不必重拉整表
    const updated = disabled ? await enableAgent(agent.id) : await disableAgent(agent.id)
    if (!store.replace(updated)) {
      // 本地没有这一行（异常情况），退回全量刷新保证界面正确
      await store.refresh()
    }
    toastSuccess(agent.id === 'agent-local'
      ? '已解除本机禁用；请确认 local.enabled 已开启并重启 Hub 后恢复本机执行。'
      : disabled ? `已启用服务器「${name}」` : `已禁用服务器「${name}」`)
  } catch (err) {
    toastError(err instanceof ApiError ? err.message : `${disabled ? '启用' : '禁用'}服务器失败`)
  } finally {
    busyId.value = ''
  }
}

/**
 * 编辑保存成功。
 *
 * PATCH 已经返回了更新后的完整对象，因此直接替换本地那一行即可，
 * 不重新拉整表——省一次请求，也避免列表「闪一下重新加载」的观感。
 * 若本地找不到该行（理论上不会），退化成一次全量刷新兜底。
 */
function handleSaved(updated: Agent): void {
  // replace 返回 false 说明本地没有这一行，此时靠刷新补齐
  if (!store.replace(updated)) {
    void handleRefresh(false)
  }
}

/**
 * 创建成功：插入列表 + 打开密钥弹窗。
 *
 * 顺序很重要：先把 agent 放进列表（用户能立刻看到「待接入」那一行），
 * 再把密钥交给弹窗。创建成功**不等于**服务器接入了，
 * 因此这里给的是「先提示、再展示密钥」的路径，而不是一个「创建成功」就完事的 toast。
 */
function handleCreated(payload: CreateAgentResponse): void {
  store.insert(payload.agent)
  pendingAgentName.value = agentDisplayName(payload.agent)
  tokenIsReset.value = false
  pendingEnrollment.value = payload.enrollment
  tokenShow.value = true
}

/**
 * 重置密钥。
 *
 * 这是有破坏性的操作：旧密钥立即失效，Hub 会断开该 Agent 的连接，
 * 那台机器会一直连不上，直到用户把新密钥更新到部署配置里。
 * 因此必须二次确认并写清后果——尤其对在线服务器，用户很可能不在那台机器旁边。
 */
async function handleResetToken(agent: Agent): Promise<void> {
  if (agent.id === 'agent-local') {
    toastWarning('内置本机不使用网络接入密钥，无需重置。')
    return
  }
  if (busyId.value !== '') return
  const name = agentDisplayName(agent)
  const online = agent.status === 'online'

  const confirmed = await confirmAction({
    title: `确认重置「${name}」的专属密钥？`,
    content:
      '旧密钥会立即失效' +
      (online
        ? '，这台服务器上的 Agent 会马上断开，并且在它更新配置之前无法重新连接。\n\n'
        : '。\n\n') +
      '新的密钥只显示一次，请准备好把它更新到那台机器的部署配置里。\n\n' +
      '如果只是想查看密钥：密钥在创建时只显示一次，无法再次查看，只能重置。',
    positiveText: '重置密钥',
    negativeText: '取消',
    danger: true
  })
  if (!confirmed) return

  busyId.value = agent.id
  try {
    const res = await resetAgentToken(agent.id)
    // 用后端返回的最新对象更新那一行（状态可能因断连变为 offline）
    if (!store.replace(res.agent)) await store.refresh()
    pendingAgentName.value = agentDisplayName(res.agent)
    tokenIsReset.value = true
    pendingEnrollment.value = res.enrollment
    tokenShow.value = true
    toastSuccess('已生成新的专属密钥')
  } catch (err) {
    toastError(err instanceof ApiError ? err.message : '重置密钥失败')
  } finally {
    busyId.value = ''
  }
}

/**
 * 密钥弹窗已关闭：立刻丢弃引用。
 *
 * 这是「不持久化」要求的最后一道闸。弹窗自己也清了一遍，
 * 但父组件持有可能让密钥在内存里活得更久，所以两处都清。
 */
function handleTokenClosed(): void {
  pendingEnrollment.value = null
  pendingAgentName.value = ''
  tokenIsReset.value = false
}

onMounted(() => {
  void handleRefresh(false)
})

/**
 * 状态自动刷新：每 5 秒静默拉一次服务器列表。
 *
 * 为什么必须有：预创建的服务器是 pending，用户部署完 Agent 后要靠**状态变成在线**
 * 才知道自己搞成了。之前列表只有手刷，用户得反复点「刷新」才看得到变化，
 * 很容易误判「部署没成功」。
 *
 * 间隔与容器列表一致（5 秒）：服务器状态变化不算高频，
 * 但「等它变绿」是用户此刻唯一关心的事，5 秒的等待感可以接受。
 *
 * 直接复用仓库里已有的 usePolling（含全局「自动刷新」开关、页面隐藏暂停、
 * 连续失败退避、卸载清理），**不另写 setInterval**——
 * 各页一套定时器早晚会出现「某个页面忘了清、切走后还在刷」的问题。
 */
usePolling(
  async () => {
    // 走静默路径：不显示 loading、不写 errorText，失败交给 usePolling 退避重试
    await store.refresh(true)
  },
  { name: 'agent-list', interval: 5000, immediate: false }
)

/**
 * 卸载时清空密钥。
 *
 * 用户可能在密钥还显示着的时候切走页面（前进/后退/点别的菜单），
 * 此时组件被销毁但内存里的 ref 仍可能被闭包引用住，
 * 显式置空是零成本保险。
 */
onUnmounted(() => {
  handleTokenClosed()
})
</script>

<template>
  <div class="agent-list-page">
    <page-header title="服务器" :subtitle="summary">
      <template #actions>
        <n-button size="small" type="primary" @click="addShow = true">添加服务器</n-button>
        <n-button size="small" :loading="store.loading" @click="handleRefresh(true)">刷新</n-button>
      </template>
    </page-header>

    <!-- 加载失败：保留旧数据，用内联提示 + 可重试 -->
    <div v-if="store.errorText !== ''" class="page-alert">
      <n-alert type="error" closable>
        {{ store.errorText }}
      </n-alert>
      <n-button size="small" quaternary class="page-alert__retry" @click="handleRefresh(false)">
        重试
      </n-button>
    </div>

    <!--
      表格容器：窄屏（手机/平板）下表格列宽总和会超出视口，
      由 NDataTable 的 scroll-x 在内部横向滚动，容器负责兜住宽度。
      class 里的 min-width: 0 见 global.css 的 .arkade-table-scroll 说明。
    -->
    <div class="table-card arkade-table-scroll">
      <n-data-table
        v-if="store.agents.length > 0 || store.loading"
        :columns="columns"
        :data="store.agents"
        :loading="store.loading"
        :row-key="(row: Agent) => row.id"
        :row-props="handleRowProps"
        :bordered="false"
        :single-line="false"
        size="medium"
        :scroll-x="1180"
      />
      <empty-state
        v-else-if="store.loaded"
        description="还没有任何服务器。点击「添加服务器」创建一台，然后在目标机器上用专属密钥启动 Agent。"
      >
        <template #extra>
          <n-button size="small" type="primary" @click="addShow = true">添加服务器</n-button>
        </template>
      </empty-state>
      <!-- 首次加载完成前不显示空态，避免「闪一下没有数据」 -->
      <div v-else class="table-skeleton">
        <n-tag :bordered="false" size="small">正在加载服务器列表…</n-tag>
      </div>
    </div>

    <agent-edit-modal v-model:show="editShow" :agent="editingAgent" @saved="handleSaved" />
    <add-agent-modal v-model:show="addShow" @created="handleCreated" />
    <!--
      密钥弹窗：v-if 而不是 v-show——关闭时真的销毁组件，
      让它的局部 token ref 随组件一起消失，不留残余引用。
    -->
    <enrollment-token-modal
      v-if="tokenShow"
      v-model:show="tokenShow"
      :enrollment="pendingEnrollment"
      :agent-name="pendingAgentName"
      :is-reset="tokenIsReset"
      @closed="handleTokenClosed"
    />
  </div>
</template>

<style scoped>
.page-alert {
  display: flex;
  align-items: center;
  gap: 8px;
  margin-bottom: 16px;
}

.page-alert :deep(.n-alert) {
  flex: 1;
}

.page-alert__retry {
  flex-shrink: 0;
}

.table-card {
  background: #fff;
  border-radius: 8px;
  padding: 8px 8px 0;
  box-shadow: 0 1px 2px rgba(31, 34, 37, 0.04);
}

.table-skeleton {
  padding: 48px 0;
  text-align: center;
}

/* 离线行整行置灰：仅做视觉弱化，拦截逻辑在 AgentRowActions 里 */
:deep(.agent-row--offline) {
  opacity: 0.55;
}

/* 名称列的可点击链接样式 */
:deep(.agent-name-link) {
  color: #2080f0;
  cursor: pointer;
  text-decoration: none;
}

:deep(.agent-name-link:hover) {
  text-decoration: underline;
}

:deep(.runtime-cell__version) {
  color: #8b9099;
  font-size: 12px;
}
</style>
