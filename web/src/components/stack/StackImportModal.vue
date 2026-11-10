<script setup lang="ts">
/**
 * 导入纳管弹窗：把服务器上已有的 compose 项目纳入 Arkade 管理
 *
 * ## 为什么是「粘贴内容」而不是「读取服务器上的文件」
 *
 * Agent 通常跑在容器里，宿主上的 compose 文件路径它不一定读得到
 * （挂载范围有限，宿主机路径与容器内路径也不是一回事）。
 * 让它去读任意宿主路径本身还是个安全隐患。
 * 所以这里让用户把内容粘进来——虽然多一步，但**不会因为读不到文件而失败**，
 * 也不会为了读文件而放宽 Agent 的文件访问权限。
 *
 * ## 界面上必须讲清的三件事
 *
 * 1. **预览只是校验**：不保存、不启动、不停止任何东西；
 * 2. **导入只复制配置**：不自动 up/down/rebuild，容器状态不会因此改变；
 *    想让它跑起来要另行确认动作；
 * 3. **不支持的文件依赖直接拦死**：build、相对挂载、env_file、secrets 等
 *    依赖宿主上的其他文件，Agent 看不到这些文件，导进来也起不来。
 *    这类 blocker 不做「兼容」伪装——说清楚不能导，比导进去跑不起来好。
 *
 * ## 两道确认闸
 * 1) 必须先预检通过（can_import 为真）；
 * 2) 导入时必须带回预检得到的 `expected_content_hash` 并传 `confirmation: true`。
 * 内容在预检后被改过，哈希就对不上，后端会拒绝并要求重新预检。
 * 这样「预检通过的是 A、导入的是 B」不可能发生。
 */
import { computed, onUnmounted, ref, watch } from 'vue'
import { NAlert, NButton, NInput, NModal, NTag } from 'naive-ui'
import StackEnvEditor from '@/components/stack/StackEnvEditor.vue'
import CodeEditor from '@/components/common/CodeEditor.vue'
import { importStack, previewImport } from '@/api/stacks'
import type { ImportPreviewResponse } from '@/types/stack'
import { ApiError } from '@/types/api'
import { toastError, toastSuccess, toastWarning } from '@/utils/feedback'

interface Props {
  show: boolean
  agentId: string
  /** 外部项目名（预填） */
  projectName: string
}

const props = defineProps<Props>()

const emit = defineEmits<{
  (e: 'update:show', value: boolean): void
  /** 导入成功：通知列表刷新 */
  (e: 'imported'): void
}>()

/** 用户粘贴的内容 */
const name = ref('')
const content = ref('')
const envContent = ref('')

/** 预检结果；null 表示还没预检或内容已改需要重新预检 */
const preview = ref<ImportPreviewResponse | null>(null)
const previewing = ref(false)
const importing = ref(false)
const errorText = ref('')

/** 预检是否通过 */
const canImport = computed(() => preview.value?.can_import === true)

/**
 * 内容与预检的一致性靠 watch 保证：任何内容变动都把 preview 置空。
 *
 * 为什么用「改动即失效」而不是只在导入时比对哈希：
 * 让用户在界面上立刻看出「你改过了，得重新校验」，
 * 而不是点了导入才被后端拒回来。
 */
watch(
  () => [name.value, content.value, envContent.value] as const,
  () => {
    // 任何内容变动都作废旧预检——旧哈希已经不代表当前内容
    if (preview.value !== null) preview.value = null
  }
)

watch(
  () => props.show,
  (show) => {
    if (show) {
      // 打开时重置：不残留上一次的内容
      name.value = props.projectName
      content.value = ''
      envContent.value = ''
      preview.value = null
      errorText.value = ''
    }
  }
)

/** 关闭并清空（.env 可能有密码，不能在关掉后还留着） */
function handleClose(): void {
  if (previewing.value || importing.value) return
  clearSensitive()
  emit('update:show', false)
}

/** 清空敏感输入与预检状态（关闭、卸载、导入成功时调用） */
function clearSensitive(): void {
  content.value = ''
  envContent.value = ''
  preview.value = null
  errorText.value = ''
}

