<script setup lang="ts">
/**
 * 添加服务器（预创建）弹窗
 *
 * ## 这里到底在「添加」什么
 *
 * Hub 没法主动连进内网里的服务器，所以真正的接入动作永远发生在服务器那一侧。
 * 这个弹窗做的是**预创建一条 pending 记录 + 领一把专属密钥**：
 * 用户拿去那台机器上启动 Agent，连上之后这条记录才从「待接入」变成「在线」。
 *
 * 这个区别必须在界面上讲清楚，否则用户点完「添加」就以为活干完了，
 * 然后对着一台一直是「待接入」的服务器发愁。
 *
 * ## 敏感值处理
 * 本弹窗自身**不接触密钥**：创建成功后把 enrollment 原样 emit 给页面，
 * 由页面转交给 EnrollmentTokenModal（那个组件负责一次性展示与关闭清理）。
 * 这样密钥的生存期只有一个弹窗那么长，不会在本组件的状态里多留一份。
 */
import { computed, reactive, ref, watch } from 'vue'
import { NAlert, NButton, NForm, NFormItem, NInput, NModal } from 'naive-ui'
import type { FormInst, FormRules } from 'naive-ui'
import { createAgent } from '@/api/agents'
import type { CreateAgentResponse } from '@/types/agent'
import { ApiError } from '@/types/api'
import { toastError } from '@/utils/feedback'

interface Props {
  show: boolean
}

const props = defineProps<Props>()

const emit = defineEmits<{
  (e: 'update:show', value: boolean): void
  /** 创建成功：把后端返回的 agent + enrollment 交给页面处理 */
  (e: 'created', payload: CreateAgentResponse): void
}>()

const formRef = ref<FormInst | null>(null)
const submitting = ref(false)
/** 提交错误：重名等业务错误就地显示在表单里，比 toast 更好定位 */
const submitError = ref('')

/**
 * 表单值。
 *
 * 标签用「文本输入 + 解析」而不是键值对编辑器：多数人只打一两个标签，
 * 做成 key/value 表格反而增加操作成本。格式 `key=value`，逗号分隔。
 */
const form = reactive({
  name: '',
  note: '',
  labelsText: ''
})

/** 限制长度与后端字段长度保持一致，避免提交后才被拒 */
const rules: FormRules = {
  name: [
    { required: true, message: '请填写服务器名称', trigger: ['input', 'blur'] },
    { max: 64, message: '名称不能超过 64 个字符', trigger: ['input', 'blur'] }
  ],
  note: [{ max: 200, message: '备注不能超过 200 个字符', trigger: ['input', 'blur'] }]
}

/**
 * 把 `env=prod, role=web` 解析成 {env:'prod', role:'web'}。
 *
 * 忽略空片段与没有等号的片段（而不是报错）：
 * 用户随手多打一个逗号不该阻止提交，静默跳过更省事。
 */
function parseLabels(text: string): Record<string, string> {
  const out: Record<string, string> = {}
  for (const part of text.split(',')) {
    const trimmed = part.trim()
    if (trimmed === '') continue
    const index = trimmed.indexOf('=')
    if (index <= 0) continue
    const key = trimmed.slice(0, index).trim()
    const value = trimmed.slice(index + 1).trim()
    if (key !== '') out[key] = value
  }
  return out
}

const parsedLabels = computed(() => parseLabels(form.labelsText))

/** 显示名与 labels 的格式提示 */
const labelsHint = computed(() => {
  const keys = Object.keys(parsedLabels.value)
  if (keys.length === 0) return '可选，格式：env=prod, role=web'
  return `将保存 ${keys.length} 个标签：${keys.join('、')}`
})

/** 重置表单（打开时清掉上次的残留） */
function resetForm(): void {
  form.name = ''
  form.note = ''
  form.labelsText = ''
  submitError.value = ''
}

watch(
  () => props.show,
  (show) => {
    if (show) resetForm()
  }
)

/** 关闭弹窗 */
function handleClose(): void {
  if (submitting.value) return
  emit('update:show', false)
}

