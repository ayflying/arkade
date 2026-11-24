<script setup lang="ts">
/**
 * 卷内文件查看 / 编辑面板
 *
 * 四种状态互斥：
 * 1. **文本**：可预览、可编辑、可保存（必须带 expected_sha256）；
 * 2. **二进制**：拒绝编辑，只允许下载原件；
 * 3. **超过在线查看上限**（> 2 MiB）：不打开编辑器，只给下载；
 * 4. **超过单文件上限**（> 4 MiB）：内容根本进不来，连下载也不可用。
 *
 * ## 为什么二进制必须拒绝编辑而不是「显示乱码让用户自己判断」
 *
 * 卷里最常见的是数据库文件。UTF-8 解码会把非法字节变成 U+FFFD，
 * 这一步就在内存里**已经损坏了内容**；用户存回去等于把数据库写坏，
 * 而且不可恢复。所以判定必须在解码之前完成，判为二进制就完全不给编辑器。
 *
 * ## 未保存保护
 *
 * 「有改动时切路径/关面板」必须确认。这里不自己弹窗——由父组件在
 * 导航前调用 `confirmLeave()`，因为决定权在父组件（它知道用户要去哪）。
 * 本组件只提供 `isDirty` 与 `reset()`。
 */
import { computed, ref, watch } from 'vue'
import { NAlert, NButton, NSpin } from 'naive-ui'
import { formatBytes } from '@/utils/formatSize'
import { base64ToText } from '@/utils/base64'
import { looksBinaryBase64 } from '@/utils/binaryDetect'
import type { VolumeFileResult } from '@/types/volumeFile'
import {
  VOLUME_FILE_MAX_BYTES,
  VOLUME_FILE_OPEN_MAX_BYTES,
  volumeFileViewLevel
} from '@/types/volumeFile'

interface Props {
  /** 已读到的文件结果；null 表示还没读或已关闭 */
  result: VolumeFileResult | null
  loading: boolean
  saving: boolean
  /** 读取失败的文案 */
  errorText: string
  /**
   * 文件超过单文件上限（4 MiB）。
   *
   * 重要：后端 read 对超限文件**直接拒绝**，没有单独的流式下载端点。
   * 所以这里**不提供下载**——给一个必然失败的按钮比不给更糟。
   * 只说明上限值并引导用户到服务器上用命令行处理。
   */
  tooLarge?: boolean
  /**
   * 文件超过在线查看上限（2 MiB）但没超过单文件上限：不打开编辑器，只允许下载。
   *
   * 此时 `result` 里只有元信息（path/size），**没有内容**——内容要等用户
   * 真的点下载时再去读。这样点开一个 3 MB 的文件不会先占住一个 helper
   * 并发槽位、传完几 MB 才告诉他「打不开」。
   */
  downloadOnly?: boolean
}

const props = withDefaults(defineProps<Props>(), { tooLarge: false, downloadOnly: false })

const emit = defineEmits<{
  save: [content: string, expectedSha256: string]
  download: []
  close: []
  retry: []
  cancel: []
}>()

/** 编辑器里的当前文本 */
const draft = ref('')
/** 读到的原始文本（用于判断是否有改动） */
const original = ref('')
/** 原内容的 sha256，保存时回传做乐观锁 */
const readSha256 = ref('')

/**
 * 文件内容的 base64。
 *
 * ## 为什么缺省当作空串
 *
 * 后端的 `data_base64` 带 `omitempty`：**空文件（0 字节）的 base64 就是空串，
 * 会被直接省略**。如果把它当「没有内容」处理，结果是一个完全合法的空配置
 * 文件显示成「读不到内容」，既不能编辑也不能下载——用户会以为文件坏了。
 *
 * 空文件是常态（占位配置、`.gitkeep`、刚建出来的文件），必须正常打开。
 */
const contentBase64 = computed<string>(() => props.result?.data_base64 ?? '')

/**
 * 是否二进制。
 *
 * 判定基于 base64 原文，**不是**先解码成字符串——那一步会污染数据。
 * 注意空串必须判为「不是二进制」：空文件没有任何字节，谈不上二进制。
 */
const isBinary = computed<boolean>(() => {
  if (props.result === null) return false
  return looksBinaryBase64(contentBase64.value)
})

/** 按实际字节数判定的在线查看级别（与 store 用同一个函数） */
const viewLevel = computed(() => volumeFileViewLevel(props.result?.size))

