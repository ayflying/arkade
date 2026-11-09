<script setup lang="ts">
/**
 * 编排详情页 /agents/:id/stacks/:name
 *
 * 页面结构：
 * 1. 顶部：编排名 + 状态 + 漂移告警 + 操作栏（7 个操作）
 * 2. compose.yml 编辑器：可编辑并保存，带乐观锁
 * 3. 容器列表（ps 接口）
 * 4. 日志查看（可切服务）
 *
 * ## 乐观锁的必要性
 *
 * 保存时带读取到的 version。若期间别人也保存过，后端会返回冲突，
 * 前端提示「内容已被他人修改，请刷新」而**不是**静默覆盖。
 * compose.yml 一旦被覆盖，之前那人做的全部修改就丢了，
 * 所以这里宁可让用户重做一次也不要静默合并。
 */
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import { NAlert, NButton, NDataTable, NInput, NTabPane, NTabs } from 'naive-ui'
import type { DataTableColumns } from 'naive-ui'
import PageHeader from '@/components/common/PageHeader.vue'
import StackActionBar from '@/components/stack/StackActionBar.vue'
import StackEnvEditor from '@/components/stack/StackEnvEditor.vue'
import StackLogViewer from '@/components/stack/StackLogViewer.vue'
import StackStateTag from '@/components/stack/StackStateTag.vue'
import {
  deleteStack,
  getStack,
  getStackConfig,
  listStackPs,
  runStackAction,
  updateStack
} from '@/api/stacks'
import { useCurrentAgent } from '@/composables/useCurrentAgent'
import type {
  StackAction,
  StackDetail,
  StackPsContainer,
  StackState
} from '@/types/stack'
import { ApiError } from '@/types/api'
import { confirmWithOption } from '@/utils/confirm'
import { toastError, toastSuccess, toastWarning } from '@/utils/feedback'
import { formatAbsoluteTime, formatRelativeTime } from '@/utils/formatTime'

const router = useRouter()
const { agentId, isOnline } = useCurrentAgent()

/** 编排名来自路由参数 */
const stackName = computed<string>(() => {
  const raw = router.currentRoute.value.params.name
  const value = Array.isArray(raw) ? raw[0] : raw
  return typeof value === 'string' ? value : ''
})

const detail = ref<StackDetail | null>(null)
const loading = ref(false)
const loadError = ref('')

/** compose.yml 编辑态 */
const content = ref('')
const originalContent = ref('')
const version = ref(0)
const saving = ref(false)
const saveError = ref('')

/**
 * .env 编辑态。
 *
 * `originalEnv` 的意义：后端对 update 的 `env_content` 是「省略=保留、空串=清空」，
 * 所以必须知道用户到底动过没有。用户只改 compose 时若把 env_content 一起带上，
 * 一旦本地值因加载失败而为空串，就会**把服务器上的 .env 清空**——那是事故。
 * 因此只在 `envContent !== originalEnv` 时才提交该字段。
 */
const envContent = ref('')
const originalEnv = ref('')

/**
 * 配置是否真的加载成功。
 *
 * **保存的前置条件**：配置没加载成功时 version 不可信，
 * 保存会带着 0 或旧版本号发出去，轻则被 409 拒（像 bug），
 * 重则在无校验路径上覆盖别人的修改。所以宁可禁用保存。
 */
const configLoaded = ref(false)

/**
 * 配置加载失败的原因（多数是 403 无权限）。
 *
 * 与 loadError 分开：loadError 代表「整页不可用」，
 * 而 configError 只是「编辑区不可用」——运行状态、容器、日志都还能看。
 */
const configError = ref('')
/**
 * 同步未成功的提示（来自后端 sync_ok:false + sync_hint）。
 *
 * 与 saveError 分开的原因：这不是「保存失败」——内容确实已经存进 Hub 了，
 * 只是还没下发到服务器。若混在错误里展示，用户会以为要重写一遍。
 */
const syncWarning = ref('')

/** 删除中 */
const deleting = ref(false)

/** 容器列表（ps） */const containers = ref<StackPsContainer[]>([])
const psLoading = ref(false)

/** 当前 tab */
const activeTab = ref<'config' | 'containers' | 'logs'>('config')

const actionBarRef = ref<InstanceType<typeof StackActionBar> | null>(null)

