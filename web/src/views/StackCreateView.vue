<script setup lang="ts">
/**
 * 新建编排页 /agents/:id/stacks/new
 *
 * 独立成页而不是弹窗：
 * compose.yml 动辄几十行，弹窗里写代码体验差，而且弹窗容易被误关丢内容。
 *
 * ## 为什么 compose 旁边必须有 .env
 *
 * compose 里写 `${DB_PASSWORD}` 是最常见写法。若界面上没有 .env 的位置，
 * 用户把它贴进来后服务直接起不来，报错还只说「变量未设置」，
 * 完全不知道要去哪配。因此 .env 编辑区是本页的必要组成，不是附加功能。
 *
 * 但 .env 里通常就是密码，所以默认收起、默认打码，用户显式展开才可见。
 * 内容**不写入任何浏览器存储**，离开页面即丢弃（见 onUnmounted）。
 *
 * 表单校验在前端先做一轮（名称格式、内容非空），
 * 避免用户写完一大段 YAML 提交后才被后端打回。
 */
import { computed, onUnmounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import { NAlert, NButton, NForm, NFormItem, NInput } from 'naive-ui'
import type { FormInst, FormRules } from 'naive-ui'
import PageHeader from '@/components/common/PageHeader.vue'
import CodeEditor from '@/components/common/CodeEditor.vue'
import StackEnvEditor from '@/components/stack/StackEnvEditor.vue'
import StackOperationModal from '@/components/stack/StackOperationModal.vue'
import { useCurrentAgent } from '@/composables/useCurrentAgent'
import { toastSuccess, toastWarning } from '@/utils/feedback'

const router = useRouter()
const { agentId, isOnline, displayName } = useCurrentAgent()

const formRef = ref<FormInst | null>(null)
/** 是否显示新建执行弹窗 */
const createShow = ref(false)
/** 创建任务已失败（配置很可能已入库）：引导去详情页部署，而不是重复 create */
const createFailed = ref(false)
  /**
   * 创建任务是否正在执行。
   *
   * 弹窗按 pendingCreate 挂载，并绑定当前 agent/name。任务在跑时若切换
   * 服务器或改名，会重新挂载弹窗、丢掉进行中的任务与 request_id。
   * 所以期间锁住这些选择器。
   */
  const createBusy = ref(false)
/**
 * 待创建的快照。
 *
 * 弹窗执行期间用户可能继续编辑表单，但提交的必须是点「创建」那一刻的内容，
 * 否则"确认的"和"执行的"不是同一个东西。
 */
const pendingCreate = ref<{
  name: string
  content: string
  envContent: string
} | null>(null)
const submitting = ref(false)
const submitError = ref('')

/**
 * 新建时预填一个最小可用的 compose 示例。
 *
 * 为什么预填而不是留空：多数用户第一次会照着示例改，
 * 留空的话连接缝该缩进几格都要靠猜。
 * 注意：必须声明在 form 之前——form 的初始化要立即读它，
 * 若放在后面会命中 const 的暂时性死区（TDZ）而直接报错。
 *
 * 示例刻意**不引用 ${VAR}**：给了 `${DB_PASSWORD}` 这类示例，
 * 用户删掉 .env 后 compose 就跑不起来。默认示例必须是自洽可用的。
 */
const DEFAULT_COMPOSE = `services:
  web:
    image: nginx:1.27
    restart: unless-stopped
    ports:
      - "8080:80"
`

/**
 * 表单模型。
 *
 * `envContent` 初始为空：新建时默认不配 .env，
 * 用户有需要才展开编辑区填。
 */
const form = ref({ name: '', content: DEFAULT_COMPOSE, envContent: '' })

/**
 * 编排名校验。
 *
 * 与后端保持同一套规则（防止提交后才报错）：
 * - 只允许字母、数字、下划线、连字符、点；
 * - 必须以字母或数字开头（compose 项目名不允许首字符是符号）；
 * - 长度 1~64。
 */
const NAME_PATTERN = /^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,63}$/

const rules: FormRules = {
  name: [
    { required: true, message: '请输入编排名', trigger: ['input', 'blur'] },
    {
      validator: (_rule, value: string) => {
        if (value === '') return true
        if (!NAME_PATTERN.test(value)) {
          return new Error('只能包含字母、数字、下划线、连字符和点，且需以字母或数字开头')
        }
        return true
      },
      trigger: ['input', 'blur']
    }
  ],
  content: [
    { required: true, message: '请填写 compose.yml 内容', trigger: ['input', 'blur'] },
    {
      validator: (_rule, value: string) => {
        // 只挡「全是空白」，不尝试校验 YAML 语法——语法错误由后端 docker compose 判定，
        // 前端硬写一个 YAML 解析器既容易误判又要引依赖
        if (value.trim() === '') return new Error('compose.yml 内容不能为空')
        return true
      },
      trigger: ['input', 'blur']
    }
  ]
}

/** 内容行数：给编辑器一个「写到哪了」的提示 */
const lineCount = computed(() => form.value.content.split('\n').length)

