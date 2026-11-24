import { onScopeDispose, ref, watch } from 'vue'
import { readVolumeFile } from '@/api/volumeFiles'
import { isAborted } from '@/api/request'
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
  /**
   * 进行中的下载读取。
   *
   * 下载走的是和打开文件同一条读取接口（同样占 Agent 侧并发槽位）。
   * 切目录、切卷、断线或离开页面时必须真正中断请求，而不只是作废响应——
   * 否则服务端会继续跑到自己的超时，槽位一直被一次已放弃的下载占着。
   */
  let inFlight: AbortController | null = null
  function invalidate(): void {
    revision++
    inFlight?.abort()
    inFlight = null
    downloadingName.value = ''
  }
  watch([agentId, volumeName, currentPath, isOnline], invalidate, { flush: 'sync' })
  onScopeDispose(invalidate)

  /**
   * 下载某个相对路径（相对当前卷根）。
   *
   * 打开文件与「仅下载」态的面板都走这里：内容本地没有，只能现在读一次。
   * 直接下载不切换正在编辑的文件，也不改变它的保存基线。
   */
  async function downloadPath(path: string): Promise<void> {
    const agent = agentId()
    if (!agent || !isOnline() || downloadingName.value !== '' || path === '') return
    const volume = volumeName()
    const name = path.split('/').at(-1) ?? 'download'
    const request = ++revision
    const isCurrent = () => request === revision
    inFlight?.abort()
    const controller = new AbortController()
    inFlight = controller
    downloadingName.value = name
    try {
      const result = await readVolumeFile(agent, volume, path, controller.signal)
      if (isCurrent()) downloadFile(name, result.data_base64 ?? '')
    } catch (err) {
      // 主动放弃不是失败：不给用户弹「下载失败」，他们自己刚离开
      if (isCurrent() && !isAborted(err)) {
        toastError(err instanceof Error ? err.message : '下载失败')
      }
    } finally {
      if (inFlight === controller) inFlight = null
      if (isCurrent()) downloadingName.value = ''
    }
  }

  /** 下载目录里的某个条目（表格行内按钮） */
  async function downloadEntry(entry: VolumeEntry): Promise<void> {
    if (entry.kind !== 'file' || downloadingName.value !== '') return
    const directory = currentPath()
    await downloadPath(directory === '' ? entry.name : `${directory}/${entry.name}`)
  }

  return { downloadingName, downloadEntry, downloadPath }
}
