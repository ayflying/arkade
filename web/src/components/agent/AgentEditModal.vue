<script setup lang="ts">
/**
 * 编辑服务器弹窗（显示名 / 备注 / 标签）
 *
 * ## 为什么只让改这三个字段
 * - `id`、`status`、`last_seen` 等是 Agent 自报的运行时数据，改了没意义也会被下次心跳覆盖；
 * - `name` 是 Agent 自报的原始名，同样会被覆盖，所以「改名字」走 display_name。
 *
 * ## 标签的编辑方式
 * 用「多行文本，每行 key=value」而不是动态增删的键值对控件：
 * 1. 不引入新依赖，也不写一堆增删行的交互逻辑；
 * 2. 批量改标签时（运维常见操作）直接编辑文本比一行行点快得多；
 * 3. 解析规则简单易懂，出错时能明确指出是第几行。
 *
 * 格式错误不会静默丢弃——用户敲错一个字符却看到「保存成功」，
 * 标签就悄悄少了一个，这种失败最难排查。
 */
import { computed, ref, watch } from 'vue'
import { NAlert, NButton, NForm, NFormItem, NInput, NModal } from 'naive-ui'
import { updateAgent } from '@/api/agents'
import type { Agent, UpdateAgentRequest } from '@/types/agent'
import { ApiError } from '@/types/api'
import { toastSuccess } from '@/utils/feedback'

interface Props {
  /** 是否显示；用 v-model:show 由父组件控制 */
  show: boolean
  /** 要编辑的服务器；null 时不渲染内容 */
  agent: Agent | null
}

const props = defineProps<Props>()

const emit = defineEmits<{
  (e: 'update:show', value: boolean): void
  /**
   * 保存成功：把后端返回的**更新后的完整对象**交给父组件。
   *
   * 传对象而不是只发一个「saved」事件，是为了让父组件直接替换本地那一行，
   * 省掉一次整表刷新（PATCH 已经把最新值带回来了）。
   */
  (e: 'saved', updated: Agent): void
}>()

const displayName = ref('')
const note = ref('')
/** 标签文本，每行 key=value */
const labelsText = ref('')
/**
 * 直连地址：用户在浏览器里能打开这台服务器的地址。
 *
 * 只填主机部分（`10.0.0.5` 或 `[2001:db8::1]`），不要带 http://、路径或端口。
 * 容器的端口链接靠它把 `0.0.0.0` 这种通配绑定换成真实可达的地址。
 */
const directAddress = ref('')
const saving = ref(false)
const errorText = ref('')

/**
 * 标签输入框的占位提示。
 *
 * 用字符串常量 + 模板绑定而不是写在 placeholder 属性里：
 * HTML 属性里的 `&#10;` 不会被解码成换行，多行示例需要用真实的 \n。
 */
const LABELS_PLACEHOLDER = '每行一个，格式为 键=值，例如：\nenv=prod\nregion=cn-east'

/** 把 labels 对象转成「每行 key=value」的文本 */
function labelsToText(labels: Record<string, string>): string {
  return Object.entries(labels)
    .map(([k, v]) => `${k}=${v}`)
    .join('\n')
}

/**
 * 解析标签文本。
 *
 * 规则：逐行 trim，空行跳过，按**第一个** `=` 切分（值里可以再出现 =，例如 URL）。
 * 任一非法行都返回错误而不是跳过——见文件头注释的理由。
 */
function parseLabels(text: string): { ok: true; labels: Record<string, string> } | { ok: false; message: string } {
  const labels: Record<string, string> = {}
  const lines = text.split('\n')
  for (let i = 0; i < lines.length; i += 1) {
    const line = (lines[i] ?? '').trim()
    if (line === '') continue
    const eq = line.indexOf('=')
    if (eq <= 0) {
      return { ok: false, message: `第 ${i + 1} 行格式不正确，应为 键=值 的形式：${line}` }
    }
    const key = line.slice(0, eq).trim()
    const value = line.slice(eq + 1).trim()
    if (key === '') {
      return { ok: false, message: `第 ${i + 1} 行缺少标签名：${line}` }
    }
    labels[key] = value
  }
  return { ok: true, labels }
}

/** 打开时用当前 Agent 的值填充表单；关闭时清掉错误，避免下次打开看到旧报错 */
watch(
  () => [props.show, props.agent] as const,
  ([show, agent]) => {
    if (!show) return
    errorText.value = ''
    saving.value = false
    displayName.value = agent?.display_name ?? ''
    note.value = agent?.note ?? ''
    labelsText.value = labelsToText(agent?.labels ?? {})
    directAddress.value = agent?.direct_address ?? ''
  },
  { immediate: true }
)

/** 显示名留空时后端会自动回落成 Agent 原名，因此占位符只是给个提示 */
const displayNamePlaceholder = computed(() => props.agent?.name ?? '')

function close(): void {
  emit('update:show', false)
}

/**
 * 保存。
 *
 * 只提交**改动过**的字段（PATCH 语义）：
 * 把没动的字段一起回写，会在多标签页同时编辑时互相覆盖。
 */
