<script setup lang="ts">
/**
 * 存储卷文件管理器 /agents/:id/volumes/:name/files
 *
 * 独立路由而非抽屉：需要可分享深链接。当前目录走 `?path=a/b`。
 * 数据在 useVolumeFiles，离开保护在 useUnsavedGuard，弹窗在 useVolumeActions。
 */
import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { NAlert, NButton } from 'naive-ui'
import PageHeader from '@/components/common/PageHeader.vue'
import VolumeToolbar from '@/components/volume/VolumeToolbar.vue'
import VolumeEntryTable from '@/components/volume/VolumeEntryTable.vue'
import VolumeFileBox from '@/components/volume/VolumeFileBox.vue'
import VolumeCreateDialog from '@/components/volume/VolumeCreateDialog.vue'
import VolumeRenameDialog from '@/components/volume/VolumeRenameDialog.vue'
import { useCurrentAgent } from '@/composables/useCurrentAgent'
import { useVolumeFiles } from '@/composables/useVolumeFiles'
import { useVolumeActions } from '@/composables/useVolumeActions'
import { useUnsavedGuard } from '@/composables/useUnsavedGuard'
import type { VolumeEntry } from '@/types/volumeFile'
import { VOLUME_DIR_MAX_ENTRIES } from '@/types/volumeFile'
import { base64ToBytes } from '@/utils/base64'
import { toastError, toastSuccess } from '@/utils/feedback'

const route = useRoute()
const router = useRouter()
const { agentId, isOnline, displayName, ensureAgentsLoaded } = useCurrentAgent()

/* ───────── 路由参数 ───────── */
/** 取第一个字符串参数（params/query 可能是数组） */
function firstParam(value: unknown): string {
  const v = Array.isArray(value) ? value[0] : value
  return typeof v === 'string' ? v : ''
}

const volumeName = computed<string>(() => firstParam(route.params.name))

/** 当前目录：规整空段与首尾斜杠，避免 `a//b` 与后端 Clean 结果不一致 */
const currentPath = computed<string>(() =>
  firstParam(route.query.path)
    .split('/')
    .filter((s) => s !== '')
    .join('/')
)

/* ───────── 状态 ───────── */
const store = useVolumeFiles({ agentId, volume: volumeName, currentPath })
const actions = useVolumeActions({ store, currentPath })

const keyword = ref('')
const fileBoxRef = ref<InstanceType<typeof VolumeFileBox> | null>(null)

const atEntryLimit = computed(() => store.entries.value.length >= VOLUME_DIR_MAX_ENTRIES)
const showFileBox = computed(
  () =>
    store.fileResult.value !== null ||
    store.fileLoading.value ||
    store.fileError.value !== '' ||
    store.fileTooLarge.value
)

/* ───────── 离开保护 ───────── */

// 路由守卫 + beforeunload 兜底都在这里面，页面上不必重复实现
const { confirmLeaveDirty } = useUnsavedGuard({
  isDirty: () => fileBoxRef.value?.isDirty ?? false,
  onLeaveConfirmed: () => {
    // 确认离开后才关面板：目录/卷变了，原文件已不在当前视图
    store.closeFile()
    keyword.value = ''
  }
})

/* ───────── 路由变化与卸载 ───────── */

// 换目录/换卷时作废进行中的文件请求，避免迟到的响应写进新上下文
watch(
  () => [agentId.value, volumeName.value, currentPath.value] as const,
  () => {
    store.invalidateFileRequests()
    keyword.value = ''
    void store.loadEntries()
  }
)

onMounted(async () => {
  // 深链进入时 store 可能是空的，补拉一次才能显示服务器名与在线状态
  await ensureAgentsLoaded()
  void store.loadEntries()
})

onUnmounted(() => {
  // 组件没了之后迟到的响应会写进已销毁的状态
  store.invalidateFileRequests()
})

/* ───────── 导航与打开 ───────── */

async function navigateTo(path: string): Promise<void> {
  // 确认由路由守卫统一处理，这里不重复弹
  await router.push({ query: path === '' ? {} : { path } })
}

async function backToVolumes(): Promise<void> {
  await router.push({ name: 'agent-volumes', params: { id: agentId.value ?? '' } })
}

async function handleOpen(entry: VolumeEntry): Promise<void> {
  const path = currentPath.value === '' ? entry.name : `${currentPath.value}/${entry.name}`
  if (entry.kind === 'directory') {
    await navigateTo(path)
    return
  }
  if (entry.kind === 'file') {
    if (!(await confirmLeaveDirty())) return
    await store.readFile(path)
  }
}

async function closeFile(): Promise<void> {
  if (!(await confirmLeaveDirty())) return
  store.closeFile()
}

/* ───────── 保存与下载 ───────── */

async function handleSave(content: string, expectedSha256: string): Promise<void> {
  const savedPath = await store.saveFile(content, expectedSha256)
  if (savedPath === null) {
    toastError(store.fileError.value !== '' ? store.fileError.value : '保存失败')
    return
  }
  // 保存期间用户可能已切到别的文件：只有仍是同一文件才更新基线，
  // 否则会把 A 的保存结果标到 B 上（界面显示 B 已保存而 B 根本没动）
  if (savedPath !== store.openFilePath.value) return
  const result = store.fileResult.value
  if (result !== null) fileBoxRef.value?.markSaved(result)
  toastSuccess('已保存')
}