/** 是否超过单文件上限（超限时后端 read 已拒绝，没有内容可下载） */
const isTooLarge = computed<boolean>(() => props.tooLarge || viewLevel.value === 'blocked')

/**
 * 是否只允许下载（超过在线查看上限，但没超过单文件上限）。
 *
 * 标记由父组件给出；这里再用 size 兜一层，避免有人只传 result 忘了标记时
 * 把几 MB 的内容塞进编辑器。
 */
const isDownloadOnly = computed<boolean>(
  () => props.downloadOnly || viewLevel.value === 'download'
)

/**
 * 可以下载。
 *
 * 文本与二进制都要能下载：这是文件管理器，用户需要把文件拿走，
 * 而不是只能在线看一眼。唯一例外是超限——后端 read 已拒绝，
 * 手里根本没有内容，给按钮只会点出一次失败。
 */
const canDownload = computed(() => props.result !== null && !isTooLarge.value)

/** 可以展示文本编辑器 */
const canEdit = computed(
  () => props.result !== null && !isBinary.value && !isTooLarge.value && !isDownloadOnly.value
)

/** 有未保存改动 */
const isDirty = computed(() => canEdit.value && draft.value !== original.value)

/** 允许保存：可编辑 + 有改动 + 有原始哈希（后端据此防冲突）+ 不在保存中 */
const canSave = computed(
  () => canEdit.value && isDirty.value && readSha256.value !== '' && !props.saving
)

/** 内容大小提示（按当前草稿估算，让用户知道离上限还有多远） */
const draftSize = computed(() => new TextEncoder().encode(draft.value).length)

/**
 * 把读到的结果装载进编辑器。
 *
 * 只有「可编辑」时才解码；二进制/超大一律不碰数据，
 * 保证这两类文件的内容在整个生命周期里都是未解码的原始 base64。
 */
watch(
  () => props.result,
  (result) => {
    if (result === null) {
      draft.value = ''
      original.value = ''
      readSha256.value = ''
      return
    }
    readSha256.value = result.sha256 ?? ''
    if (canEdit.value) {
      // 空文件 base64 被省略 → 当作空串解码，得到空编辑器而不是「打不开」
      const text = base64ToText(contentBase64.value)
      original.value = text
      draft.value = text
    } else {
      // 二进制/超大：不解码、不填充编辑器
      original.value = ''
      draft.value = ''
    }
  },
  { immediate: true }
)

/** 保存：把草稿编码回 base64 交给父组件 */
function handleSave(): void {
  if (!canSave.value) return
  emit('save', draft.value, readSha256.value)
}

/** 丢弃改动，回到读到的版本 */
function handleRevert(): void {
  draft.value = original.value
}

/** 父组件保存成功后调用：把当前草稿当作新的基线 */
function markSaved(newResult: VolumeFileResult): void {
  original.value = draft.value
  if (newResult.sha256 !== undefined) readSha256.value = newResult.sha256
}

defineExpose({ isDirty, markSaved, readSha256 })
</script>

