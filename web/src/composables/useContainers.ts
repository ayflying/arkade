/**
 * 容器列表数据逻辑（页面局部状态）
 *
 * 为什么不放进全局 store：容器状态变化频繁、只在容器页使用，
 * 放进全局会带来「什么时候失效」的额外复杂度，而页面卸载后就没用了。
 * 因此用一个组合式函数封装「加载 / 局部更新 / 启停 / 重启 / 删除」，视图只负责渲染。
 */

import { ref } from 'vue'
import {
  deleteContainer,
  listContainers,
  restartContainer,
  startContainer,
  stopContainer
} from '@/api/containers'
import { ApiError } from '@/types/api'
import type { Container } from '@/types/container'

/** 单行操作的进行中标记：key 为容器 id，用于按钮 loading 与防连点 */
export type BusyMap = Record<string, boolean>

/** 可执行的容器操作 */
export type ContainerOperation = 'start' | 'stop' | 'restart'

export function useContainers(agentId: () => string) {
  const containers = ref<Container[]>([])
  const loading = ref(false)
  /** 加载失败文案 */
  const errorText = ref('')
  /** 是否已成功加载过，用于区分「没加载完」与「确实没有容器」 */
  const loaded = ref(false)
  /** 容器 id → 是否正在执行操作 */
  const busy = ref<BusyMap>({})

  /**
   * 全量加载容器列表。
   *
   * @param silent 静默刷新：不显示 loading 骨架。
   *               自动轮询走这个模式——每 5 秒闪一次骨架会让页面看起来一直在抽搐，
   *               而且会把用户的滚动位置和交互打断。
   *
   * 边界：失败时保留旧数据，只设 errorText，避免刷新失败把页面刷成空白。
   * 静默模式下失败**不改 errorText**：自动刷新偶发失败不该在页面上弹错误，
   * 用户可能正在看别的；连续失败由 usePolling 的退避与全局提示承担。
   */
  async function load(silent = false): Promise<boolean> {
    if (!silent) loading.value = true
    if (!silent) errorText.value = ''
    try {
      containers.value = await listContainers(agentId())
      loaded.value = true
      if (silent) errorText.value = ''
      return true
    } catch (err) {
      if (!silent) {
        errorText.value = err instanceof ApiError ? err.message : '加载容器列表失败'
      }
      // 静默失败向上抛，让 usePolling 计入连续失败并触发退避
      if (silent) throw err
      return false
    } finally {
      if (!silent) loading.value = false
    }
  }

  /**
   * 用接口返回值局部更新单行。
   *
   * 关键点：只更新这一行的 state/status，不整表重刷。
   * 原因：整表刷新会让用户失去滚动位置、且其它行的实时变化可能造成闪烁；
   * 而启停接口本身已经返回了最新 state，足够反映真实结果。
   * status 文案由前端根据 state 生成中文描述（接口只保证 state 准确）。
   */
  function patchContainerState(containerId: string, state: string): void {
    const target = containers.value.find((c) => c.id === containerId)
    if (!target) return
    target.state = state
    target.status = describeState(state)
  }

  /**
   * 启停/重启统一入口：设置 busy → 调接口 → 局部更新 → 清 busy。
   *
   * 返回 { ok, message }，由页面决定如何提示，避免把 UI 反馈耦合进数据层。
   */
  async function runAction(
    container: Container,
    action: ContainerOperation
  ): Promise<{ ok: boolean; message: string }> {
    // 防连点：同一容器已有操作在跑时直接拒绝
    if (busy.value[container.id] === true) {
      return { ok: false, message: '该容器正在执行操作，请稍候' }
    }
    busy.value = { ...busy.value, [container.id]: true }
    try {
      const res =
        action === 'start'
          ? await startContainer(agentId(), container.id)
          : action === 'stop'
            ? await stopContainer(agentId(), container.id)
            : await restartContainer(agentId(), container.id)
      patchContainerState(container.id, res.state)
      return {
        ok: true,
        message: `${container.name} 已${ACTION_LABELS[action]}`
      }
    } catch (err) {
      // 失败时给出中文原因（离线/超时/运行时失败等都由 request 层翻译好）
      return { ok: false, message: err instanceof ApiError ? err.message : '操作失败，请稍后重试' }
    } finally {
      const next = { ...busy.value }
      delete next[container.id]
      busy.value = next
    }
  }

  /**
   * 删除容器。
   *
   * 与启停的区别：删除成功后该行**不复存在**，无法局部更新，
   * 只能把这条从列表里移除（或整表重刷）。这里选择从本地数组移除，
   * 理由与启停保持一致：不整表重刷，用户不会丢失滚动位置。
   *
   * @param force         运行中的容器必须为 true（后端会先强制停止再删）
   * @param removeVolumes 是否连带删除匿名数据卷，默认 false
   */
  async function removeContainer(
    container: Container,
    options: { force: boolean; removeVolumes: boolean }
  ): Promise<{ ok: boolean; message: string }> {
    if (busy.value[container.id] === true) {
      return { ok: false, message: '该容器正在执行操作，请稍候' }
    }
    busy.value = { ...busy.value, [container.id]: true }
    try {
      await deleteContainer(agentId(), container.id, {
        force: options.force,
        removeVolumes: options.removeVolumes
      })
      containers.value = containers.value.filter((c) => c.id !== container.id)
      return {
        ok: true,
        message:
          options.removeVolumes
            ? `${container.name} 已删除（含匿名数据卷）`
            : `${container.name} 已删除`
      }
    } catch (err) {
      return { ok: false, message: err instanceof ApiError ? err.message : '删除容器失败' }
    } finally {
      const next = { ...busy.value }
      delete next[container.id]
      busy.value = next
    }
  }

  return { containers, loading, errorText, loaded, busy, load, runAction, removeContainer }
}

/** 操作 → 完成提示里的动词 */
const ACTION_LABELS: Record<ContainerOperation, string> = {
  start: '启动',
  stop: '停止',
  restart: '重启'
}

/**
 * 根据容器 state 生成中文状态描述（用于「Up 3 hours」这类字段的替代）。
 *
 * 为什么不直接用后端 status：启停成功后我们只知道新 state，
 * 后端 status 文本（"Up 3 hours"）还没重新拉取；这里给一个确定性文案，
 * 等用户下次刷新时再回到后端的真实描述。
 */
function describeState(state: string): string {
  switch (state) {
    case 'running':
      return '运行中'
    case 'exited':
      return '已停止'
    case 'paused':
      return '已暂停'
    case 'restarting':
      return '重启中'
    case 'created':
      return '已创建'
    default:
      return state
  }
}
