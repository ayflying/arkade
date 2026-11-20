<script setup lang="ts">
/**
 * 专属密钥展示弹窗（创建成功后 / 重置密钥后）
 *
 * ## 这个组件的唯一职责：让密钥**只存在这一次**
 *
 * 密钥是明文、后端只存摘要、丢失后无法找回，只能重置。
 * 所以这里的每一条行为都是围绕「不让它意外留存」设计的：
 *
 * 1. **不写 Store / localStorage / sessionStorage / URL**：
 *    密钥只在本组件的局部 ref 里，父组件通过 props 传进来，关闭即废弃。
 * 2. **不 console.log / console.error**：
 *    连错误处理都不打印对象——很多人顺手 log 整个 err 或 res，
 *    密钥就跟着进了浏览器日志。这里所有捕获都只取 message。
 * 3. **关闭即清空**：
 *    `handleClose` 先把 token ref 置空再 emit，即使父组件忘了清理也不会残留。
 * 4. **不允许点遮罩/按 Esc 关闭**：
 *    密钥弹窗是「一次性信息」，误触关闭后用户只能重置密钥（会让已部署的 Agent 掉线）。
 *    必须让用户显式点「我已保存」。
 *
 * ## 为什么不是简单的复制框
 *
 * 直接给出可复制的单文件部署配置。
 */
import { computed, ref, watch } from 'vue'
import { NAlert, NButton, NInput, NModal, NTag } from 'naive-ui'
import { buildComposeSnippet, guessHubUrl, validateHubUrl } from '@/utils/agentDeploy'
import type { Enrollment } from '@/types/agent'
import { copyToClipboard, selectElementText } from '@/utils/clipboard'
import { toastError, toastSuccess } from '@/utils/feedback'

interface Props {
  show: boolean
  /** 一次性密钥；关闭后由本组件与父组件共同清空 */
  enrollment: Enrollment | null
  /** 服务器显示名，用于生成编排里的 ARKADE_AGENT_NAME */
  agentName: string
  /** 是否是重置密钥（影响文案与提醒：旧密钥已失效） */
  isReset?: boolean
}

const props = withDefaults(defineProps<Props>(), { isReset: false })

const emit = defineEmits<{
  (e: 'update:show', value: boolean): void
  /** 关闭后通知父组件丢弃密钥引用 */
  (e: 'closed'): void
}>()

/**
 * 本地副本。
 *
 * 拿着 props 里的 token 另存一份是为了能在关闭时**主动清空**——
 * 父组件持有引用是父组件的事，本组件至少要保证自己不再留。
 */
const token = ref('')

/** Hub 地址：从当前页面推断初值，用户可改 */
const hubUrl = ref('')
/** 是否已确认「我已知晓密钥只显示一次」——勾选后才显示片段，降低误关风险 */
const acknowledged = ref(false)

/** 编排片段与密钥输入框：复制失败时用它们把文本选中，交回用户 Ctrl+C */
const snippetRef = ref<HTMLElement | null>(null)
const tokenFieldRef = ref<HTMLElement | null>(null)

/** n-input 内部才是原生 <input>，选中要落到它身上 */
function tokenInputElement(): HTMLElement | null {
  const root = tokenFieldRef.value
  if (!root) return null
  return root instanceof HTMLInputElement ? root : root.querySelector('input')
}

const hubUrlError = computed(() => validateHubUrl(hubUrl.value))
const composeSnippet = computed(() => {
  if (token.value === '' || hubUrlError.value !== '') return ''
  return buildComposeSnippet({ agentName: props.agentName, token: token.value, hubUrl: hubUrl.value })
})

/**
 * 同步 props → 本地。watch immediate + 深度取 token 字符串本身。
 *
 * 重置场景下 token 会变，这个 watch 保证拿到的是最新的那份。
 */
watch(
  () => [props.show, props.enrollment] as const,
  ([show, enrollment]) => {
    if (show && enrollment) {
      token.value = enrollment.token
      // 只在首次打开时给初值，避免覆盖用户已经改过的地址
      if (hubUrl.value === '') hubUrl.value = guessHubUrl()
    }
  },
  { immediate: true }
)