/**
 * 执行预检。
 *
 * ## 为什么要比对「发出去的内容」和「回来时的内容」
 *
 * 预检是异步的。用户点了预检、请求还在路上时又改了 compose 或 .env，
 * 等响应回来会把**旧内容**的预检结果写进 preview —— 界面显示「可以导入」，
 * 但真正要导入的是新内容。虽然后端会用 hash 拦下，但前端不该出现
 * 一个明显在撒谎的绿色「预检通过」。
 *
 * 因此发送前先把内容快照下来，响应回来时若已变化就直接丢弃这次结果。
 */
async function handlePreview(): Promise<void> {
  if (previewing.value || importing.value) return
  errorText.value = ''

  if (name.value.trim() === '') {
    errorText.value = '请填写编排名'
    return
  }
  if (content.value.trim() === '') {
    errorText.value = '请粘贴 compose.yml 内容'
    return
  }

  // 快照本次提交的内容，响应回来时比对
  const sent = {
    name: name.value.trim(),
    content: content.value,
    envContent: envContent.value
  }

  previewing.value = true
  try {
    const result = await previewImport(props.agentId, {
      name: sent.name,
      content: sent.content,
      env_content: sent.envContent
    })
    // 请求期间用户改了内容：这是陈旧结果，丢弃并提示重新预检
    if (
      name.value.trim() !== sent.name ||
      content.value !== sent.content ||
      envContent.value !== sent.envContent
    ) {
      preview.value = null
      errorText.value = '内容已改动，请重新预检'
      return
    }
    preview.value = result
  } catch (err) {
    preview.value = null
    errorText.value = err instanceof ApiError ? err.message : '预检失败'
    toastError(errorText.value)
  } finally {
    previewing.value = false
  }
}

/** 确认导入 */
async function handleImport(): Promise<void> {
  if (importing.value || !preview.value || !canImport.value) return
  errorText.value = ''
  importing.value = true
  try {
    const res = await importStack(props.agentId, {
      name: name.value.trim(),
      content: content.value,
      env_content: envContent.value,
      // 原样带回预检的哈希：内容若已变，后端会拒绝
      expected_content_hash: preview.value.config_hash,
      confirmation: true
    })

    /*
     * 三种结果要分开说：
     * - sync_ok && adopted：接管完成
     * - sync_ok 但 !adopted：配置已存且下发成功，但 Agent 未标记接管
     * - !sync_ok：配置已存 Hub，Agent 侧没接上，**此时不能启动**
     * 一律说「导入成功」会让用户以为已经纳管完毕并去点启动。
     */
    const fullyOk = res.sync_ok && res.adopted
    if (fullyOk) {
      toastSuccess('已导入纳管。未启动任何容器，需要运行时请另行操作。')
    } else if (res.sync_ok) {
      toastWarning(res.sync_hint ?? '配置已导入，但 Agent 未确认接管，请先确认状态再操作。')
    } else {
      toastWarning(
        res.sync_hint ?? '配置已保存到 Hub，但 Agent 接管未完成。请勿直接启动或重建，先排查连接。'
      )
    }

    /*
     * 列表刷新总是发出（后端已把项目记录进 Hub，列表该变），
     * 但**只有完全成功才关闭弹窗并清内容**。
     *
     * 为什么失败时要留着内容和弹窗：用户粘进来的是几十行 compose（还可能有 .env），
     * 一旦清掉就只能去服务器上重新找。记录虽然在 Hub 里，但用户无法原样重试。
     * 失败时只清预检结果（它已不代表当前状态），并让用户重新预检后再点导入。
     */
    emit('imported')

    if (fullyOk) {
      clearSensitive()
      emit('update:show', false)
    } else {
      // 保留输入；预检结果作废，必须重新预检才能再次导入
      preview.value = null
      errorText.value =
        '配置已保存到 Hub，但 Agent 未完成接管。内容已为你保留，可修改后重新预检并再次导入。'
    }
  } catch (err) {
    // 导入请求失败（含 403 无权限、409 冲突）：同样保留输入，只作废预检
    preview.value = null
    errorText.value = err instanceof ApiError ? err.message : '导入失败'
    toastError(errorText.value)
  } finally {
    importing.value = false
  }
}
/**
 * 组件卸载时清空敏感内容。
 *
 * 弹窗关闭路径已清过一次，但组件可能因路由切换、父组件重建等原因此外被卸载。
 * .env 里通常是密码，不该在组件销毁后还被闭包引用住。
 */
onUnmounted(() => {
  clearSensitive()
})
</script>