/**
 * 是否有未保存的改动。
 *
 * .env 单独比较：它可能是空串（本来就没配），不能只看 content。
 */
const dirty = computed(
  () => content.value !== originalContent.value || envContent.value !== originalEnv.value
)

/**
 * 是否允许编辑配置。
 *
 * ## 为什么不按角色判断
 *
 * 后端只有 admin / user 两种角色，而真正的写权限是「用户组在某台 Agent 上的授权」
 * （view < operate < manage）。普通用户被授予 manage 后同样可以编辑。
 * 后端没有「查询我对这个 Agent 的权限档位」的接口，前端无从得知——
 * 按角色猜会让**明明有权限的普通用户永远看不到编辑入口**，那是更严重的问题。
 *
 * 所以这里只依赖两件**确定**的事：
 * 1. 配置是否真的加载成功（403 就是没权限，加载失败就是不可编辑）；
 * 2. 用户是否真的改过（没加载出内容自然也无从改）。
 *
 * 真正的拦截在后端（config 要 manage、update 要 manage）。前端不猜权限。
 */
const canEditConfig = computed(() => configLoaded.value && configError.value === '')

const state = computed<StackState>(() => detail.value?.status ?? 'unknown')

/** 操作类文案（用于成功提示） */
const ACTION_LABELS: Record<StackAction, string> = {
  start: '启动',
  stop: '停止',
  restart: '重启',
  pull: '更新镜像',
  recreate: '重建',
  backup: '备份',
  delete: '删除'
}

/**
 * 加载详情 + 配置（**两条独立通道，不能互相拖垮**）。
 *
 * ## 为什么必须分开
 *
 * `GET .../config` 现在要 manage 权限（因为返回内容含 .env 里的密钥）。
 * 而详情、容器列表、日志只需要 view 权限。
 * 如果两者放在同一个 Promise.all 里，viewer/operator 一进页面就因 config 403
 * **整页报错**，连「服务在不在跑」都看不到——这是可用性事故：
 * 只读用户最需要知道的恰恰是运行状态。
 *
 * 所以：详情失败才算整页失败；配置 403 只是「没有查看/编辑配置的权限」，
 * 页面其余部分照常工作。
 *
 * ## 另一个关键点
 *
 * 配置**没加载成功就必须禁止保存**。否则 version 还是 0，
 * 保存会带着错误的乐观锁版本号发出去，要么被拒（看起来像 bug），
 * 要么在无校验时覆盖别人的修改。因此用 `configLoaded` 把关。
 */
async function loadAll(): Promise<void> {
  if (agentId.value === null || stackName.value === '') return
  loading.value = true
  loadError.value = ''

  // 先拿详情：它决定整页能不能显示
  try {
    detail.value = await getStack(agentId.value, stackName.value)
  } catch (err) {
    loadError.value = err instanceof ApiError ? err.message : '加载编排详情失败'
    loading.value = false
    return
  }

  // 再单独拿配置：失败只影响编辑区
  await loadConfig()

  loading.value = false
}

/**
 * 单独加载配置（compose + .env + version）。
 *
 * 失败时不抛错、不污染 loadError：403 属于「权限不足」这一**预期情况**，
 * 不是异常。用 configError 单独承载，界面上只把编辑区换成提示。
 */
async function loadConfig(): Promise<void> {
  if (agentId.value === null || stackName.value === '') return
  configError.value = ''
  try {
    const configRes = await getStackConfig(agentId.value, stackName.value)
    content.value = configRes.content
    originalContent.value = configRes.content
    envContent.value = configRes.env_content
    originalEnv.value = configRes.env_content
    version.value = configRes.version
    configLoaded.value = true
  } catch (err) {
    // 没拿到配置就绝不能允许保存：version 未知，保存等于无锁覆盖
    configLoaded.value = false
    version.value = 0
    const apiError = err instanceof ApiError ? err : null
    if (apiError?.status === 403) {
      configError.value = '当前账号没有查看或编辑配置的权限，需要管理员权限。'
    } else {
      configError.value = apiError?.message ?? '加载配置失败'
    }
  }
}

