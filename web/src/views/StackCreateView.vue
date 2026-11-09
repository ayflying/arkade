<script setup lang="ts">
/**
 * 新建编排页 /agents/:id/stacks/new
 *
 * 独立成页而不是弹窗：
 * compose.yml 动辄几十行，弹窗里写代码体验差，而且弹窗容易被误关丢内容。
 *
 * 编辑器实现：`NInput type="textarea"` + 等宽字体 + 行号关闭。
 * 刻意不引 CodeMirror/Monaco——按项目「不轻易加依赖」的基调，
 * 这里的需求只是「能舒服地贴一段 YAML」，原生 textarea 足够。
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
import StackEnvEditor from '@/components/stack/StackEnvEditor.vue'
import { createStack } from '@/api/stacks'
import { useCurrentAgent } from '@/composables/useCurrentAgent'
import { ApiError } from '@/types/api'
import { toastError, toastSuccess, toastWarning } from '@/utils/feedback'

const router = useRouter()
const { agentId, isOnline, displayName } = useCurrentAgent()

const formRef = ref<FormInst | null>(null)
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

  // 先跑表单校验，不通过直接返回（NForm 会标红具体字段）
  try {
    await formRef.value?.validate()
  } catch {
    return
  }

  submitting.value = true
  submitError.value = ''
  try {
    const res = await createStack(agentId.value, {
      name: form.value.name.trim(),
      content: form.value.content,
      // .env 总是随创建一起提交（可为空串）：新建场景没有「保留旧值」的语义，
      // 不传会让后端无法区分「没配」和「没给」
      env_content: form.value.envContent
    })

    /*
     * 必须检查 sync_ok：Agent 离线或下发失败时后端**不报错**，
     * 而是返回 200 + sync_ok:false + sync_hint。
     * 内容已经存进 Hub（不会丢用户写的东西），但服务器上还没生效。
     * 若这里直接当成功提示，用户会以为已经部署好了。
     */
    if (res.sync_ok) {
      toastSuccess('编排已创建并同步到服务器')
    } else {
      toastWarning(res.sync_hint ?? '编排已保存到 Hub，但尚未同步到服务器。')
    }

    // 创建成功后直接进详情页，用户可以立刻启动它
    await router.replace({
      name: 'agent-stack-detail',
      params: { id: agentId.value, name: form.value.name.trim() }
    })
  } catch (err) {
    submitError.value = err instanceof ApiError ? err.message : '创建编排失败'
    toastError(submitError.value)
  } finally {
    submitting.value = false
  }
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
          size="small"
          type="primary"
          :loading="submitting"
          @click="handleSubmit"
        >
          创建
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

    <div v-if="submitError !== ''" class="stack-create__alert">
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
            <!--
              只用 textarea + 等宽字体，不引编辑器依赖。
              :autosize 让内容多时自动长高，避免在小框里滚动。
            -->
            <n-input
              v-model:value="form.content"
              type="textarea"
              class="editor__input"
              placeholder="services:&#10;  web:&#10;    image: nginx:1.27"
              :autosize="{ minRows: 16, maxRows: 30 }"
              :disabled="submitting"
              spellcheck="false"
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