/** 提交 */
async function handleSubmit(): Promise<void> {
  if (agentId.value === null || submitting.value) return

  /*
   * 已有创建任务（无论还在跑、已失败、还是状态未知）→ **只重开查看**。
   *
   * 绝不能拿当前表单重新定格：form 里的 name/content 可能已经被改过，
   * 用新参数配旧 request_id 会 409，用新 name 又会在错的名字下建东西。
   * 放在表单校验之前：用户此时只是想看进度，表单改得对不对不重要。
   * 只有在明确选择「新建另一个编排」且旧任务已终态时才会清空重来。
   */
  if (pendingCreate.value !== null) {
    createShow.value = true
    return
  }

  // 先跑表单校验，不通过直接返回（NForm 会标红具体字段）
  try {
    await formRef.value?.validate()
  } catch {
    return
  }

  submitError.value = ''
  /*
   * 新建改走操作任务：Hub 在 create 成功持久化之后才返回 202，
   * 之后由 Hub 注入 spec 下发并执行 up。
   *
   * **不在前端先 createStack 再 up**：两次请求之间可能失败，
   * 留下「建了但没部署」的半成品，而且前端也无从知道该不该回滚。
   */
  pendingCreate.value = {
    name: form.value.name.trim(),
    content: form.value.content,
    envContent: form.value.envContent
  }
  createShow.value = true
}

/**
 * 新建任务结束。
 *
 * 只有 `status === 'succeeded'` 才跳详情页——`202 已受理` 表示任务刚开始跑，
 * 此时跳走会让用户以为建好了，实际可能还在失败中。
 *
 * 失败时**绝不能再走一次 create**。Hub 在返回 202 之前已经把配置持久化了，
 * 编排已存在；再发 `create:true` 会被判定为「已存在」返回 409。
 * 正确做法是引导用户去详情页用「启动/保存并部署」——那是幂等的 up。
 */
function handleCreateFinished(task: unknown): void {
  const t = task as { status?: string; error?: string } | null
  if (t?.status === 'succeeded') {
    const created = pendingCreate.value
    if (created === null || agentId.value === null) return
    toastSuccess('编排已创建并部署')
    void router.replace({
      name: 'agent-stack-detail',
      params: { id: agentId.value, name: created.name }
    })
    return
  }

  /*
   * 失败：配置很可能已存进 Hub（Hub 先持久化再下发）。
   * 给出明确出路而不是让用户重试"新建"——重试必然 409。
   */
  createFailed.value = true
  const reason = t?.error !== undefined && t.error !== '' ? t.error : '部署阶段失败'
  submitError.value = `编排可能已创建，但部署没有完成（${reason}）。请到详情页查看并重试部署。`
  toastWarning('编排可能已创建但未部署，请到详情页重试部署。')
}

/**
 * 关闭弹窗：**保留 pendingCreate**。
 *
 * 清空会让承载任务的弹窗实例失去 props、被条件渲染卸载，
 * 任务与 request_id 一起丢掉；而且状态未知时重开会变成新任务。
 */
function handleCreateShow(value: boolean): void {
  createShow.value = value
}

/** 失败后去详情页重试部署（那里是幂等的 up，不会重复创建） */
function goDetailToDeploy(): void {
  const created = pendingCreate.value
  if (created === null || agentId.value === null) return
  void router.replace({
    name: 'agent-stack-detail',
    params: { id: agentId.value, name: created.name }
  })
}

/**
 * 明确要建**另一个**编排。
 *
 * 只在旧任务已确认失败、且没有任务在跑时提供这个出口——
 * 此时清空 pendingCreate 会让弹窗卸载，但那是一个已经结束的失败任务，
 * 丢掉它没有代价；若任务还在跑或状态未知，绝不能清（那是放弃幂等）。
 * 表单内容保留，用户改个名字就能继续用。
 */
function resetCreate(): void {
  if (createBusy.value || !createFailed.value) return
  createShow.value = false
  pendingCreate.value = null
  createFailed.value = false
  submitError.value = ''
}

/** 返回列表 */
function goBack(): void {
  if (agentId.value === null) return
  void router.push({ name: 'agent-stacks', params: { id: agentId.value } })
}

/**
 * 离开页面时清空 .env 内容。
 *
 * .env 里通常是密码，不该在组件销毁后还被闭包引用住。
 * 这里同时清 compose 内容：它也可能含密钥（如 image 私有仓库凭据），
 * 且没有「返回后恢复草稿」的需求——本就不做草稿持久化。
 */
onUnmounted(() => {
  form.value.envContent = ''
  form.value.content = ''
})
</script>

