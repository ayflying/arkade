/**
 * 编排「更新镜像」任务的接口
 *
 * ## 为什么单独一个文件
 *
 * `api/stacks.ts` 已经很长，而且这个接口的**错误语义**和普通动作不同：
 * 启动 POST 失败时，前端必须区分「请求根本没发出去」和
 * 「发出去但没收到响应」。后者是幂等键最危险的场景——
 * 任务可能已经起了，换一个新的 request_id 重试就会起第二个。
 * 把这种判断放这里，调用方不用重复写。
 */
import { httpGet, httpPost } from './request'
import { ApiError } from '@/types/api'
import type { StackPullTask, StackPullTaskResponse } from '@/types/stackPullTask'
import { normalizePullTask } from '@/types/stackPullTask'

/** 编排名可能含特殊字符，必须编码，否则拼出坏 URL */
function base(agentId: string, name: string): string {
  return `/agents/${encodeURIComponent(agentId)}/stacks/${encodeURIComponent(name)}/pull-tasks`
}

/**
 * 启动任务。
 *
 * `requestId` 是幂等键，**必须由调用方一次性生成并复用**。
 * 这里不提供「自动生成 id 并重试」的封装——那会把幂等语义藏起来，
 * 一旦有人调两次就变成两个任务。
 */
export async function startStackPull(
  agentId: string,
  name: string,
  requestId: string
): Promise<StackPullTask> {
  const res = await httpPost<StackPullTaskResponse>(base(agentId, name), { request_id: requestId })
  const task = normalizePullTask((res as { task?: unknown } | null)?.task)
  if (task === null) throw new ApiError(500, 'invalid_response', '服务器返回了无法识别的任务信息')
  return task
}

/**
 * 读取任务状态（可选增量）。
 *
 * `after` 传上一次拿到的 `next_cursor`，服务端只返回之后的事件。
 * 这是**只读**接口：不会改变任务状态，所以断线后可以放心重试。
 */
export async function getStackPullTask(
  agentId: string,
  name: string,
  taskId: string,
  after: number
): Promise<StackPullTask> {
  const path = `${base(agentId, name)}/${encodeURIComponent(taskId)}?after=${String(Math.max(0, Math.floor(after)))}`
  const res = await httpGet<StackPullTaskResponse>(path)
  const task = normalizePullTask((res as { task?: unknown } | null)?.task)
  if (task === null) throw new ApiError(500, 'invalid_response', '服务器返回了无法识别的任务信息')
  return task
}

/**
 * 判断错误是不是「网络层失败」，而不是服务端的明确拒绝。
 *
 * 两者的区别决定了能不能重试：
 * - 403/404/409 是服务端**明确回答**了，重试没意义（还可能是权限问题）；
 * - 没有响应体/超时是**状态未知**，任务可能已经启动。
 *
 * `ApiError` 带 status 说明拿到了 HTTP 响应；不是 ApiError 的
 * （如 fetch 的 TypeError）通常意味着连响应都没有。
 */
export function isNetworkUncertain(err: unknown): boolean {
  if (err instanceof ApiError) {
    // 有明确 HTTP 状态码 → 服务端答过了
    return false
  }
  // 其它异常（fetch failed / timeout）→ 无法确定
  return true
}
