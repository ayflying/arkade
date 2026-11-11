/**
 * 迁移编排任务的状态封装
 *
 * 迁移不是「编辑本编排」，但它**需要的东西和操作任务一模一样**：
 * 一次 POST 拿到任务、按游标增量读取、断线不算失败、关窗不取消。
 * 所以这里不写第二套状态机，只把 `useStackOperationTask` 的两个闭包
 * 接到迁移端点上。
 *
 * 唯一额外的职责是**把 404 翻译成「后端尚未部署」**：
 * 迁移是新增能力，后端可能还没上线。如果照原样显示成「迁移失败」，
 * 用户会去怀疑自己的编排或目标服务器，排查一个根本不存在的问题。
 */
import { ref } from 'vue'
import type { StackMigrateRequest } from '@/types/stackOperationTask'
import { getStackOperationTask, isMigrateNotDeployed, migrateStack } from '@/api/stackOperationTasks'
import { useStackOperationTask } from './useStackOperationTask'

export interface UseStackMigrateTaskOptions {
  /** 源服务器 id */
  agentId: string
  /** 源编排名（也是任务回显的 name） */
  stackName: string
}

/**
 * 目标服务器与两个勾选在「发起那一刻」取值。
 *
 * 用 getter 而不是值：组件把用户的选择放在 ref 里，
 * 传值进来就固定成初始值了，用户改了勾选也发不出去。
 */
export interface MigrateTaskParams {
  targetAgentId: () => string
  includeVolumes: () => boolean
  startAfter: () => boolean
}

export function useStackMigrateTask(
  options: UseStackMigrateTaskOptions,
  params: MigrateTaskParams
) {
  /**
   * 后端尚未部署迁移接口。
   *
   * 独立成一个标志而不是塞进 errorText：界面要显示的是
   * 「这个功能还没上线」这种完全不同的提示（不是红色错误），
   * 并且它成立时不该让用户反复点重试。
   */
  const notDeployed = ref(false)
  // The idempotency key and payload are one immutable intent. A network retry
  // must never read changed form getters with the same request_id.
  let submitted: Readonly<StackMigrateRequest> | null = null
  let submittedRequestId = ''

  const runner = useStackOperationTask({
    /*
     * 在 start 的**闭包里**拦 404。
     *
     * 不能等 runner.start() 返回后再判断——那时错误已经被状态机
     * 消化成 errorText 了，外层拿不到原始错误。
     */
    start: async (requestId) => {
      try {
        if (requestId !== submittedRequestId) {
          submittedRequestId = requestId
          submitted = null
        }
        submitted ??= Object.freeze({
          target_agent_id: params.targetAgentId(),
          include_volumes: params.includeVolumes(),
          start_after: params.startAfter(),
          confirmation: true
        })
        const task = await migrateStack(options.agentId, options.stackName, requestId, submitted)
        notDeployed.value = false
        return task
      } catch (err) {
        if (isMigrateNotDeployed(err)) notDeployed.value = true
        throw err
      }
    },
    /*
     * 进度接口与普通操作任务共用：任务体结构完全一致，
     * 后端也用它读迁移任务的增量事件。
     */
    fetch: (taskId, after) =>
      getStackOperationTask(options.agentId, options.stackName, taskId, after),
    /*
     * 任务回显的 name 是**源编排**名（后端约定不变），
     * 动作是新值 migrate。两者都用来校验响应身份，
     * 避免把别的编排或别的动作的日志显示成迁移进度。
     */
    expectedName: options.stackName,
    expectedAction: 'migrate',
    // 迁移不保存配置，503 就是「不知道有没有跑起来」，不能声称已保存
    savesConfig: false,
    describeStartError: (error) => {
      const status = (error as { status?: unknown } | null)?.status
      if (status === 403) return '迁移要求对源服务器和目标服务器都具有 manage 管理权限。'
      if (status === 404 || status === 409) {
        return error instanceof Error ? error.message : '源或目标不可用、存在同名项目或迁移冲突，请核对后重试。'
      }
      return undefined
    }
  })

  return { ...runner, notDeployed }
}
