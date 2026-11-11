/**
 * 编排操作任务接口
 *
 * 所有编排编辑动作（启动/停止/重启/更新/重建/备份/删除/保存部署）共用。
 *
 * 旧文件 `api/stackPullTasks.ts` 保留并转调本文件——**不复制实现**，
 * 否则两处校验逻辑迟早分叉（一处修了另一处没修）。
 */
import { httpGet, httpPost } from './request'
import { ApiError } from '@/types/api'
import type {
  StackMigrateRequest,
  StackOperationAction,
  StackOperationTask,
  StackOperationTaskResponse
} from '@/types/stackOperationTask'
import { normalizeOperationTask } from '@/types/stackOperationTask'

/** 编排名可能含特殊字符，必须编码 */
function base(agentId: string, name: string): string {
  return `/agents/${encodeURIComponent(agentId)}/stacks/${encodeURIComponent(name)}/operation-tasks`
}

/** 启动参数（除 id 与 action 外的可选字段） */
export interface StartOperationOptions {
  removeVolumes?: boolean
  includeVolumes?: boolean
  /** 保存并部署 / 新建时的 compose 内容 */
  content?: string
  /** 保存并部署 / 新建时的 .env；undefined = 保留，'' = 清空 */
  envContent?: string
  /** 编辑已有编排时必填的乐观锁版本号 */
  version?: number
  /** 可选的额外内容校验 */
  expectedConfigHash?: string
  /** 新建编排时置 true（此时不需要 version） */
  create?: boolean
}

/**
 * 启动一个操作任务。
 *
 * `requestId` 是幂等键，**必须由调用方一次性生成并复用**。
 * 这里不提供「自动生成 id 并重试」的封装——那会把幂等语义藏起来，
 * 一旦有人调两次就变成两个任务。
 */
export async function startStackOperation(
  agentId: string,
  name: string,
  action: StackOperationAction,
  requestId: string,
  options: StartOperationOptions = {}
): Promise<StackOperationTask> {
  const body: Record<string, unknown> = { request_id: requestId, action }
  if (options.removeVolumes !== undefined) body.remove_volumes = options.removeVolumes
  if (options.includeVolumes !== undefined) body.include_volumes = options.includeVolumes
  if (options.content !== undefined) body.content = options.content
  /*
   * env_content 的「未提供」必须保持字段缺失，不能写成空串——
   * Hub 语义里 undefined 是"保留原文件"，'' 是"清空"。
   */
  if (options.envContent !== undefined) body.env_content = options.envContent
  // 乐观锁版本号：编辑已有编排必填，不能只用 hash 代替
  if (options.version !== undefined) body.version = options.version
  if (options.expectedConfigHash !== undefined) {
    body.expected_config_hash = options.expectedConfigHash
  }
  if (options.create === true) body.create = true
  const res = await httpPost<StackOperationTaskResponse>(base(agentId, name), body)
  const task = normalizeOperationTask((res as { task?: unknown } | null)?.task)
  if (task === null) throw new ApiError(500, 'invalid_response', '服务器返回了无法识别的任务信息')
  return task
}

/**
 * 读取任务状态（可选增量）。
 *
 * `after` 传上次拿到的 `next_cursor`，服务端只返回之后的事件。
 * **只读**接口：不改变任务状态，断线后可放心重试。
 */
export async function getStackOperationTask(
  agentId: string,
  name: string,
  taskId: string,
  after: number
): Promise<StackOperationTask> {
  const path = `${base(agentId, name)}/${encodeURIComponent(taskId)}?after=${String(Math.max(0, Math.floor(after)))}`
  const res = await httpGet<StackOperationTaskResponse>(path)
  const task = normalizeOperationTask((res as { task?: unknown } | null)?.task)
  if (task === null) throw new ApiError(500, 'invalid_response', '服务器返回了无法识别的任务信息')
  return task
}

/**
 * 迁移编排到另一台服务器。
 *
 * 端点与普通操作任务**不同**（`/migrate` 而不是 `/operation-tasks`）：
 * 迁移的语义是「源编排 → 另一台服务器」，入参里有目标服务器这类
 * 操作任务没有的字段。但返回的任务体与读取方式完全一致，
 * 所以进度直接复用 `getStackOperationTask`。
 *
 * `requestId` 同样是幂等键，由调用方生成并复用。
 */
export async function migrateStack(
  agentId: string,
  name: string,
  requestId: string,
  request: StackMigrateRequest
): Promise<StackOperationTask> {
  const path = `/agents/${encodeURIComponent(agentId)}/stacks/${encodeURIComponent(name)}/migrate`
  const body: Record<string, unknown> = {
    request_id: requestId,
    target_agent_id: request.target_agent_id,
    /*
     * 两个布尔值都**显式发送**，不靠后端默认值：
     * 界面上的勾选框就是用户的明确选择，省略等于让后端猜。
     */
    include_volumes: request.include_volumes,
    start_after: request.start_after,
    // 硬性确认位：后端要求 true 才执行
    confirmation: request.confirmation
  }
  const res = await httpPost<StackOperationTaskResponse>(path, body)
  const task = normalizeOperationTask((res as { task?: unknown } | null)?.task)
  if (task === null) throw new ApiError(500, 'invalid_response', '服务器返回了无法识别的任务信息')
  return task
}

/**
 * 判断错误是不是「该接口在这个后端上还不存在」。
 *
 * 迁移是新增能力，后端可能还没部署。404 在用户眼里必须呈现为
 * 「功能尚未就绪」，而不是「迁移失败」——后者会让用户以为是自己
 * 的编排或目标服务器有问题，从而去排查根本不存在的原因。
 *
 * 403 不算：那是权限问题，得如实告诉用户。
 */
export function isMigrateNotDeployed(err: unknown): boolean {
  if (err === null || typeof err !== 'object') return false
  const status = (err as { status?: unknown }).status
  return status === 404
}

/**
 * 判断错误是不是「配置已经保存了，但 Agent 离线没执行」。
 *
 * Hub 在保存成功、下发失败时返回 503 并给出这句提示。
 * 这种情况**不能**按普通失败处理：配置已经落到 Hub 了，
 * 界面要是把表单清掉或让用户以为没保存，他刚写的 YAML 就白写了。
 * 正确处理是保留草稿、提示可以稍后重试执行。
 */
export function isSavedButNotDeployed(err: unknown): boolean {
  if (err === null || typeof err !== 'object') return false
  const status = (err as { status?: unknown }).status
  if (status !== 503) return false
  const message = (err as { message?: unknown }).message
  if (typeof message !== 'string') return true
  // 有明确"已保存"措辞时更确定；没有也别否定（503 本身就可能是这个场景）
  return true
}

/**
 * 判断错误是不是「网络层失败」而非服务端明确拒绝。
 *
 * 带 HTTP 状态码说明服务端答过了（403/404/409 重试无意义）；
 * 其它异常（fetch failed / 超时）意味着连响应都没有，任务可能已启动。
 *
 * 用结构判断而不是 `instanceof ApiError`：跨模块边界时 instanceof 会失效。
 */
export function isOperationNetworkUncertain(err: unknown): boolean {
  if (err === null || typeof err !== 'object') return true
  const status = (err as { status?: unknown }).status
  return typeof status !== 'number' || !Number.isFinite(status)
}
