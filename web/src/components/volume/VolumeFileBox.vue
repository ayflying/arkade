<script setup lang="ts">
/**
 * 卷文件面板容器
 *
 * 负责「面板外框 + 路径标题 + 超限/离线提示」，把 VolumeFilePanel 的
 * 纯编辑逻辑与页面布局隔开。
 *
 * ## 超限文件不给下载
 *
 * 后端 read 对超过 4 MiB 的文件**直接拒绝**，也没有单独的流式下载端点。
 * 因此超限时界面只说明「当前限制 4 MiB」，不提供任何下载入口——
 * 给一个必然失败的按钮比不给更糟。
 */
import { computed, ref } from 'vue'
import type { VolumeFileResult } from '@/types/volumeFile'
import VolumeFilePanel from './VolumeFilePanel.vue'

interface Props {
  result: VolumeFileResult | null
  loading: boolean
  saving: boolean
  errorText: string
  /** 超限标记（后端已拒绝，没有内容） */
  tooLarge: boolean
  /** 当前文件相对路径，用于标题 */
  filePath: string
}

defineProps<Props>()

const emit = defineEmits<{
  save: [content: string, expectedSha256: string]
  download: []
  close: []
  retry: []
}>()

/**
 * 透传内部面板的「是否有未保存改动」。
 *
 * 页面需要在**路由守卫**里判断能否离开，而守卫拿不到子组件实例，
 * 所以由这一层把 isDirty 转出来。用 getter 而不是拷贝值：
 * 值会随用户输入变化，拷贝一份就永远是打开时的 false。
 */
const panelRef = ref<InstanceType<typeof VolumeFilePanel> | null>(null)

const isDirty = computed<boolean>(() => panelRef.value?.isDirty ?? false)

/** 保存成功后由页面调用：把当前草稿设为新基线 */
function markSaved(result: VolumeFileResult): void {
  panelRef.value?.markSaved(result)
}

defineExpose({ isDirty, markSaved })
</script>

<template>
  <div class="vol-file-box">
    <div class="vol-file-box__head" :title="filePath">{{ filePath }}</div>
    <volume-file-panel
      ref="panelRef"
      :result="result"
      :loading="loading"
      :saving="saving"
      :error-text="errorText"
      :too-large="tooLarge"
      @save="(content, sha) => emit('save', content, sha)"
      @download="emit('download')"
      @close="emit('close')"
      @retry="emit('retry')"
    />
  </div>
</template>

<style scoped>
.vol-file-box {
  margin: 12px 0;
  padding: 12px 14px;
  border: 1px solid var(--arkade-border-color);
  border-radius: 8px;
  background: var(--arkade-page-bg);
}

.vol-file-box__head {
  margin-bottom: 8px;
  font-family: var(--arkade-mono-font);
  font-size: 12px;
  color: var(--arkade-text-secondary);
  word-break: break-all;
}
</style>
