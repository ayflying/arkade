/**
 * 服务器资源的通用加载/删除逻辑
 *
 * 为什么抽象：镜像、网络、存储卷三个页面除了「列定义」和「删除文案」不同，
 * 加载、错误处理、删除后刷新、忙碌态管理**完全一致**。
 * 若各写一遍，任何一处逻辑修复都要改三遍。
 *
 * 设计：传入「加载函数」和「删除函数」，返回统一的状态与动作。
 * 删除成功后**重新拉列表**而不是本地 splice——因为删除可能连带影响其他项
 * （例如删镜像后某些容器的引用数变化），以服务端为准更可靠。
 */

import { ref, type Ref } from 'vue'
import { ApiError } from '@/types/api'
import { toastError, toastSuccess } from '@/utils/feedback'

export interface UseResourceListOptions<T> {
  /** 拉取列表；返回的数组直接赋给 items */
  load: () => Promise<T[]>
  /** 删除单项 */
  remove: (item: T) => Promise<void>
  /** 加载失败时的兜底文案 */
  loadErrorMessage: string
  /** 删除成功提示 */
  deleteSuccessMessage: string
}

export interface UseResourceListReturn<T> {
  /** 当前数据 */
  items: Ref<T[]>
  /** 加载中 */
  loading: Ref<boolean>
  /** 加载错误文案；为空表示无错误 */
  errorText: Ref<string>
  /** 是否至少成功加载过一次：区分「首次加载为空」与「确实没有数据」 */
  loaded: Ref<boolean>
  /** 正在删除的项标识；用于禁用它自己的按钮，避免连点 */
  busyKey: Ref<string>
  /** 拉取列表 */
  refresh: () => Promise<void>
  /** 执行删除；内部已含失败提示与刷新 */
  runDelete: (item: T, busyKeyValue: string) => Promise<boolean>
}

/**
 * @param options 见 UseResourceListOptions
 */
export function useResourceList<T>(options: UseResourceListOptions<T>): UseResourceListReturn<T> {
  const items = ref([]) as Ref<T[]>
  const loading = ref(false)
  const errorText = ref('')
  const loaded = ref(false)
  const busyKey = ref('')

  /**
   * 拉取列表。
   *
   * 失败时保留旧数据只置错误文案：手动刷新失败不该把已有表格清空成白屏。
   */
  async function refresh(): Promise<void> {
    loading.value = true
    errorText.value = ''
    try {
      items.value = await options.load()
      loaded.value = true
    } catch (err) {
      errorText.value = err instanceof ApiError ? err.message : options.loadErrorMessage
    } finally {
      loading.value = false
    }
  }

  /**
   * 删除单项。
   *
   * @param busyKeyValue 用于标记「哪一项正在删」，让只有该行按钮转圈
   * @returns 是否删除成功（调用方通常不需要区分，失败已在内部提示）
   */
  async function runDelete(item: T, busyKeyValue: string): Promise<boolean> {
    // 已有删除在途时直接忽略：防止用户连点造成重复请求
    if (busyKey.value !== '') return false
    busyKey.value = busyKeyValue
    try {
      await options.remove(item)
      toastSuccess(options.deleteSuccessMessage)
      // 以服务端为准重新拉取，而不是本地移除
      await refresh()
      return true
    } catch (err) {
      toastError(err instanceof ApiError ? err.message : '删除失败，请稍后重试')
      return false
    } finally {
      busyKey.value = ''
    }
  }

  return { items, loading, errorText, loaded, busyKey, refresh, runDelete }
}