/** 加载容器列表 */
async function loadPs(): Promise<void> {
  if (agentId.value === null || stackName.value === '') return
  psLoading.value = true
  try {
    const res = await listStackPs(agentId.value, stackName.value)
    containers.value = res.containers
  } catch {
    // 容器列表失败不打断主流程：详情/配置仍然可用
    containers.value = []
  } finally {
    psLoading.value = false
  }
}

/**
 * 保存 compose.yml（以及改动过的 .env）。
 *
 * 冲突处理：后端用 409/`conflict` 表示版本不一致。
 * 这里不只提示，还直接把本地未保存的修改标记出来，
 * 让用户能先复制自己改的内容再刷新，不至于直接丢失。
 *
 * ## env_content 的三态必须精确表达
 * - 用户没动 .env → **不传该字段**，后端保留原值；
 * - 用户清空了 .env → 传空串，后端清空；
 * - 用户改了 → 传新内容。
 *
 * 为什么不能「总是传」：一旦加载配置失败、本地 envContent 是空串，
 * 总是传就会把服务器上的 .env 静默清空——里面的密码全丢。
 * 因此用 `envContent !== originalEnv` 判断是否动过。
 */
async function handleSave(): Promise<void> {
  if (agentId.value === null || saving.value) return

  /*
   * 双保险：按钮已经 disabled，但这里再挡一次。
   * 配置没加载成功时 version 不可信（可能是 0），
   * 带着错误版本号保存会覆盖别人的修改或被无意义地 409。
   */
  if (!configLoaded.value) {
    toastError('配置尚未加载成功，无法保存。请先刷新重试。')
    return
  }

  saving.value = true
  saveError.value = ''
  try {
    const envChanged = envContent.value !== originalEnv.value
    const res = await updateStack(agentId.value, stackName.value, {
      content: content.value,
      version: version.value,
      // 只在真的改过时带上；没改就不传，让后端保留原 .env
      ...(envChanged ? { env_content: envContent.value } : {})
    })
    // 保存成功后用服务端返回的新版本号更新，便于连续编辑。
    // 版本号在 stack 里（后端把编排信息包在 stack 下），不在顶层。
    version.value = res.stack.version
    originalContent.value = content.value
    // 同步本地基线：否则保存完 dirty 仍是 true，保存按钮会一直亮着
    originalEnv.value = envContent.value

    /*
     * 必须检查 sync_ok：Agent 离线或下发失败时后端**不报错**，
     * 而是返回 200 + sync_ok:false + sync_hint（内容已存 Hub，还没推到服务器）。
     * 这是「离线也不丢用户内容」的设计，但前端若当成成功提示，
     * 用户会以为已经部署到服务器上了，实际什么都没变。
     */
    if (res.sync_ok) {
      toastSuccess('配置已保存并同步到服务器')
    } else {
      syncWarning.value = res.sync_hint ?? '配置已保存到 Hub，但尚未同步到服务器。'
      toastWarning(syncWarning.value)
    }
    await loadAll()
  } catch (err) {
    const apiError = err instanceof ApiError ? err : null
    if (apiError?.status === 409 || apiError?.code === 'conflict') {
      saveError.value =
        '内容已被他人修改，无法保存。请先复制你本次的修改，然后刷新页面获取最新内容。'
    } else {
      saveError.value = apiError?.message ?? '保存配置失败'
    }
    toastError(saveError.value)
  } finally {
    saving.value = false
  }
}

/** 放弃当前修改，恢复为服务端内容（compose 与 .env 都要回退） */
function handleReset(): void {
  content.value = originalContent.value
  envContent.value = originalEnv.value
  saveError.value = ''
}

/**
 * 执行编排操作。
 *
 * 成功后重新拉详情与容器列表：
 * compose 操作是异步的（后端返回时容器可能还在重启），
 * 因此隔一小段时间再刷一次，让状态尽量贴近真实。
 */
async function handleAction(action: StackAction): Promise<void> {
  if (agentId.value === null) return
  try {
    await runStackAction(agentId.value, stackName.value, { action })
    toastSuccess(`已下发「${ACTION_LABELS[action]}」操作`)
    await loadAll()
    await loadPs()
  } catch (err) {
    toastError(err instanceof ApiError ? err.message : `「${ACTION_LABELS[action]}」失败`)
  } finally {
    // 无论成功失败都要清掉按钮 loading，否则按钮会一直转
    actionBarRef.value?.finish()
  }
}

