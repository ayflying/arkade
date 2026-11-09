/**
 * 卷文件页的对话框与动作编排
 *
 * 从页面抽出来的部分：新建、重命名、删除三个动作的状态与提交流程。
 *
 * ## 为什么单独一层
 *
 * 这三块的共同点是「开关弹窗 → 校验 → 提交 → 错误回填弹窗」，
 * 和页面的路由守卫、安全提示没有关系。放在页面里会把视图撑得过长，
 * 改一个弹窗文案要在一堆路由逻辑里找。
 *
 * ## 删除不走弹窗
 *
 * 删除用 `confirmAction`（全局确认框）而不是自成弹窗：
 * 它是不可恢复的高危操作，应该和项目里其他删除保持完全一致的二次确认形态，
 * 让用户遇到的确认框长得一样、说得一样重。
 */

import { ref } from 'vue'
import type { Ref } from 'vue'
import type { VolumeEntry } from '@/types/volumeFile'
import { baseName, joinPath, useVolumeFiles } from './useVolumeFiles'
import { confirmAction } from '@/utils/confirm'
import { toastError, toastSuccess } from '@/utils/feedback'

export interface UseVolumeActionsOptions {
  /** 数据层 */
  store: ReturnType<typeof useVolumeFiles>
  /** 当前目录，用于拼相对路径 */
  currentPath: Ref<string>
}

export function useVolumeActions(options: UseVolumeActionsOptions) {
  const { store, currentPath } = options

  /* ── 新建 ── */

  const createShow = ref(false)
  const createKind = ref<'file' | 'dir'>('file')
  const creating = ref(false)
  const createError = ref('')

  function openCreate(kind: 'file' | 'dir'): void {
    createKind.value = kind
    createError.value = ''
    createShow.value = true
  }

  async function submitCreate(name: string): Promise<void> {
    creating.value = true
    createError.value = ''
    try {
      if (createKind.value === 'file') {
        await store.createFile(name)
        toastSuccess('文件已创建')
      } else {
        await store.createDir(name)
        toastSuccess('文件夹已创建')
      }
      createShow.value = false
    } catch (err) {
      // 错误回填到弹窗内，不用 toast：用户要能看着输入框改名字
      createError.value = err instanceof Error ? err.message : '创建失败'
    } finally {
      creating.value = false
    }
  }

  /* ── 重命名 ── */

  const renameShow = ref(false)
  const renameTarget = ref<VolumeEntry | null>(null)
  const renaming = ref(false)
  const renameError = ref('')

  function openRename(entry: VolumeEntry): void {
    renameTarget.value = entry
    renameError.value = ''
    renameShow.value = true
  }

  async function submitRename(name: string): Promise<void> {
    const target = renameTarget.value
    if (target === null) return
    if (name === target.name) {
      renameShow.value = false
      return
    }
    renaming.value = true
    renameError.value = ''
    try {
      await store.rename(joinPath(currentPath.value, target.name), joinPath(currentPath.value, name))
      toastSuccess('已重命名')
      renameShow.value = false
    } catch (err) {
      // 后端拒绝覆盖已存在目标，错误回填弹窗
      renameError.value = err instanceof Error ? err.message : '重命名失败'
    } finally {
      renaming.value = false
    }
  }

  /* ── 删除 ── */

  /**
   * 删除确认后执行。
   *
   * 文案刻意写重：不可恢复、仅空目录、可能影响运行中的服务。
   * 这些不是免责声明，是用户真的需要知道的信息。
   */
  async function confirmRemove(entry: VolumeEntry): Promise<void> {
    const isDir = entry.kind === 'directory'
    const confirmed = await confirmAction({
      title: isDir ? '确认删除这个文件夹？' : '确认删除这个文件？',
      content:
        `将删除「${entry.name}」${isDir ? '（仅当它是空文件夹时才能删除）' : ''}。\n\n` +
        '删除后**无法恢复**。如果这是容器正在使用的数据，删除会导致相关服务异常或数据丢失。\n\n' +
        '确定要删除吗？',
      positiveText: '永久删除',
      negativeText: '取消',
      danger: true
    })
    if (!confirmed) return
    try {
      await store.remove(joinPath(currentPath.value, entry.name))
      toastSuccess('已删除')
    } catch (err) {
      toastError(err instanceof Error ? err.message : '删除失败')
    }
  }

  return {
    createShow,
    createKind,
    creating,
    createError,
    openCreate,
    submitCreate,
    renameShow,
    renameTarget,
    renaming,
    renameError,
    openRename,
    submitRename,
    confirmRemove
  }
}

export { baseName }