/** 提交创建 */
async function handleSubmit(): Promise<void> {
  if (submitting.value) return
  submitError.value = ''

  try {
    await formRef.value?.validate()
  } catch {
    // 校验失败：n-form 已经就地标红，不需要额外提示
    return
  }

  submitting.value = true
  try {
    const labels = parsedLabels.value
    const res = await createAgent({
      name: form.name.trim(),
      // 空备注不传，保持后端字段干净
      ...(form.note.trim() !== '' ? { note: form.note.trim() } : {}),
      ...(Object.keys(labels).length > 0 ? { labels } : {})
    })

    // 先关弹窗再把结果抛给页面：避免两个弹窗同时出现
    emit('update:show', false)
    emit('created', res)
  } catch (err) {
    if (err instanceof ApiError) {
      if (err.status === 409) {
        // 重名是可预期的高频错误，给具体建议而不是笼统报错
        submitError.value = `已有服务器使用了名称「${form.name.trim()}」。请换一个名称，或检查列表里是否已经建过。`
      } else {
        submitError.value = err.message || '创建服务器失败'
      }
    } else {
      submitError.value = '创建服务器失败'
    }
    // 注意：这里只取错误文案，绝不打印 err 对象——
    // 创建接口的响应里带密钥，整个对象进控制台就等于泄漏。
    toastError(submitError.value)
  } finally {
    submitting.value = false
  }
}
</script>

<template>
  <n-modal
    :show="show"
    preset="card"
    title="添加服务器"
    :style="{ maxWidth: '560px', width: '94vw' }"
    :bordered="false"
    :mask-closable="!submitting"
    @update:show="(v: boolean) => { if (!v) handleClose() }"
  >
    <!--
      必须先讲清楚「添加」是什么意思：这不是代替用户在服务器上装东西，
      而是先建好记录、发一把专属密钥，用户拿去那台机器上启动 Agent。
    -->
    <n-alert type="info" :bordered="false" class="add-agent__lead">
      这里会先在 Arkade 里创建好这台服务器的记录，并生成一把<strong>专属密钥</strong>。
      你需要在目标服务器上用这把密钥启动 Agent，它才会真正接进来。
      <br />
      服务器上的 Agent 是<strong>主动向外连接</strong> Arkade 的，因此不需要给服务器开放端口。
    </n-alert>

    <n-form ref="formRef" :model="form" :rules="rules" label-placement="top" size="small">
      <n-form-item label="服务器名称" path="name">
        <n-input
          v-model:value="form.name"
          placeholder="例如：东京-生产-01"
          :maxlength="64"
          show-count
          autofocus
        />
      </n-form-item>

      <n-form-item label="备注" path="note">
        <n-input
          v-model:value="form.note"
          type="textarea"
          placeholder="可选，例如：这台机器跑对外站点，勿动数据盘"
          :autosize="{ minRows: 2, maxRows: 4 }"
          :maxlength="200"
        />
      </n-form-item>

      <n-form-item label="标签">
        <n-input v-model:value="form.labelsText" placeholder="env=prod, role=web" />
        <template #feedback>
          <span class="add-agent__hint">{{ labelsHint }}</span>
        </template>
      </n-form-item>
    </n-form>

    <!-- 业务错误就地展示，比飘过的 toast 更容易对照着改 -->
    <n-alert v-if="submitError !== ''" type="error" :bordered="false" class="add-agent__error">
      {{ submitError }}
    </n-alert>

    <template #footer>
      <div class="add-agent__footer">
        <n-button size="small" :disabled="submitting" @click="handleClose">取消</n-button>
        <n-button size="small" type="primary" :loading="submitting" @click="handleSubmit">
          创建并生成密钥
        </n-button>
      </div>
    </template>
  </n-modal>
</template>

<style scoped>
.add-agent__lead {
  margin-bottom: 16px;
  line-height: 1.7;
}

.add-agent__hint {
  font-size: 12px;
  color: var(--arkade-text-tertiary);
}

.add-agent__error {
  margin-bottom: 8px;
  line-height: 1.6;
}

.add-agent__footer {
  display: flex;
  justify-content: flex-end;
  gap: 8px;
}
</style>