<template>
  <div class="stack-create">
    <page-header title="新建编排" :subtitle="`在 ${displayName} 上创建新的容器编排`">
      <template #actions>
        <n-button size="small" @click="goBack">返回列表</n-button>
        <n-button
          v-if="pendingCreate !== null && createFailed && !createBusy"
          size="small"
          @click="resetCreate"
        >
          新建另一个编排
        </n-button>
        <n-button
          size="small"
          type="primary"
          :loading="submitting"
          :disabled="createBusy"
          @click="handleSubmit"
        >
          {{ pendingCreate !== null ? '查看任务' : '创建' }}
        </n-button>
      </template>
    </page-header>

    <!--
      离线也能创建：后端支持「先写 Hub」，返回 200 + sync_ok:false + sync_hint 而不是报错。
      因此这里不禁用创建按钮，只提示用户「现在创建的会存进 Hub，上线后需手动重新下发」。
      注意不能写成「自动同步」——目前没有自动上线同步机制，
      承诺了却没有发生，用户会发现服务器上跑的还是旧配置。
    -->
    <div v-if="!isOnline" class="stack-create__alert">
      <n-alert type="warning" :bordered="false" title="服务器当前离线">
        你仍可以创建编排：内容会先保存到 Hub，不会丢失。服务器上线后需要手动重新下发才会生效。
      </n-alert>
    </div>

    <!-- 失败但编排可能已创建：给出唯一正确的出路，而不是让用户重试"新建"（必然 409） -->
    <div v-if="createFailed" class="stack-create__alert">
      <n-alert type="warning" closable @close="createFailed = false">
        {{ submitError }}
        <n-button text size="tiny" class="stack-create__goto" @click="goDetailToDeploy">
          前往详情页重试部署
        </n-button>
      </n-alert>
    </div>
    <div v-if="submitError !== '' && !createFailed" class="stack-create__alert">
      <n-alert type="error" closable @close="submitError = ''">{{ submitError }}</n-alert>
    </div>

    <div class="stack-create__card">
      <n-form ref="formRef" :model="form" :rules="rules" label-placement="top">
        <n-form-item label="编排名" path="name">
          <n-input
            v-model:value="form.name"
            placeholder="例如 my-app（仅字母、数字、下划线、连字符、点）"
            :disabled="submitting"
          />
        </n-form-item>

        <n-form-item label="compose.yml" path="content">
          <div class="editor">
            <div class="editor__bar">
              <span class="editor__hint">直接粘贴或编辑 YAML 内容</span>
              <span class="editor__meta">{{ lineCount }} 行</span>
            </div>
            <!-- 共享 CodeMirror 编辑器：行号、YAML 高亮、两空格缩进 -->
            <code-editor
              v-model="form.content"
              aria-label="compose.yml 内容"
              :disabled="submitting"
              :min-height="380"
              :max-height="560"
            />
          </div>
        </n-form-item>

        <!--
          .env 编辑区：紧贴 compose 编辑器下方。
          默认收起+打码（里面通常是密码），用户显式展开才编辑。
          放在 compose 之后而不是之前：用户的注意力顺序是「先写 compose，
          发现有 ${VAR} 才需要 .env」，顺序反了会让人以为必须先配环境变量。
        -->
        <n-form-item label="环境变量 .env">
          <stack-env-editor
            v-model="form.envContent"
            :disabled="submitting"
            class="stack-create__env"
          />
        </n-form-item>
      </n-form>

    <!--
      新建走操作任务：Hub 在 create 成功持久化之后才返回 202，
      再由 Hub 注入 spec 下发执行 up。弹窗展示真实命令与实时输出。
      成功后由 handleCreateFinished 跳详情页；失败留在本页并保留表单。
    -->
    <!--
      常驻实例（不 v-if）：关闭仅隐藏。若用 v-if 卸载，进行中/未知状态的
      创建任务连同 request_id 一起丢失，再点创建就可能重复执行。
    -->
    <stack-operation-modal
      v-if="pendingCreate !== null"
      :show="createShow"
      :agent-id="agentId ?? ''"
      :stack-name="pendingCreate?.name ?? ''"
      action="up"
      online
      create
      :content="pendingCreate?.content"
      :env-content="pendingCreate?.envContent"
      auto-start
      @finished="handleCreateFinished"
      @busy="createBusy = $event"
      @update:show="handleCreateShow"
    />
    </div>
  </div>
</template>

<style scoped>
.stack-create__alert {
  margin-bottom: 16px;
}

.stack-create__card {
  background: #fff;
  border-radius: 8px;
  padding: 20px;
}

.editor {
  width: 100%;
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

/* 行数与提示分离，避免长提示挤掉行数 */
.editor__meta {
  flex-shrink: 0;
}

/*
 * 等宽字体 + 略小字号 + 1.6 行高：
 * YAML 靠缩进表达层级，等宽字体才能让缩进对齐看得准。
 */
.editor__input :deep(textarea) {
  font-family: var(--arkade-mono-font);
  font-size: 13px;
  line-height: 1.6;
  tab-size: 2;
}

/* 编辑器外边距清零，让边框贴合容器 */
.editor__input :deep(.n-input__border),
.editor__input :deep(.n-input__state-border) {
  display: none;
}

/* .env 编辑区占满整行：它是个独立的折叠块，不是窄输入框 */
.stack-create__env {
  width: 100%;
}
</style>