<template>
  <n-modal
    :show="show"
    preset="card"
    title="导入纳管"
    :style="{ maxWidth: '760px', width: '94vw' }"
    :bordered="false"
    :mask-closable="false"
    @update:show="(v: boolean) => { if (!v) handleClose() }"
  >
    <!--
      三件必须讲清的事放在最上面：
      用户对这个功能最容易误解的就是「导入=接管并启动」，
      实际只复制配置。误解会让他以为点完就在纳管下跑起来了。
    -->
    <n-alert type="info" :bordered="false" class="import__lead">
      把服务器上已有的项目纳入 Arkade 管理。需要**粘贴** compose 内容，
      因为 Agent 运行在容器里，通常读不到宿主机上的编排文件。
      <br />
      预检只做校验；<strong>导入只复制配置，不会启动、停止或重建任何容器</strong>。
    </n-alert>

    <div class="import__field">
      <label class="import__label">编排名</label>
      <n-input v-model:value="name" placeholder="与服务器上的项目名一致" :disabled="importing" />
    </div>

    <div class="import__field">
      <label class="import__label">compose.yml</label>
      <code-editor
        v-model="content"
        aria-label="compose.yml 内容"
        :disabled="importing"
        :min-height="280"
        :max-height="420"
      />
    </div>

    <!-- .env 可选：默认收起+打码，内容不落任何存储 -->
    <div class="import__field">
      <stack-env-editor v-model="envContent" :disabled="importing" />
    </div>

    <!-- 预检结果 -->
    <template v-if="preview !== null">
      <!-- 阻塞项：不可导入，逐条列出原因 -->
      <n-alert
        v-if="preview.blockers.length > 0"
        type="error"
        :bordered="false"
        title="无法导入：存在不支持的文件依赖"
        class="import__result"
      >
        <ul class="import__list">
          <li v-for="(item, index) in preview.blockers" :key="index">{{ item }}</li>
        </ul>
        <p class="import__list-note">
          这些依赖指向服务器上的其他文件，Arkade 无法保证它们存在且一致，因此不做导入。
          请先调整 compose 内容（例如改成绝对路径的具名卷、或去掉 build）后重新预检。
        </p>
      </n-alert>

      <!-- 警告项：可导入但有风险 -->
      <n-alert
        v-if="preview.warnings.length > 0"
        type="warning"
        :bordered="false"
        title="需要注意"
        class="import__result"
      >
        <ul class="import__list">
          <li v-for="(item, index) in preview.warnings" :key="index">{{ item }}</li>
        </ul>
      </n-alert>

      <n-alert
        v-if="canImport"
        type="success"
        :bordered="false"
        class="import__result"
        title="预检通过，可以导入"
      >
        导入后配置会复制到 Arkade 管理区，容器保持当前状态不变。
      </n-alert>
    </template>

    <!-- 错误提示 -->
    <n-alert v-if="errorText !== ''" type="error" :bordered="false" class="import__result">
      {{ errorText }}
    </n-alert>

    <template #footer>
      <div class="import__footer">
        <n-tag v-if="preview !== null" size="small" :bordered="false" type="info">
          内容已变动需重新预检则此状态会消失
        </n-tag>
        <n-button size="small" :disabled="importing" @click="handleClose">取消</n-button>
        <n-button size="small" :loading="previewing" :disabled="importing" @click="handlePreview">
          预检
        </n-button>
        <n-button
          size="small"
          type="primary"
          :loading="importing"
          :disabled="!canImport || previewing"
          @click="handleImport"
        >
          确认导入
        </n-button>
      </div>
    </template>
  </n-modal>
</template>

<style scoped>
.import__lead {
  margin-bottom: 16px;
  line-height: 1.7;
}

.import__field {
  margin-bottom: 14px;
}

.import__label {
  display: block;
  margin-bottom: 6px;
  font-size: 13px;
  font-weight: 500;
  color: var(--arkade-text-primary);
}

.import__code :deep(textarea) {
  font-family: var(--arkade-mono-font);
  font-size: 12px;
  line-height: 1.6;
}

.import__result {
  margin-bottom: 12px;
  line-height: 1.7;
}

.import__list {
  margin: 0;
  padding-left: 18px;
}

.import__list li {
  margin-bottom: 4px;
}

.import__list-note {
  margin: 8px 0 0;
  font-size: 12px;
  color: var(--arkade-text-tertiary);
}

.import__footer {
  display: flex;
  align-items: center;
  justify-content: flex-end;
  gap: 8px;
  flex-wrap: wrap;
}
</style>