async function handleSave(): Promise<void> {
  if (props.agent === null || saving.value) return
  errorText.value = ''

  const parsed = parseLabels(labelsText.value)
  if (!parsed.ok) {
    errorText.value = parsed.message
    return
  }

  const payload: UpdateAgentRequest = {}

  const nextDisplay = displayName.value.trim()
  const prevDisplay = (props.agent.display_name ?? '').trim()
  // display_name 的清空回落到 name 由后端负责，前端只需如实提交空串。
  // 这里仍然按「内容是否变化」判断，避免没改也提交一次空值覆盖。
  if (nextDisplay !== prevDisplay) payload.display_name = nextDisplay

  const nextNote = note.value.trim()
  const prevNote = (props.agent.note ?? '').trim()
  if (nextNote !== prevNote) payload.note = nextNote

  // 标签按序列化结果比较：对象引用不同但内容相同不应触发提交
  const prevLabelsText = labelsToText(props.agent.labels ?? {})
  const nextLabelsText = labelsToText(parsed.labels)
  if (nextLabelsText !== prevLabelsText) payload.labels = parsed.labels

  /*
   * 直连地址：只做 trim，格式交给后端校验。
   *
   * 前端不自己拦格式，是因为规则会随需求演进（IPv6、域名、端口），
   * 两边各写一套校验迟早不一致；而后端拒绝时给出的原文更准确。
   * 清空同样如实提交空串，让后端决定是否允许。
   */
  const nextAddress = directAddress.value.trim()
  const prevAddress = (props.agent.direct_address ?? '').trim()
  if (nextAddress !== prevAddress) payload.direct_address = nextAddress

  if (Object.keys(payload).length === 0) {
    // 没有任何改动：直接关闭，不发一个空 PATCH 制造无意义的审计记录
    toastSuccess('未做任何修改')
    close()
    return
  }

  saving.value = true
  try {
    const updated = await updateAgent(props.agent.id, payload)
    toastSuccess('服务器信息已更新')
    emit('saved', updated)
    close()
  } catch (err) {
    errorText.value = err instanceof ApiError ? err.message : '保存失败，请稍后重试'
  } finally {
    saving.value = false
  }
}
</script>

<template>
  <n-modal
    :show="show"
    preset="card"
    title="编辑服务器"
    class="agent-edit-modal"
    :style="{ maxWidth: '560px', width: '92vw' }"
    :bordered="false"
    @update:show="(v: boolean) => emit('update:show', v)"
  >
    <n-form label-placement="top">
      <n-form-item label="服务器 ID">
        <!-- ID 不可改：它是 Agent 上报的标识，改动会牵动所有历史记录与外键 -->
        <n-input :value="agent?.id ?? ''" disabled />
      </n-form-item>

      <n-form-item label="显示名">
        <n-input
          v-model:value="displayName"
          :placeholder="displayNamePlaceholder"
          :disabled="saving"
          clearable
        />
      </n-form-item>
      <div class="agent-edit-modal__hint">
        留空则显示 Agent 上报的原始名称。
      </div>

      <n-form-item label="直连地址">
        <n-input
          v-model:value="directAddress"
          placeholder="例如：10.0.0.12 或 [2001:db8::1]"
          :disabled="saving"
          clearable
        />
      </n-form-item>
      <div class="agent-edit-modal__hint">
        你在浏览器里访问这台服务器用的地址，可留空。只填主机，<strong>不要带 http:// 、路径或端口</strong>；
        IPv6 用方括号形式。容器的端口链接会用它替代 0.0.0.0 这类通配地址。
      </div>

      <n-form-item label="备注">
        <n-input
          v-model:value="note"
          type="textarea"
          :autosize="{ minRows: 2, maxRows: 4 }"
          placeholder="例如：生产环境主库所在机器，变更需走审批"
          :disabled="saving"
        />
      </n-form-item>

      <n-form-item label="标签">
        <n-input
          v-model:value="labelsText"
          type="textarea"
          class="agent-edit-modal__labels"
          :autosize="{ minRows: 3, maxRows: 8 }"
          :placeholder="LABELS_PLACEHOLDER"
          :disabled="saving"
          spellcheck="false"
        />
      </n-form-item>
      <div class="agent-edit-modal__hint">
        每行一个标签，格式为 键=值。格式不正确时会提示具体行号，不会静默丢弃。
      </div>

      <n-alert v-if="errorText !== ''" type="error" :bordered="false" class="agent-edit-modal__error">
        {{ errorText }}
      </n-alert>
    </n-form>

    <template #footer>
      <div class="agent-edit-modal__footer">
        <n-button size="small" :disabled="saving" @click="close">取消</n-button>
        <n-button size="small" type="primary" :loading="saving" @click="handleSave">保存</n-button>
      </div>
    </template>
  </n-modal>
</template>

<style scoped>
.agent-edit-modal__hint {
  margin: -14px 0 14px;
  font-size: 12px;
  color: #8b9099;
  line-height: 1.6;
}

/* 标签用等宽字体：键值对里冒号、等号在等宽字体下更容易对齐检查 */
.agent-edit-modal__labels :deep(textarea) {
  font-family: var(--arkade-mono-font);
}

.agent-edit-modal__error {
  margin-top: 4px;
}

.agent-edit-modal__footer {
  display: flex;
  justify-content: flex-end;
  gap: 8px;
}
</style>