<template>
  <div class="file-panel">
    <!--
      读取中给出明确出口。
      没有取消按钮时，用户唯一的"放弃"方式是切走页面——那正是以前把
      Agent 并发槽位占死的原因（请求还在服务端跑，界面已经没人看）。
    -->
    <div v-if="loading" class="file-panel__loading">
      <n-spin size="small" description="正在读取文件…" class="file-panel__spin" />
      <n-button size="tiny" quaternary @click="emit('cancel')">取消</n-button>
    </div>

    <div v-else-if="errorText !== ''" class="file-panel__error">
      <n-alert type="error" :bordered="false">{{ errorText }}</n-alert>
      <n-button size="small" quaternary @click="emit('retry')">重试</n-button>
    </div>

    <template v-else-if="result !== null">
      <!--
        超过单文件上限：内容根本进不来，连下载也不可用。
        先判大小再看内容——大小是最确定的信息，二进制判定要读字节。
      -->
      <n-alert v-if="isTooLarge" type="warning" :bordered="false" class="file-panel__notice">
        文件大小 {{ formatBytes(result.size ?? 0) }}，超过单文件上限
        {{ formatBytes(VOLUME_FILE_MAX_BYTES) }}。
        这里<strong>无法打开也无法下载</strong>它——请在服务器上直接用命令行工具处理。
      </n-alert>

      <!--
        超过在线查看上限（2 MiB）：不打开编辑器，只允许下载。
        这类文件几乎都是日志、导出数据、数据库文件，该下载到本地用合适的工具看。
      -->
      <n-alert v-else-if="isDownloadOnly" type="info" :bordered="false" class="file-panel__notice">
        文件大小 {{ formatBytes(result.size ?? 0) }}，超过在线查看上限
        {{ formatBytes(VOLUME_FILE_OPEN_MAX_BYTES) }}，因此<strong>不在浏览器里打开</strong>。
        点下面的<strong>下载</strong>拿到原件后用本地工具查看。
      </n-alert>

      <!--
        二进制：拒绝编辑并说明原因。
        这不是技术限制的托词，而是关键安全提示——
        卷里多半是数据库文件，用文本编辑器打开再保存会直接损坏数据。
      -->
      <n-alert v-else-if="isBinary" type="warning" :bordered="false" class="file-panel__notice">
        这是一个<strong>二进制文件</strong>（{{ formatBytes(result.size ?? 0) }}），无法在此编辑。
        卷内常见的是数据库、图片或压缩数据，用文本方式打开后再保存会<strong>直接损坏文件</strong>。
        如需修改请下载原件并用相应工具处理。
      </n-alert>

      <!-- 文本编辑 -->
      <template v-else>
        <div class="file-panel__meta">
          <span>UTF-8 文本 · {{ formatBytes(draftSize) }}</span>
          <span v-if="isDirty" class="file-panel__dirty">有未保存的修改</span>
        </div>
        <textarea
          v-model="draft"
          class="file-panel__editor"
          spellcheck="false"
          autocomplete="off"
          autocapitalize="off"
          :aria-label="`${result.path} 的内容`"
        />
      </template>
    </template>

    <div class="file-panel__actions">
      <n-button size="small" :disabled="saving" @click="emit('close')">关闭</n-button>
      <!--
        文本与二进制都能下载：这是文件管理器，用户需要把文件拿走。
        超限文件不给按钮——后端 read 已拒绝，点了必然失败。
        「仅下载」态手里同样没有内容，父组件会在点击时按需读一次再落盘。
      -->
      <n-button v-if="canDownload" size="small" @click="emit('download')">下载</n-button>
      <template v-if="canEdit">
        <n-button size="small" :disabled="!isDirty || saving" @click="handleRevert">撤销修改</n-button>
        <n-button size="small" type="primary" :loading="saving" :disabled="!canSave" @click="handleSave">
          保存
        </n-button>
      </template>
    </div>
  </div>
</template>

<style scoped>
.file-panel {
  display: flex;
  flex-direction: column;
  gap: 10px;
  min-width: 0;
}

.file-panel__loading {
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: 8px;
  padding: 24px 0;
}

.file-panel__spin {
  /* 加载容器已经给了纵向内边距，spin 自己不再叠加，否则取消按钮离得太远 */
  padding: 0;
}

.file-panel__error {
  display: flex;
  align-items: center;
  gap: 8px;
}

.file-panel__error :deep(.n-alert) {
  flex: 1;
}

.file-panel__notice {
  font-size: 13px;
  line-height: 1.7;
}

.file-panel__meta {
  display: flex;
  align-items: center;
  gap: 12px;
  font-size: 12px;
  color: var(--arkade-text-tertiary);
}

.file-panel__dirty {
  color: var(--arkade-warning);
}

/*
  等宽字体 + 固定行高是刻意选的：
  卷里的文本多是配置/日志/SQL，对齐比行文美观更重要。
  resize: vertical 让用户自己拉高，不要 auto-grow（内容长时会把页面撑飞）。
*/
.file-panel__editor {
  width: 100%;
  min-height: 320px;
  max-height: 60vh;
  resize: vertical;
  padding: 10px 12px;
  border: 1px solid var(--arkade-border-color);
  border-radius: 6px;
  background: var(--arkade-surface-bg-subtle);
  font-family: var(--arkade-mono-font);
  font-size: 13px;
  line-height: 1.6;
  color: var(--arkade-text-primary);
  tab-size: 2;
}

.file-panel__editor:focus {
  outline: 2px solid var(--arkade-link-color, var(--arkade-primary));
  outline-offset: 1px;
}

.file-panel__actions {
  display: flex;
  justify-content: flex-end;
  gap: 8px;
  flex-wrap: wrap;
}
</style>