/**
 * 删除编排。
 *
 * ## 为什么默认保留数据卷
 *
 * 删卷是**不可恢复的数据丢失**。用户点「删除」的意图通常是
 * 「删掉这个编排」，不一定想连数据一起清掉。
 * 业界惯例（docker compose down 不加 -v 也不删卷）同样是保留。
 *
 * 因此这里用带勾选项的确认框：默认不删卷，
 * 只有用户在框里主动勾选「同时删除数据卷」才传 remove_volumes=true。
 * 危险操作必须由用户表达明确意图，而不是靠默认值替他决定。
 *
 * ## 离线时不能删
 *
 * 删除必须由 Agent 执行（它要在那台机器上真的移除容器与文件），
 * Agent 离线时后端返回 503 并**保留管理记录**——记录留着，用户上线后还能再删一次。
 * 所以界面上不能承诺「离线也能删除记录」：那会让用户以为删了，
 * 结果服务器上还在跑、占着端口。
 */
async function handleDelete(): Promise<void> {
  if (agentId.value === null || deleting.value) return

  // 离线直接拦住：让用户看到原因，而不是点完拿到一个 503
  if (!isOnline.value) {
    toastWarning('服务器离线，无法删除编排。请等服务器上线后再试（记录会保留）。')
    return
  }

  const result = await confirmWithOption({
    title: '确认删除编排？',
    content:
      `将停止并移除编排「${stackName.value}」的所有容器。\n\n` +
      '数据卷会保留，如需一并删除请在下方勾选。',
    optionLabel: '同时删除数据卷（数据将永久丢失）',
    // 默认不勾：危险选项绝不给默认值
    optionDefault: false,
    optionHint: '⚠️ 同时删除数据卷，卷内数据将永久丢失且无法恢复。',
    positiveText: '删除',
    negativeText: '取消',
    danger: true
  })
  if (!result.confirmed) return

  deleting.value = true
  try {
    // 只有用户勾选了才连带删卷
    await deleteStack(agentId.value, stackName.value, result.optionChecked)
    toastSuccess(result.optionChecked ? '编排及数据卷已删除' : '编排已删除，数据卷已保留')
    await router.replace({ name: 'agent-stacks', params: { id: agentId.value } })
  } catch (err) {
    toastError(err instanceof ApiError ? err.message : '删除编排失败')
  } finally {
    deleting.value = false
  }
}

/** 返回列表 */
function goBack(): void {
  if (agentId.value === null) return
  void router.push({ name: 'agent-stacks', params: { id: agentId.value } })
}

const psColumns = computed<DataTableColumns<StackPsContainer>>(() => [
  {
    title: '名称',
    key: 'name',
    minWidth: 160,
    render: (row) => row.name
  },
  {
    title: '服务',
    key: 'service',
    minWidth: 110,
    render: (row) => row.service
  },
  {
    title: '状态',
    key: 'state',
    minWidth: 100,
    render: (row) =>
      row.state === 'running'
        ? (row.status || '运行中')
        : row.status || row.state
  },
  {
    title: '镜像',
    key: 'image',
    minWidth: 180,
    render: (row) => row.image
  }
])

onMounted(async () => {
  await loadAll()
  await loadPs()
})

/**
 * 离开页面时清空 .env 与 compose 内容。
 *
 * 两者都可能含密钥（.env 里的密码、compose 里私有仓库凭据），
 * 不该在组件销毁后还被闭包引用住。这里不做任何草稿持久化，
 * 所以清空不会影响用户体验。
 */
onUnmounted(() => {
  envContent.value = ''
  originalEnv.value = ''
  content.value = ''
  originalContent.value = ''
})
</script>

