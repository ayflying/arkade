<script setup lang="ts">
/**
 * 外部（未纳管）compose 项目详情
 *
 * 路由：/agents/:id/stacks/external/:name
 *
 * 独立成页而不扩展 StackDetailView：已纳管编排的配置在 Hub、状态由 Hub 下发；
 * 外部项目的配置在服务器目录里，Hub 只能读、写只落在草稿上。
 * 两者语义、权限、动作都不同，合并会让每个分支都要判断「这是哪种」。
 *
 * 权限：详情 / 容器 / 日志要 **view**；配置读取 / 草稿 / 登记 / 复制要 **manage**。
 * 「进得来但看不到配置」是正常状态，应作权限提示而非故障。
 */
import { computed, onMounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { NAlert, NButton, NSpin, NTabPane, NTabs } from 'naive-ui'
import PageHeader from '@/components/common/PageHeader.vue'
import ExternalStackOverview from '@/components/external/ExternalStackOverview.vue'
import ExternalStackContainers from '@/components/external/ExternalStackContainers.vue'
import ExternalStackConfigPanel from '@/components/external/ExternalStackConfigPanel.vue'
import ExternalStackCopyModal from '@/components/external/ExternalStackCopyModal.vue'
import ExternalStackLogs from '@/components/external/ExternalStackLogs.vue'
import { useExternalStack } from '@/composables/useExternalStack'
import { useCurrentAgent } from '@/composables/useCurrentAgent'
import { listAgents } from '@/api/agents'
import { applyExternalStack, copyExternalConfig, previewExternalApply } from '@/api/externalStacks'
import { ApiError } from '@/types/api'
import type { Agent } from '@/types/agent'
import type { ApplyPreviewResponse } from '@/types/externalStack'
import { toastError, toastSuccess, toastWarning } from '@/utils/feedback'

const route = useRoute()
const router = useRouter()
const { agentId, agent, displayName, isOnline } = useCurrentAgent()

/** 服务器直连地址，供端口链接使用；为空表示未配置，此时端口一律不给链接 */
const directAddress = computed<string>(() => agent.value?.direct_address ?? '')

/** 取第一个字符串参数（params/query 可能是数组） */
function firstParam(value: unknown): string {
  const v = Array.isArray(value) ? value[0] : value
  return typeof v === 'string' ? v : ''
}

const stackName = computed<string>(() => firstParam(route.params.name))

const store = useExternalStack({
  agentId: () => agentId.value ?? '',
  name: () => stackName.value
})

/** 当前 tab */
const activeTab = ref<'overview' | 'config' | 'logs'>('overview')

/** 登记到 Arkade */
const applying = ref(false)
const preview = ref<ApplyPreviewResponse | null>(null)
const applyError = ref('')
/*
 * 登记时发现同名编排已被自动登记过。这不是失败：Hub 里已有这个项目，
 * 用户该去已纳管详情页看，而不是在这里反复点，所以给直达链接而非一句错误。
 */
const alreadyManaged = ref(false)

/** 复制配置 */
const copyShow = ref(false)
const copying = ref(false)
const copyError = ref('')
const agentList = ref<Agent[]>([])

const pageTitle = computed(() =>
  stackName.value === '' ? '外部编排' : `外部编排：${stackName.value}`
)

const subtitle = computed(() => {
  const p = store.project.value
  if (p === null) return `服务器 ${displayName.value}`
  const count = p.services === null ? '未知' : `${String(p.services.length)} 个服务`
  return `服务器 ${displayName.value} · ${count}`
})
/** 保存草稿 */
async function handleSaveDraft(content: string, expectedVersion: number): Promise<void> {
  const ok = await store.saveDraft(content, expectedVersion)
  if (ok) {
    toastSuccess('草稿已保存到 Hub')
  } else {
    toastError(store.draftError.value === '' ? '草稿保存失败' : store.draftError.value)
  }
}

/**
 * 登记到 Arkade。
 *
 * 先预检再执行（含 build 等写法会被拦下）。界面叫「登记」而不是「应用」，
 * 因为这个动作**不启动、不重启**任何容器。进入时快照 id/名字，
 * 预检与登记之间的网络往返期间切换项目不会登记错对象。
 */
async function handleApply(content: string): Promise<void> {
  const targetAgentId = agentId.value
  const targetName = stackName.value
  if (targetAgentId === null || targetName === '' || applying.value) return
  applying.value = true
  applyError.value = ''
  preview.value = null
  alreadyManaged.value = false
  try {
    const pre = await previewExternalApply(targetAgentId, targetName, { content })
    preview.value = pre
    if (!pre.can_import) {
      // 不弹 toast，就地展示 blockers 更有用
      return
    }
    const res = await applyExternalStack(targetAgentId, targetName, {
      content,
      expected_content_hash: pre.config_hash,
      confirmation: true
    })
    if (res.sync_ok) {
      toastSuccess('已登记到 Arkade')
    } else {
      toastWarning(res.sync_hint ?? '已登记到 Hub，但尚未同步到服务器。')
    }
    void store.loadDetail()
  } catch (err) {
  /*
   * 409 有多种成因：同名已在 Hub、校验冲突、运行时冲突。前端无法区分，
   * 一律宣称「已登记」对后两种就是假消息。做法：原样展示后端文案，
   * 同时给「查看已纳管项目」入口让用户自己确认。
   */
    if (err instanceof ApiError && err.status === 409) {
      applyError.value = err.message === '' ? '登记冲突，请查看已纳管项目确认。' : err.message
      alreadyManaged.value = true
    } else {
      applyError.value = err instanceof Error ? err.message : '登记失败'
    }
  } finally {
    applying.value = false
  }
}

/** 去已纳管的同项目详情页 */
function goManagedDetail(): void {
  if (agentId.value === null) return
  void router.push({
    name: 'agent-stack-detail',
    params: { id: agentId.value, name: stackName.value }
  })
}

/**
 * 编辑器当前内容（可能是草稿，也可能是用户未保存的改动）。
 * 复制与登记都必须用它，**不能用 rawContent**——那会静静丢掉用户的修改。
 */
const editorContent = ref('')

/** 打开复制弹窗（需要先拿到服务器列表） */
async function openCopy(): Promise<void> {
  copyError.value = ''
  try {
    agentList.value = await listAgents()
  } catch (err) {
    copyError.value = err instanceof Error ? err.message : '服务器列表加载失败'
  }
  copyShow.value = true
}

/**
 * 执行复制。
 *
 * 进函数就快照源 id/名字/内容：await 期间用户可能切到别的项目，
 * 用实时响应式值会**复制错的项目**。
 */
async function handleCopy(payload: { targetAgentId: string; targetName: string }): Promise<void> {
  const sourceAgentId = agentId.value
  const sourceName = stackName.value
  const content = editorContent.value
  if (sourceAgentId === null || sourceName === '' || copying.value) return
  copying.value = true
  copyError.value = ''
  try {
    await copyExternalConfig(sourceAgentId, sourceName, {
      target_agent_id: payload.targetAgentId,
      target_name: payload.targetName,
      content,
      confirmation: true
    })
    copyShow.value = false
    // 只能说「目标 Hub 记录」，不能说「已到目标服务器」——配置还没下发
    toastSuccess('已复制到目标 Hub 记录，尚未下发到目标服务器，也不会自动启动')
  } catch (err) {
    /*
     * 复制的失败要区分三种，提示各不相同：
     * - 403：源或目标缺 manage
     * - 409：目标已有同名项目（引擎里或 Hub 里），换名字或先处理目标
     * - 其他：原文展示
     */
    if (err instanceof ApiError && err.status === 403) {
      copyError.value = '复制配置需要对源与目标服务器都有管理权限。'
    } else if (err instanceof ApiError && err.status === 409) {
      copyError.value = `目标服务器上已存在同名编排「${payload.targetName}」，请换一个名称或先在目标上处理该项目。`
    } else {
      copyError.value = err instanceof Error ? err.message : '复制失败'
    }
  } finally {
    copying.value = false
  }
}

/** 回到外部项目列表 */
function backToList(): void {
  void router.push({ name: 'agent-stacks', params: { id: agentId.value ?? '' } })
}

onMounted(() => {
  void store.loadDetail()
  void store.loadConfig()
})

/*
 * 路由从「外部项目 A」切到「外部项目 B」时，Vue 会**复用同一个组件实例**
 * （同一条路由记录，只有 params 变了），onMounted 不会再跑。
 * 不重新加载的话，页面上显示的还是 A 的数据，用户会以为 B 就是 A 的样子。
 *
 * 这里只做一次 reset + reload，不引入轮询。
 */
watch(
  () => [agentId.value, stackName.value] as const,
  () => {
    editorContent.value = ''
    activeTab.value = 'overview'
    preview.value = null
    applyError.value = ''
    alreadyManaged.value = false
    copyShow.value = false
    void store.loadDetail()
    void store.loadConfig()
  }
)
</script>

<template>
  <div class="ext-detail">
    <page-header :title="pageTitle" :subtitle="subtitle">
      <template #actions>
        <n-button size="small" @click="backToList">返回编排列表</n-button>
        <n-button size="small" :disabled="!isOnline" :loading="store.detailLoading.value" @click="store.loadDetail">
          刷新
        </n-button>
      </template>
    </page-header>

    <!--
      常驻说明：这些项目不受 Arkade 管理，本页做的是「查看」和「把配置收进 Hub」，
      不会改变正在运行的东西。不解释清楚，用户会以为保存就会生效。
    -->
    <n-alert type="info" :bordered="false" class="ext-detail__notice">
      这是服务器上已存在、但尚未登记到 Arkade 的编排。本页可以查看容器与日志；
      编辑与保存<strong>只写入 Hub 草稿，不修改服务器上的文件、不重启或重建容器</strong>。
    </n-alert>

    <n-alert v-if="store.detailError.value !== ''" type="error" :bordered="false">
      {{ store.detailError.value }}
    </n-alert>

    <n-spin v-if="store.detailLoading.value && store.project.value === null" size="small" />

    <template v-else-if="store.project.value !== null">
      <n-tabs v-model:value="activeTab" type="line" animated>
        <!-- 概览：项目基本信息 + 为什么没被自动接管 -->
        <n-tab-pane name="overview" tab="概览">
          <external-stack-overview
            :project="store.project.value"
            :container-count="store.containers.value.length"
          />
          <!-- 容器明细：概览只给数字不够，接口返回的字段要真的显示出来 -->
          <div class="ext-detail__containers">
            <div class="ext-detail__containers-head">容器</div>
            <external-stack-containers
              :containers="store.containers.value"
              :agent-id="agentId ?? ''"
              :direct-address="directAddress"
            />
          </div>
        </n-tab-pane>

        <!-- 配置：需要 manage，无权限时显示原因而非报错 -->
        <n-tab-pane name="config" tab="配置">
          <external-stack-config-panel
            :state="store.configState.value"
            :raw-content="store.rawContent.value"
            :reason="store.configReason.value"
            :error-text="store.configError.value"
            :blockers="store.blockers.value"
            :draft="store.draft.value"
            :saving="store.savingDraft.value"
            :draft-error="store.draftError.value"
            :can-edit="store.configState.value === 'ready'"
            :draft-only="store.draftOnly.value"
            @save-draft="handleSaveDraft"
            @update:content="editorContent = $event"
          />

          <!--
            登记与复制只在对配置可读时出现：没有配置内容就没什么可登记。
            两者都需要 manage 权限，403 由后端判定并提示。
          -->
          <div v-if="store.configReadable.value" class="ext-detail__actions">
            <n-button
              size="small"
              :loading="applying"
              :disabled="store.hasBlockers.value && preview !== null && !preview.can_import"
              @click="handleApply(editorContent)"
            >
              登记到 Arkade
            </n-button>
            <n-button size="small" secondary @click="openCopy">复制配置到其他服务器</n-button>
            <span class="ext-detail__action-hint">
              登记只把配置收进 Hub，不启动也不重启容器。
            </span>
          </div>

          <!-- 预检阻塞项：如实展示，不擅自放行 -->
          <n-alert
            v-if="preview !== null && !preview.can_import"
            type="warning"
            :bordered="false"
            class="ext-detail__preview"
            title="这份配置暂时不能登记到 Arkade"
          >
            <ul class="ext-detail__preview-list">
              <li v-for="item in preview.blockers" :key="item">{{ item }}</li>
            </ul>
          </n-alert>
          <!--
            409 可能是同名已登记，也可能是校验/运行时冲突。前端分不清，
            所以只说「冲突」并给确认入口，**不宣称已登记**。
          -->
          <n-alert
            v-if="alreadyManaged"
            type="warning"
            :bordered="false"
            class="ext-detail__preview"
            title="登记未完成"
          >
            如果该项目已被自动登记，可以到已纳管列表确认；否则请按上面的提示处理后重试。
            <div class="ext-detail__already">
              <n-button size="small" @click="goManagedDetail">查看已纳管项目</n-button>
            </div>
          </n-alert>

          <p v-if="applyError !== ''" class="ext-detail__error">{{ applyError }}</p>
        </n-tab-pane>

        <!-- 日志：复用容器日志接口，先选容器 -->
        <n-tab-pane name="logs" tab="日志">
          <external-stack-logs :agent-id="agentId ?? ''" :containers="store.containers.value" />
        </n-tab-pane>
      </n-tabs>
    </template>

    <external-stack-copy-modal
      v-model:show="copyShow"
      :source-name="stackName"
      :content="editorContent"
      :agent-id="agentId ?? ''"
      :agents="agentList"
      :loading="copying"
      :error-text="copyError"
      @submit="handleCopy"
    />
  </div>
</template>

<style scoped>
.ext-detail {
  display: flex;
  flex-direction: column;
  gap: 12px;
}

.ext-detail__notice {
  font-size: 13px;
}

.ext-detail__containers {
  margin-top: 18px;
}

.ext-detail__containers-head {
  margin-bottom: 8px;
  font-size: 13px;
  font-weight: 600;
  color: var(--arkade-text-primary);
}

.ext-detail__actions {
  display: flex;
  align-items: center;
  gap: 10px;
  flex-wrap: wrap;
  margin-top: 12px;
}

.ext-detail__action-hint {
  font-size: 12px;
  color: var(--arkade-text-tertiary);
}

.ext-detail__preview-list {
  margin: 4px 0 0;
  padding-left: 18px;
}

.ext-detail__error {
  margin: 0;
  font-size: 13px;
  color: var(--arkade-error-color, #d03050);
}
</style>