/**
 * 复制到剪贴板。
 *
 * 必须看返回值：内网 http 是非安全上下文，没有 Clipboard API，
 * 只能靠 execCommand，而它失败时只返回 false、不抛异常。
 * 以前这里无条件提示「已复制」，用户粘出来的其实是上一次复制的密钥。
 *
 * 失败时把目标文本选中交给用户：只提示「请手动复制」是不够的，
 * 用户选中困难时仍会用旧剪贴板内容，问题看起来就和没修一样。
 */
async function copyText(text: string, label: string, selectable?: HTMLElement | null): Promise<void> {
  if (text === '') return
  if (await copyToClipboard(text)) {
    toastSuccess(`${label}已复制`)
    return
  }
  if (selectable) {
    selectElementText(selectable)
    toastError(`浏览器阻止了自动复制，内容已选中，请按 Ctrl+C 复制`)
    return
  }
  toastError(`浏览器阻止了自动复制，请手动选中${label}内容后按 Ctrl+C 复制`)
}

/** 关闭：先清空本地密钥，再通知父组件 */
function handleClose(): void {
  token.value = ''
  hubUrl.value = ''
  acknowledged.value = false
  emit('update:show', false)
  emit('closed')
}
</script>

<template>
  <!--
    刻意不设置 mask-closable：误触遮罩关掉这个弹窗的代价是
    用户必须重置密钥（已部署的 Agent 会掉线），这个代价太高，
    必须让用户显式点「我已保存密钥」。
  -->
  <n-modal
    :show="show"
    preset="card"
    :title="isReset ? '新的专属密钥' : '服务器已创建，请保存专属密钥'"
    :style="{ maxWidth: '760px', width: '94vw' }"
    :bordered="false"
    :mask-closable="false"
    :close-on-esc="false"
    :closable="false"
    @update:show="(v: boolean) => { if (!v) handleClose() }"
  >
    <!--
      一次性提醒必须最显眼：这是整个弹窗存在的唯一理由。
      用 warning 而不是 info，因为「关掉就再也看不到」是真实损失。
    -->
    <n-alert type="warning" :bordered="false" title="密钥只显示这一次" class="enroll__lead">
      Arkade 不保存密钥原文，<strong>关闭这个窗口后就无法再次查看</strong>。
      请立刻复制并保存到安全的地方；如果丢失，只能在服务器列表里重置密钥，
      届时正在运行的 Agent 会断开，直到你更新它的部署配置。
    </n-alert>

    <!-- 密钥本体 -->
    <div class="enroll__token-row">
      <div ref="tokenFieldRef" class="enroll__token-field">
        <n-input
          :value="token"
          readonly
          size="large"
          class="enroll__token"
          @focus="(e: FocusEvent) => (e.target as HTMLInputElement).select()"
        />
      </div>
      <n-button type="primary" size="large" @click="copyText(token, '密钥', tokenInputElement())">复制密钥</n-button>
    </div>

    <div class="enroll__field">
      <label class="enroll__label">
        Hub 地址
        <span class="enroll__label-hint">这台服务器能访问到的 Arkade 地址</span>
      </label>
      <n-input
        v-model:value="hubUrl"
        placeholder="ws://10.0.0.5:8080/agent/connect"
        :status="hubUrlError !== '' ? 'error' : undefined"
      />
      <p v-if="hubUrlError !== ''" class="enroll__error">{{ hubUrlError }}</p>
      <p v-else class="enroll__tip">
        默认按当前访问地址推断。如果服务器在另一个网络，请改成它实际能访问到的地址
        （例如内网 IP 或域名），并确保带上 /agent/connect。
      </p>
    </div>

    <!-- 未勾选前不展示片段：让用户先确认已保存密钥，避免边看片段边把密钥窗口关掉 -->
    <div v-if="!acknowledged" class="enroll__ack">
      <n-button size="small" type="primary" @click="acknowledged = true">
        我已保存密钥，查看部署步骤
      </n-button>
    </div>

    <template v-else>
      <div class="enroll__snippet-header">
        <strong>docker-compose.yml</strong>
        <n-button size="tiny" quaternary :disabled="composeSnippet === ''" @click="copyText(composeSnippet, '配置', snippetRef)">
          复制编排文件
        </n-button>
      </div>

      <pre v-if="composeSnippet !== ''" ref="snippetRef" class="enroll__code">{{ composeSnippet }}</pre>

      <n-alert type="info" :bordered="false" class="enroll__steps">
        <ol class="enroll__ol">
          <li>在目标服务器上把上面的配置保存为 <code>docker-compose.yml</code>。</li>
          <li>在文件所在目录执行 <code>docker compose up -d</code> 启动 Agent。</li>
          <li>
            回到 Arkade 的服务器列表，等状态从「待接入」变成「在线」。
            Agent 连接成功后状态会变为在线；开启「自动刷新」时列表约 5 秒更新一次，
            关闭时请手动点「刷新」。
          </li>
        </ol>
      </n-alert>

      <p class="enroll__note">
        编排文件包含专属密钥，请妥善保管，不要分享或提交到版本库。
      </p>
    </template>

    <template #footer>
      <div class="enroll__footer">
        <n-tag v-if="!acknowledged" size="small" :bordered="false" type="warning">
          请先保存密钥
        </n-tag>
        <n-button size="small" @click="handleClose">
          {{ acknowledged ? '我已保存密钥，关闭' : '关闭' }}
        </n-button>
      </div>
    </template>
  </n-modal>