<template>
  <div class="stack-detail">
    <page-header :title="stackName" :subtitle="`编排详情 · ${activeTab === 'config' ? '配置' : ''}`">
      <template #actions>
        <n-button size="small" @click="goBack">返回列表</n-button>
        <n-button size="small" :loading="loading" @click="loadAll">刷新</n-button>
      </template>
    </page-header>

    <!-- 加载失败：给出重试而不是白屏 -->
    <div v-if="loadError !== ''" class="stack-detail__alert">
      <n-alert type="error" closable>{{ loadError }}</n-alert>
      <n-button size="small" class="stack-detail__retry" @click="loadAll">重试</n-button>
    </div>

    <template v-else>
      <!-- 状态卡片：状态 + 漂移告警 + 操作栏 -->
      <div class="stack-detail__card">
        <div class="stack-detail__headline">
          <stack-state-tag :state="state" />
          <span v-if="detail" class="stack-detail__meta">
            最后更新：{{ formatRelativeTime(detail.updated_at) }}
            （{{ formatAbsoluteTime(detail.updated_at) }}）
          </span>
        </div>

        <!--
          drift 告警：显式、醒目、不可关闭。
          Hub 记录与服务器本机文件哈希不一致意味着「有人绕过 Hub 改了文件」，
          用户必须知道，否则会误以为 Hub 里的配置就是线上真实配置。
        -->
        <n-alert
          v-if="detail?.drift"
          type="warning"
          :bordered="false"
          class="stack-detail__drift"
          title="配置漂移"
        >
          该编排在服务器上的 compose.yml 与 Hub 记录不一致，可能有人直接修改了本机文件。
          你在下方编辑并保存会覆盖服务器上的改动，请先确认线上真实配置。
        </n-alert>

        <!--
          离线提示：操作类动作（启动/停止等）必须下发到 Agent 才能执行，
          但**编辑并保存配置仍然可用**——后端会把内容先存进 Hub，
          等 Agent 上线再同步（返回 sync_ok:false 告知）。
          所以这里只说清「哪些不能做」，不要一句话把用户吓退。
        -->
        <n-alert v-if="!isOnline" type="warning" :bordered="false" class="stack-detail__drift">
          服务器离线，启动/停止等操作暂时不可用，也无法获取容器列表。
          但配置仍可编辑并保存，会先存入 Hub；服务器上线后需要手动重新下发才能生效。
        </n-alert>

        <stack-action-bar
          ref="actionBarRef"
          :state="state"
          :online="isOnline"
          :deleting="deleting"
          @action="handleAction"
          @delete="handleDelete"
        />
      </div>

      <!-- 三个 tab：配置 / 容器 / 日志 -->
      <div class="stack-detail__card">
        <n-tabs v-model:value="activeTab" type="line" animated>
          <n-tab-pane name="config" tab="compose.yml">
            <!--
              配置加载失败（多为 403 无 manage 权限）：
              只把编辑区换掉，页面其余部分（状态、容器、日志）照常可用。
              只读用户最需要的就是看到运行状态，不能因为看不了配置就整页报废。
            -->
            <n-alert
              v-if="configError !== ''"
              type="info"
              :bordered="false"
              class="editor__error"
              title="无法查看或编辑配置"
            >
              {{ configError }}
              <div class="editor__retry">
                <n-button size="tiny" @click="loadConfig">重试</n-button>
              </div>
            </n-alert>

            <div v-else class="editor">
              <div class="editor__bar">
                <span class="editor__hint">
                  {{ dirty ? '有未保存的修改' : '内容与服务端一致' }}
                </span>
                <span class="editor__meta">版本 v{{ version }}</span>
              </div>

              <!-- 保存冲突等错误提示 -->
              <n-alert
                v-if="saveError !== ''"
                type="error"
                :bordered="false"
                closable
                class="editor__error"
                @close="saveError = ''"
              >
                {{ saveError }}
              </n-alert>

              <!--
                同步未成功的警告：内容已存进 Hub，但还没下发到服务器。
                单独用 warning 而不是 error——保存本身是成功的，
                用户不用重写，只要等 Agent 上线或修复连接后再同步。
              -->
              <n-alert
                v-if="syncWarning !== ''"
                type="warning"
                :bordered="false"
                closable
                title="已保存到 Hub，但未同步到服务器"
                class="editor__error"
                @close="syncWarning = ''"
              >
                {{ syncWarning }}
              </n-alert>

              <!--
                编辑器：textarea + 等宽字体，不引 CodeMirror/Monaco。
                spellcheck=false 避免 YAML 里的词被标红。
              -->
              <n-input
                v-model:value="content"
                type="textarea"
                class="editor__input"
                :autosize="{ minRows: 14, maxRows: 28 }"
                :disabled="saving || !canEditConfig"
                spellcheck="false"
              />

              <!--
                .env 编辑区：紧贴 compose 下方。
                默认收起+打码（里面通常是密码）。它和 compose 一起保存：
                后端 update 的 env_content 省略=保留、空串=清空，
                是否带上该字段由 handleSave 按「有没有改过」决定。
              -->
              <stack-env-editor
                v-model="envContent"
                :disabled="saving || !canEditConfig"
                class="detail-env"
              />

              <!-- 无编辑权限时说明为什么按钮是灰的，避免用户反复点 -->
              <p v-if="!canEditConfig" class="editor__readonly">
                当前账号只能查看，不能修改配置。如需编辑请联系管理员。
              </p>

              <div class="editor__actions">
                <n-button
                  size="small"
                  type="primary"
                  :disabled="!dirty || saving || !canEditConfig"
                  :loading="saving"
                  @click="handleSave"
                >
                  保存
                </n-button>
                <n-button
                  size="small"
                  :disabled="!dirty || saving || !canEditConfig"
                  @click="handleReset"
                >
                  放弃修改
                </n-button>
              </div>
            </div>
          </n-tab-pane>

          <n-tab-pane name="containers" tab="容器">
            <div class="table-card arkade-table-scroll">
              <n-data-table
                v-if="containers.length > 0 || psLoading"
                :columns="psColumns"
                :data="containers"
                :loading="psLoading"
                :row-key="(row: StackPsContainer) => row.id"
                :bordered="false"
                :single-line="false"
                size="small"
                :scroll-x="650"
              />
              <div v-else class="stack-detail__empty">该编排下还没有运行中的容器</div>
            </div>
          </n-tab-pane>

          <n-tab-pane name="logs" tab="日志">
            <!-- 只在 tab 可见时加载，避免进页面就打一堆请求 -->
            <stack-log-viewer
              v-if="agentId"
              :agent-id="agentId"
              :stack-name="stackName"
              :services="detail?.services ?? []"
              :online="isOnline"
              :visible="activeTab === 'logs'"
            />
          </n-tab-pane>
        </n-tabs>
      </div>
    </template>
  </div>
