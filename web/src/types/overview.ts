/**
 * 全局概览页的数据模型
 *
 * 概览页没有专属后端接口，数据由已有接口聚合而来：
 * - GET /api/agents                       → 服务器列表（含 online 状态）
 * - GET /api/agents/{id}/containers       → 每台的容器数
 * - GET /api/agents/{id}/stacks           → 每台的编排数与 drift / orphans
 * - GET /api/agents/{id}/images|networks|volumes → 每台的资源数
 *
 * ## 为什么这些类型里会有「部分失败」字段
 *
 * 聚合请求是 N 台服务器 × 5 个接口，**任何一台离线都会失败**（后端对离线 Agent
 * 返回 503）。如果一处失败就让整个概览页报错，用户明明能看到别的服务器也白搭。
 * 因此这里的模型按「尽力而为」设计：失败的那台记进 failures，其余统计照常显示，
 * 并在界面上明确告诉用户「哪些服务器没统计进来」，绝不假装是全量。
 */

import type { Agent } from './agent'
import type { StackSummary } from './stack'

/** 单台服务器的资源计数 */
export type AgentResourceCounts = {
  containers: number
  runningContainers: number
  /** Hub 已纳管的编排数 */
  stacks: number
  /**
   * 服务器上已有、但 Hub 未纳管的 compose 项目数。
   *
   * 必须与 `stacks` 分开计数：两者都不为空时页面要说清「N 个已纳管 + M 个未纳管」。
   * 全都并进 `stacks` 会让用户以为那些未纳管项目也能在这里管理（实际不能）。
   */
  unmanagedStacks: number
  images: number
  networks: number
  volumes: number
}

/** 单台服务器聚合后的概览数据 */
export type AgentOverview = {
  agent: Agent
  counts: AgentResourceCounts
  /** 该服务器上存在配置漂移的编排名 */
  driftedStacks: string[]
  /** 该服务器上未纳管的编排名（Agent 有、Hub 没有） */
  orphanStacks: string[]
}

/** 单个资源接口的失败记录 */
export type AggregationFailure = {
  agentId: string
  /** 服务器展示名，便于直接展示给用户 */
  agentName: string
  /** 失败的资源种类（中文），用于说明「哪类数据没统计到」 */
  resource: string
  /** 中文失败原因 */
  message: string
}

/** 概览页整体数据 */
export type OverviewData = {
  /** 已成功聚合的服务器 */
  agents: AgentOverview[]
  /** 聚合失败的项目（部分失败不影响其他服务器统计） */
  failures: AggregationFailure[]
  /** 实际参与统计的服务器台数 */
  countedAgents: number
  /** 服务器总台数（可能大于 countedAgents，因为超出上限的部分没有拉取） */
  totalAgents: number
  /** 是否因为服务器过多而只统计了前 N 台 */
  truncated: boolean
  /** 全局汇总 */
  totals: {
    agents: number
    onlineAgents: number
    containers: number
    runningContainers: number
    /** Hub 已纳管的编排总数 */
    stacks: number
    /**
     * 服务器上已有、但未被 Hub 纳管的 compose 项目总数。
     *
     * 单独给出来是为了修掉一个误导：全部项目都未纳管时，
     * 「容器编排 0 个」会让用户以为服务器上一个编排都没有，
     * 实际他机器上跑着一堆。界面必须显示「0 个已纳管，另有 M 个未纳管」。
     */
    unmanagedStacks: number
    images: number
    networks: number
    volumes: number
  }
  /** 异常汇总 */
  problems: {
    offlineAgents: Agent[]
    driftedStacks: Array<{ agentId: string; agentName: string; stackName: string }>
    orphanStacks: Array<{ agentId: string; agentName: string; stackName: string }>
  }
}

/** 一处异常：离线服务器 / 配置漂移 / 未纳管编排 */
export type ProblemKind = 'offline-agent' | 'drift' | 'orphan'

/**
 * 聚合过程中「尽力而为」的包装结果。
 *
 * 用它而不是直接 try/catch 到处写：聚合函数里要并发发起五类请求，
 * 每类都可能失败，统一成这个形状后上层代码只是对数组做过滤。
 */
export type Settled<T> =
  | { ok: true; value: T }
  | { ok: false; message: string }

/** 重新导出，避免概览相关模块各自 import 两个文件 */
export type { Agent, StackSummary }