</template>

<style scoped>
.enroll__lead {
  margin-bottom: 16px;
  line-height: 1.7;
}

/* 密钥输入 + 复制按钮：窄屏换行，避免挤压密钥显示区 */
.enroll__token-row {
  display: flex;
  gap: 8px;
  margin-bottom: 16px;
  flex-wrap: wrap;
}

.enroll__token {
  flex: 1;
  min-width: 240px;
}

/* 包裹层只为拿到原生 input 做「复制失败后选中」，flex 规则跟着它走，布局保持不变 */
.enroll__token-field {
  flex: 1;
  min-width: 240px;
  display: flex;
}

/* 密钥用等宽字体：容易分辨 0/O、1/l 这类易混字符 */
.enroll__token :deep(input) {
  font-family: var(--arkade-mono-font);
  letter-spacing: 0.02em;
}

.enroll__field {
  margin-bottom: 14px;
}

.enroll__label {
  display: block;
  margin-bottom: 6px;
  font-size: 13px;
  font-weight: 500;
  color: var(--arkade-text-primary);
}

.enroll__label-hint {
  margin-left: 6px;
  font-weight: 400;
  font-size: 12px;
  color: var(--arkade-text-tertiary);
}

.enroll__error {
  margin: 6px 0 0;
  font-size: 12px;
  color: var(--arkade-danger);
}

.enroll__tip {
  margin: 6px 0 0;
  font-size: 12px;
  line-height: 1.6;
  color: var(--arkade-text-tertiary);
}

.enroll__ack {
  margin: 4px 0 16px;
}

.enroll__snippet-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
  margin-bottom: 8px;
  flex-wrap: wrap;
}

.enroll__code {
  margin: 0 0 14px;
  padding: 12px 14px;
  max-height: 320px;
  overflow: auto;
  background: var(--arkade-code-bg-alt);
  color: var(--arkade-code-text);
  border-radius: 6px;
  font-family: var(--arkade-mono-font);
  font-size: 12px;
  line-height: 1.65;
  white-space: pre;
  tab-size: 2;
}

.enroll__steps {
  line-height: 1.8;
  margin-bottom: 10px;
}

.enroll__ol {
  margin: 0;
  padding-left: 18px;
}

.enroll__ol li {
  margin-bottom: 6px;
}

/* 行内代码：技术标识用等宽字体，与正文区分 */
.enroll__steps code {
  padding: 1px 4px;
  font-family: var(--arkade-mono-font);
  font-size: 11px;
  background: rgba(0, 0, 0, 0.06);
  border-radius: 3px;
}

.enroll__note {
  margin: 0;
  font-size: 12px;
  line-height: 1.7;
  color: var(--arkade-text-tertiary);
}

.enroll__footer {
  display: flex;
  align-items: center;
  justify-content: flex-end;
  gap: 8px;
}

@media (max-width: 767.98px) {
  /* 手机：密钥输入独占一行，复制按钮拉满，避免横向挤压 */
  .enroll__token,
  .enroll__token-field {
    min-width: 100%;
  }

  .enroll__token-row :deep(.n-button) {
    width: 100%;
  }

  .enroll__code {
    max-height: 240px;
    font-size: 11px;
  }
}
</style>
