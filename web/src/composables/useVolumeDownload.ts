import { onScopeDispose, ref, watch } from 'vue'
import { readVolumeFile } from '@/api/volumeFiles'
import type { VolumeEntry } from '@/types/volumeFile'
import { base64ToBytes } from '@/utils/base64'
import { toastError } from '@/utils/feedback'

export function downloadFile(name: string, content: string): void {
  const bytes = base64ToBytes(content)
  const blob = new Blob([bytes as unknown as BlobPart], { type: 'application/octet-stream' })
  const url = URL.createObjectURL(blob)
  const a = document.createElement('a')
  a.href = url
  a.download = name
  document.body.appendChild(a)
  a.click()
  document.body.removeChild(a)
  // 不 revoke 会一直占着这份数据
  URL.revokeObjectURL(url)
}

export function useVolumeDownload(
  agentId: () => string | null | undefined,
  volumeName: () => string,
  currentPath: () => string,
  isOnline: () => boolean
) {
  const downloadingName = ref('')
  let revision = 0
  function invalidate(): void {
    revision++
    downloadingName.value = ''
  }
  watch([agentId, volumeName, currentPath, isOnline], invalidate, { flush: 'sync' })
  onScopeDispose(invalidate)

  // 直接下载不切换正在编辑的文件，也不改变它的保存基线。
  async function downloadEntry(entry: VolumeEntry): Promise<void> {
    const agent = agentId()
    if (!agent || !isOnline() || downloadingName.value !== '' || entry.kind !== 'file') return
    const volume = volumeName()
    const directory = currentPath()
    const path = directory === '' ? entry.name : `${directory}/${entry.name}`
    const request = ++revision
    const isCurrent = () => request === revision
    downloadingName.value = entry.name
    try {
      const result = await readVolumeFile(agent, volume, path)
      if (isCurrent()) downloadFile(entry.name, result.data_base64 ?? '')
    } catch (err) {
      if (isCurrent()) toastError(err instanceof Error ? err.message : '下载失败')
    } finally {
      if (isCurrent()) downloadingName.value = ''
    }
  }

  return { downloadingName, downloadEntry }
}
