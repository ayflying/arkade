/**
 * 服务器展示相关的小工具
 *
 * 单独成文件的原因：「显示名优先、回退到原始名」这条规则在
 * 服务器列表、概览页、面包屑、布局标题多处都要用，
 * 各写一遍必然出现某处忘了回退、显示成空白的情况。
 */

import type { Agent } from '@/types/agent'

/**
 * 取服务器的展示名。
 *
 * 优先用用户设置的 display_name；未设置（空串/纯空格）时回退到 Agent 自报的 name，
 * 再回退到 id——保证任何情况下都不会出现「空白名字」让表格出现无法辨认的行。
 */
export function agentDisplayName(agent: Agent | null | undefined): string {
  if (!agent) return ''
  const display = (agent.display_name ?? '').trim()
  if (display !== '') return display
  const name = (agent.name ?? '').trim()
  if (name !== '') return name
  return agent.id
}

/**
 * 是否已被管理员禁用。
 *
 * 禁用与离线是两回事：
 * - 离线是 Agent 掉线（被动，用户无需处理，恢复后会自己回来）；
 * - 禁用是管理员主动关闭（主动意图，需要手动启用才恢复）。
 * 因此界面上要分开表达，不能都显示成「离线」。
 */
export function isAgentDisabled(agent: Agent | null | undefined): boolean {
  return agent?.disabled === true
}
