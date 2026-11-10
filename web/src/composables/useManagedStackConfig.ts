import { computed, onUnmounted, ref } from 'vue'
import { getStackConfig } from '@/api/stacks'
import { ApiError } from '@/types/api'
import { confirmAction } from '@/utils/confirm'
import { toastError, toastSuccess, toastWarning } from '@/utils/feedback'
import type { UpdateStackRequest } from '@/types/stack'

/** Omission preserves .env; an explicit empty string clears it. */
export function managedUpdatePayload(content: string, version: number, env: string, originalEnv: string): UpdateStackRequest {
  return { content, version, ...(env !== originalEnv ? { env_content: env } : {}) }
}

export function useManagedStackConfig(agentId: string, name: string, changed: () => void) {
  const content = ref(''), originalContent = ref(''), envContent = ref(''), originalEnv = ref('')
  const version = ref(0), loaded = ref(false), loading = ref(false), saving = ref(false)
  const error = ref(''), saveError = ref(''), syncWarning = ref('')
  /** 是否显示「保存并部署」执行弹窗 */
  const deployShow = ref(false)
  /**
   * 部署任务是否仍在执行。
   *
   * 由弹窗的 busy 事件上报。用于判断「再点保存」是开旧任务还是起新任务——
   * 进行中时绝不能起新任务（会重复部署）。
   */
  const deployInFlight = ref(false)
  /**
   * 部署任务是否已达确认终态（拿到 succeeded/failed 的任务结果）。
   *
   * 与 inFlight 不同：POST 失败且没拿到任务时两项都是 false，
   * 但那种情况服务器上可能已有任务，不能换 key 重新部署。
   */
  const deploySettled = ref(false)
  /** 是否发起过部署请求（区分"从没跑过"与"跑过但状态不明"） */
  const deployAttempted = ref(false)
  /**
   * 部署任务的代次。
   *
   * 父级把它放进弹窗的 key：只有确认终态后又发起新一轮时才 +1，
   * 让弹窗重新挂载拿到新 runner。状态未知时绝不能 +1 —— 那等于换
   * request_id、放弃幂等。
   */
  const deployGeneration = ref(0)
  /**
   * 待提交的快照。
   *
   * 点确认后先把内容定格，再打开弹窗执行——弹窗执行期间用户可能继续编辑，
   * 但提交的必须是刚才确认的那一份，否则"确认的"和"执行的"不是同一个东西。
   */
  const pendingDeploy = ref<{
    content: string
    envContent: string | undefined
    version: number
    expectedConfigHash: string | undefined
  } | null>(null)
  let seq = 0, alive = true
  const dirty = computed(() => content.value !== originalContent.value || envContent.value !== originalEnv.value)
  async function load() {
    const mine = ++seq
    loading.value = true
    loaded.value = false
    error.value = ''
    try {
      const res = await getStackConfig(agentId, name)
      if (!alive || mine !== seq) return
      content.value = originalContent.value = res.content
      envContent.value = originalEnv.value = res.env_content
      version.value = res.version
      loaded.value = true
    } catch (err) {
      if (!alive || mine !== seq) return
      /*
       * 404 就是「不存在」这一明确错误，不做"尚未就绪"的揣测：
       * 新建时 Hub 总是先保存配置再返回 202，配置必然已存在。
       * 真拿到 404 说明是权限或确实不存在，照实告诉用户。
       */
      if (err instanceof ApiError && err.status === 403) {
        error.value = '查看或编辑配置需要该服务器的管理权限。容器与日志仍可查看。'
      } else if (err instanceof ApiError && err.status === 404) {
        error.value = '该编排的配置不存在，或你无权访问。'
      } else {
        error.value = err instanceof Error ? err.message : '加载配置失败'
      }
    } finally { if (alive && mine === seq) loading.value = false }
  }
  async function save() {
    if (!loaded.value || saving.value || !dirty.value) return
    saving.value = true
    /*
     * 快照提交的内容：确认对话框之后的编辑不能影响这一次请求。
     * env 只在真的改过时才带上（undefined = 保留 Hub 上原文件）。
     */
    const payload = managedUpdatePayload(content.value, version.value, envContent.value, originalEnv.value)
    try {
      if (
        !(await confirmAction({
          title: '保存并部署配置？',
          content: `将保存「${name}」的配置并立即执行 compose up，可能创建或重建容器。这不是草稿保存。`,
          positiveText: '保存并部署',
          danger: true
        })) ||
        !alive
      ) {
        return
      }
      saveError.value = ''
      /*
       * 起一轮全新任务：**换 generation** 让弹窗重新挂载。
       *
       * 弹窗是常驻实例（关窗不能丢任务），若只改 props 再 show=true，
       * 内部 runner 已经持有终态任务，只会显示上一次的日志——
       * 用户会以为自己刚改的内容已经部署了。换 key 才能拿到新 runner。
       */
      deployGeneration.value += 1
      deploySettled.value = false
      deployAttempted.value = true
      pendingDeploy.value = {
        content: payload.content,
        envContent: payload.env_content,
        version: payload.version,
        // 当前没有独立的内容哈希来源；留空表示不做额外内容校验
        expectedConfigHash: undefined
      }
      deployShow.value = true
    } catch (err) {
      if (!alive) return
      saveError.value = err instanceof ApiError && (err.status === 409 || err.code === 'conflict')
        ? '内容已被他人修改。请先复制当前修改，再重新加载配置。'
        : err instanceof Error ? err.message : '保存并部署失败'
      toastError(saveError.value)
    } finally {
      if (alive) saving.value = false
    }
  }

  /**
   * 部署任务结束后的收尾。
   *
   * - 成功：把本地基准对齐到刚提交的内容，清掉"未保存"标记；
   * - 失败但配置已在 Hub（503 已保存未执行）：**必须刷新 version**，
   *   否则下次保存仍用旧版本号，会撞乐观锁，用户反复失败却看不出原因；
   * - 无论成败都**不清空编辑区**：配置可能已入库，草稿要留着以便重试执行。
   */
  async function handleDeployed(
    succeeded: boolean,
    stackVersion: number | null
  ): Promise<void> {
    const submitted = pendingDeploy.value
    if (submitted === null) return
    /*
     * 拿到了任务结果 = 这一轮已经确认终态。
     * 之后用户再改内容点保存，才允许换 generation 起新任务。
     */
    deploySettled.value = true

    if (succeeded) {
      /*
       * 部署成功：把本地基准对齐到刚提交的内容，清掉"未保存"标记。
       * 版本号以任务的 stack_version 为准（Hub 保存后给出的新版本）。
       */
      originalContent.value = submitted.content
      if (submitted.envContent !== undefined) originalEnv.value = submitted.envContent
      if (stackVersion !== null) version.value = stackVersion
      syncWarning.value = ''
      toastSuccess('配置已保存并部署')
      changed()
      return
    }

    /*
     * 失败路径：配置**可能已经保存进 Hub**（Hub 先持久化再下发执行）。
     *
     * 两个后果必须都处理：
     * 1. 乐观锁版本号已经前进，继续用旧版本号保存会一直 409；
     * 2. 用户草稿**绝对不能清** —— 他刚写的内容还要拿来重试。
     *
     * 所以这里只刷新版本号与基准内容（reload 拿最新 version），
     * 编辑区保持用户原样。若拿不到新版本，就退回用任务里的 stack_version。
     */
    /*
     * 优先用任务回报的 stack_version：Hub 保存成功后会带上新版本号，
     * 直接拿来更新即可，不需要额外请求。
     */
    if (stackVersion !== null) {
      version.value = stackVersion
    } else {
      /*
       * 任务已到终态但没回报版本号（例如执行失败且 Hub 未更新版本）：
       * 重新读一次对齐版本号。
       *
       * 注意：HTTP 503（配置已保存但没执行）**不会走到这里**——
       * 那种情况根本没有任务，弹窗不会发 finished 事件，
       * 父级保持"状态未知"锁定，下次仍用同一个 request_id 重试。
       * 在这里刷新版本号反而会误解除锁定，导致重复部署。
       */
      const reloaded = await reloadVersionOnly()
      if (!reloaded) {
        syncWarning.value = '无法确认最新配置版本，建议重新加载配置后再修改，避免保存冲突。'
      }
    }
    toastWarning('配置可能已保存但尚未执行。版本号已同步，可重试执行（编辑内容已保留）。')
    changed()
  }

  /**
   * 只刷新版本号与基准内容，**不动用户正在编辑的草稿**。
   *
   * 返回是否成功拿到新版本。
   */
  async function reloadVersionOnly(): Promise<boolean> {
    try {
      const res = await getStackConfig(agentId, name)
      if (!alive) return false
      /*
       * **只更新版本号**。
       *
       * 不把 originalContent 设成服务端内容：那样 dirty 会变成 false，
       * 编辑区看起来"什么都没改过"，用户以为自己的稿子被保存了。
       * 实际语义是"配置可能已入库，但这次部署没执行完"，
       * 草稿必须保持原样、仍然算未保存，用户才能拿它重试。
       */
      version.value = res.version
      return true
    } catch {
      return false
    }
  }

  /** 弹窗上报任务占用状态；父级判断能否起新任务靠它 */
  function setDeployBusy(busy: boolean): void {
    deployInFlight.value = busy
  }

  /**
   * 清掉待提交快照。
   *
   * **不要**在弹窗关闭时调用：清空会让承载任务的弹窗失去 props 来源，
   * 结合父级条件渲染会把任务实例与 request_id 一起丢掉。
   * 只在"确认要开始一个全新任务"前用它腾位。
   */
  function clearPendingDeploy(): void {
    pendingDeploy.value = null
  }
  function reset() { content.value = originalContent.value; envContent.value = originalEnv.value; saveError.value = '' }
  onUnmounted(() => {
    alive = false; seq++
    content.value = originalContent.value = envContent.value = originalEnv.value = ''
  })
  return {
    content,
    envContent,
    version,
    loaded,
    loading,
    saving,
    error,
    saveError,
    syncWarning,
    dirty,
    deployShow,
    pendingDeploy,
    load,
    save,
    reset,
    handleDeployed,
    clearPendingDeploy,
    deployInFlight,
    setDeployBusy,
    deploySettled,
    deployAttempted,
    deployGeneration
  }
}