/**
 * 下载（文本与二进制都支持）。
 *
 * blob URL + `<a download>`：内容已在手里，直接落盘。
 * 4 MiB 上限保证内存开销可控；打开文件时后端会重新 read，不依赖缓存。
 */
function handleDownload(): void {
  const result = store.fileResult.value
  if (result === null) return
  const bytes = base64ToBytes(result.data_base64 ?? '')
  const blob = new Blob([bytes as unknown as BlobPart], { type: 'application/octet-stream' })
  const url = URL.createObjectURL(blob)
  const a = document.createElement('a')
  a.href = url
  const segments = store.openFilePath.value.split('/').filter((s) => s !== '')
  a.download = segments[segments.length - 1] ?? 'download'
  document.body.appendChild(a)
  a.click()
  document.body.removeChild(a)
  // 不 revoke 会一直占着这份数据
  URL.revokeObjectURL(url)
}

/* ───────── 上传 ───────── */
/** 上传中：防止连点重复提交 */
const uploading = ref(false)

async function handleUpload(file: File): Promise<void> {
  if (uploading.value) return
  uploading.value = true
  try {
    await store.uploadCreate(file)
    toastSuccess(`已上传 ${file.name}`)
  } catch (err) {
    toastError(err instanceof Error ? err.message : '上传失败')
  } finally {
    uploading.value = false
  }
}

/* ───────── 标题 ───────── */
const pageTitle = computed(() =>
  volumeName.value === '' ? '存储卷文件' : `存储卷：${volumeName.value}`
)

const subtitle = computed(() => {
  const count = store.entries.value.length
  return count > 0 ? `服务器 ${displayName.value} · ${String(count)} 个条目` : `服务器 ${displayName.value}`
})
</script>

<template>
  <div class="vol-files">
    <page-header :title="pageTitle" :subtitle="subtitle">
      <template #actions>
        <n-button size="small" :disabled="!isOnline || store.listLoading.value" @click="store.loadEntries">
          刷新
        </n-button>
      </template>
    </page-header>

    <!-- 安全提示常驻：进来第一眼就要看到 -->
    <n-alert type="warning" :bordered="false" class="notice">
      这里显示的是容器正在读写的<strong>真实数据</strong>，所有写操作<strong>立即生效、无回滚</strong>。
      数据库文件<strong>不能</strong>当普通文本编辑——保存会损坏它，请先停止容器或使用专用工具。
    </n-alert>

    <n-alert v-if="!isOnline" type="warning" :bordered="false" class="notice">
      服务器当前离线，无法访问卷内文件。请等待 Agent 恢复在线后再试。
    </n-alert>

    <volume-toolbar
      v-model:keyword="keyword"
      :volume="volumeName"
      :path="currentPath"
      :disabled="!isOnline"
      :uploading="uploading"
      @back-to-volumes="backToVolumes"
      @navigate="navigateTo"
      @create="actions.openCreate"
      @upload="handleUpload"
    />

    <div v-if="store.listError.value !== ''" class="error-row">
      <n-alert type="error" :bordered="false">{{ store.listError.value }}</n-alert>
      <n-button size="small" quaternary @click="store.loadEntries">重试</n-button>
    </div>

    <n-alert v-if="atEntryLimit" type="info" :bordered="false" class="notice">
      当前目录条目数已达上限（{{ VOLUME_DIR_MAX_ENTRIES }} 个），只显示前
      {{ VOLUME_DIR_MAX_ENTRIES }} 条。可以用上方过滤框缩小范围。
    </n-alert>

    <volume-file-box
      v-if="showFileBox"
      ref="fileBoxRef"
      :result="store.fileResult.value"
      :loading="store.fileLoading.value"
      :saving="store.fileSaving.value"
      :error-text="store.fileError.value"
      :too-large="store.fileTooLarge.value"
      :file-path="store.openFilePath.value"
      @save="handleSave"
      @download="handleDownload"
      @close="closeFile"
      @retry="store.retryRead"
    />

    <volume-entry-table
      :entries="store.entries.value"
      :loading="store.listLoading.value"
      :keyword="keyword"
      :busy-name="store.busyName.value"
      @open="handleOpen"
      @rename="actions.openRename"
      @remove="actions.confirmRemove"
    />

    <volume-create-dialog
      :show="actions.createShow.value"
      :kind="actions.createKind.value"
      :loading="actions.creating.value"
      :error-text="actions.createError.value"
      @update:show="actions.createShow.value = $event"
      @submit="actions.submitCreate"
    />
    <volume-rename-dialog
      :show="actions.renameShow.value"
      :initial-name="actions.renameTarget.value?.name ?? ''"
      :loading="actions.renaming.value"
      :error-text="actions.renameError.value"
      @update:show="actions.renameShow.value = $event"
      @submit="actions.submitRename"
    />
  </div>
</template>

<style scoped>
.vol-files {
  padding: 4px;
}

.notice {
  margin-bottom: 12px;
  font-size: 13px;
  line-height: 1.7;
}

.error-row {
  display: flex;
  align-items: center;
  gap: 8px;
  margin-bottom: 12px;
}

.error-row :deep(.n-alert) {
  flex: 1;
}
</style>
