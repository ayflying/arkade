/**
 * 容器编排（stacks）接口
 *
 * 契约来源：**后端 task-4/task-5 的实际产出**（已与 Lead 逐条核对）。
 *
 * 所有路径参数都经 encodeURIComponent：
 * 编排名可能含 `/`、`#` 等字符（用户自定义 compose 项目名），不编码会拼出坏 URL。
 */

import { httpDelete, httpGet, httpPost, httpPut } from './request'
import type {
  CreateStackRequest,
  CreateStackResponse,
  StackActionRequest,
  StackActionResponse,
  StackConfig,
  StackDetail,
  StackListResponse,
  StackLogsResponse,
  StackPsResponse,
  UpdateStackRequest,
  UpdateStackResponse
} from '@/types/stack'

/** 拼出 /agents/{id}/stacks 基础路径，避免每个函数重复编码逻辑 */
function stacksBase(agentId: string): string {
  return `/agents/${encodeURIComponent(agentId)}/stacks`
}

/** 拼出单个编排的路径 */
function stackPath(agentId: string, name: string): string {
  return `${stacksBase(agentId)}/${encodeURIComponent(name)}`
}

/**
 * 列出某台服务器上的全部编排。
 *
 * 注意：Agent 离线时本接口**仍返回 200**，各条 `status` 为 'offline'，
 * 且 `agent_online` 为 false。因此调用方不能靠"请求是否失败"判断在线状态，
 * 要看 `agent_online`。
 *
 * 响应里的 `orphans` 是「Agent 上有、Hub 未纳管」的编排名，必须展示。
 */
export async function listStacks(agentId: string): Promise<StackListResponse> {
  return httpGet<StackListResponse>(stacksBase(agentId))
}

/** 获取编排详情（状态、服务、版本号） */
export async function getStack(agentId: string, name: string): Promise<StackDetail> {
  return httpGet<StackDetail>(stackPath(agentId, name))
}

/** 获取 compose.yml 原文与版本号 */
export async function getStackConfig(agentId: string, name: string): Promise<StackConfig> {
  return httpGet<StackConfig>(`${stackPath(agentId, name)}/config`)
}

/**
 * 新建编排。
 *
 * ⚠️ 返回值必须检查 `sync_ok`：Agent 离线或下发失败时后端不报错，
 * 而是返回 200 + sync_ok:false + sync_hint（内容已存 Hub，尚未下发到服务器）。
 * 调用方要用警告提示展示 sync_hint，不能当成部署成功。
 */
export async function createStack(
  agentId: string,
  payload: CreateStackRequest
): Promise<CreateStackResponse> {
  return httpPost<CreateStackResponse>(stacksBase(agentId), payload)
}

/**
 * 保存编排配置。
 *
 * 必须带 version（乐观锁）：后端发现版本不一致会返回冲突(409)，
 * 前端据此提示「内容已被他人修改，请刷新」，避免静默覆盖他人编辑。
 *
 * 同样要检查 `sync_ok`：保存成功不等于已下发到服务器。
 */
export async function updateStack(
  agentId: string,
  name: string,
  payload: UpdateStackRequest
): Promise<UpdateStackResponse> {
  return httpPut<UpdateStackResponse>(stackPath(agentId, name), payload)
}

/**
 * 执行编排操作（启动/停止/重启/更新镜像/重建/备份）。
 *
 * 统一走 actions 接口而非每个操作一个路径：后端只需按 action 分支，
 * 前端也只需维护一份请求体结构。
 *
 * 备份操作可传 `include_volumes` 决定是否把数据卷一起打包。
 */
export async function runStackAction(
  agentId: string,
  name: string,
  payload: StackActionRequest
): Promise<StackActionResponse> {
  return httpPost<StackActionResponse>(`${stackPath(agentId, name)}/actions`, payload)
}

/**
 * 删除编排（204 无响应体）。
 *
 * @param removeVolumes 是否连带删除数据卷。**默认 false**——
 *        删卷是不可恢复的数据丢失，必须由用户在确认框里显式勾选。
 *        这与 docker compose down 的默认行为一致（不加 -v 不删卷）。
 */
export async function deleteStack(
  agentId: string,
  name: string,
  removeVolumes = false
): Promise<void> {
  // 只有明确要求删卷时才带查询参数，默认请求保持最简
  const query = removeVolumes ? '?remove_volumes=true' : ''
  return httpDelete(`${stackPath(agentId, name)}${query}`)
}

/** 查看编排内容器（ps） */
export async function listStackPs(agentId: string, name: string): Promise<StackPsResponse> {
  return httpGet<StackPsResponse>(`${stackPath(agentId, name)}/ps`)
}

/**
 * 查看编排日志。
 *
 * @param service 只查看某个服务；不传表示全部
 * @param tail    返回末尾多少行
 */
export async function fetchStackLogs(
  agentId: string,
  name: string,
  service?: string,
  tail = 200
): Promise<StackLogsResponse> {
  const params = new URLSearchParams({ tail: String(tail) })
  if (service !== undefined && service !== '') {
    params.set('service', service)
  }
  return httpGet<StackLogsResponse>(`${stackPath(agentId, name)}/logs?${params.toString()}`)
}