</template>

<style scoped>
.stack-detail__alert {
  display: flex;
  align-items: center;
  gap: 8px;
  margin-bottom: 16px;
}

.stack-detail__alert :deep(.n-alert) {
  flex: 1;
}

.stack-detail__card {
  background: #fff;
  border-radius: 8px;
  padding: 16px;
  margin-bottom: 16px;
}

.stack-detail__headline {
  display: flex;
  align-items: center;
  gap: 12px;
  flex-wrap: wrap;
  margin-bottom: 12px;
}

.stack-detail__meta {
  font-size: 12px;
  color: var(--arkade-text-tertiary);
}

.stack-detail__drift {
  margin-bottom: 12px;
}

.editor {
  border: 1px solid var(--arkade-border-color);
  border-radius: 6px;
  overflow: hidden;
}

.editor__bar {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 6px 10px;
  background: var(--arkade-page-bg);
  border-bottom: 1px solid var(--arkade-border-color);
  font-size: 12px;
  color: var(--arkade-text-tertiary);
}

.editor__error {
  margin: 8px;
}

/* 只读说明：告诉用户按钮为什么是灰的，别让人反复点 */
.editor__readonly {
  margin: 0;
  padding: 8px 10px 0;
  font-size: 12px;
  color: var(--arkade-text-tertiary);
}

.editor__retry {
  margin-top: 8px;
}

/* 等宽字体：YAML 靠缩进表达层级，必须等宽才能对齐 */
.editor__input :deep(textarea) {
  font-family: var(--arkade-mono-font);
  font-size: 13px;
  line-height: 1.6;
  tab-size: 2;
}

.editor__input :deep(.n-input__border),
.editor__input :deep(.n-input__state-border) {
  display: none;
}

.editor__actions {
  display: flex;
  gap: 8px;
  padding: 10px;
  border-top: 1px solid var(--arkade-border-color);
  background: var(--arkade-page-bg);
}

.table-card {
  border-radius: 6px;
}

.stack-detail__empty {
  padding: 32px;
  text-align: center;
  font-size: 13px;
  color: var(--arkade-text-tertiary);
}

/* 手机端：卡片内边距收紧，把宽度让给内容 */
@media (max-width: 767.98px) {
  .stack-detail__card {
    padding: 12px;
  }
}
</style>
