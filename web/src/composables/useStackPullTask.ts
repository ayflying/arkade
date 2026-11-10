/**
 * 拉镜像任务状态机（兼容层）
 *
 * 实现已泛化到 `useStackOperationTask`——启动/停止/重建/删除的时序规则
 * 完全一样，没必要维护两份。这里把 pull 的 `StackPullTask` 适配成
 * `StackOperationTask` 形状后转调，**不复制任何状态逻辑**。
 *
 * 新代码应直接用 `useStackOperationTask`。
 */
import type { StackPullTask } from '@/types/stackPullTask'
import type { StackOperationTask } from '@/types/stackOperationTask'
import { useStackOperationTask } from './useStackOperationTask'

export const POLL_INTERVAL_MS = 1000

export interface StackPullRunnerOptions {
  start: (requestId: string) => Promise<StackPullTask>
  fetch: (taskId: string, after: number) => Promise<StackPullTask>
  expectedName?: string
}

/** pull 任务 → 通用任务（补上泛化后新增的字段） */
function toOperationTask(t: StackPullTask): StackOperationTask {
  return {
    ...t,
    action: 'pull',
    backup_path: '',
    record_removed: false,
    stack_version: null
  }
}

export function useStackPullTask(options: StackPullRunnerOptions) {
  return useStackOperationTask({
    start: async (requestId) => toOperationTask(await options.start(requestId)),
    fetch: async (taskId, after) => toOperationTask(await options.fetch(taskId, after)),
    expectedName: options.expectedName
  })
}
