/**
 * Agent 表格列定义
 *
 * 为什么单独成文件：列表页要把「列定义」和「加载/跳转/刷新逻辑」分开，
 * 避免单文件过长；列定义是纯渲染声明，不依赖页面状态，便于复用与审阅。
 *
 * 离线行的处理（需求硬性要求）：
 * - 整行置灰：通过 rowProps 加 class，父页面用 :deep(.row--offline) 降透明度；
 * - 操作按钮 disabled + tooltip 说明原因：见 render 里的 AgentRowActions 组件。
 */

import { h } from 'vue'
import type { DataTableColumns } from 'naive-ui'
import AgentStatusTag from '@/components/agent/AgentStatusTag.vue'
import AgentLabelList from '@/components/agent/AgentLabelList.vue'
import RelativeTime from '@/components/common/RelativeTime.vue'
import AgentRowActions from '@/components/agent/AgentRowActions.vue'
import type { Agent } from '@/types/agent'
import { mapAgentStatus, mapRuntime } from '@/utils/statusMap'

/** 离线行的 class 名，父组件样式与测试都依赖这个常量 */
export const OFFLINE_ROW_CLASS = 'agent-row--offline'

/** 列的渲染函数需要两个回调：进入容器页、当前行是否正被操作 */
export interface AgentColumnHandlers {
  onEnter: (agent: Agent) => void
  isBusy: (agentId: string) => boolean
}

/**
 * 构建表格列。
 *
 * 说明：这里刻意不使用 useMessage 之类的组合式 API，
 * 所有交互都通过 handlers 回调交给页面，保持列定义纯函数化。
 */
export function createAgentColumns(handlers: AgentColumnHandlers): DataTableColumns<Agent> {
  return [
    {
      title: '名称',
      key: 'name',
      minWidth: 180,
      ellipsis: { tooltip: true },
      // 名称列做成可点击入口，同时整行也支持点击进入
      render: (row) =>
        h(
          'a',
          {
            class: 'agent-name-link',
            onClick: (e: MouseEvent) => {
              // 阻止冒泡，避免触发行点击重复跳转
              e.stopPropagation()
              if (row.status === 'online' || row.status === 'upgrading' || row.status === 'offline') {
                handlers.onEnter(row)
              }
            }
          },
          row.name || row.id
        )
    },
    {
      title: '状态',
      key: 'status',
      width: 110,
      render: (row) => h(AgentStatusTag, { status: row.status })
    },
    {
      title: '引擎',
      key: 'runtime',
      width: 150,
      render: (row) => {
        const runtime = mapRuntime(row.runtime)
        // 引擎版本为空时不显示多余的括号
        return h('span', { class: 'runtime-cell' }, [
          h('span', runtime),
          row.runtime_version ? h('span', { class: 'runtime-cell__version' }, ` ${row.runtime_version}`) : null
        ])
      }
    },
    {
      title: 'Agent 版本',
      key: 'version',
      width: 120,
      render: (row) => h('span', row.version || '—')
    },
    {
      title: '最后心跳',
      key: 'last_seen',
      width: 130,
      render: (row) => h(RelativeTime, { seconds: row.last_seen, prefix: '最后心跳' })
    },
    {
      title: '标签',
      key: 'labels',
      minWidth: 200,
      render: (row) => h(AgentLabelList, { labels: row.labels ?? {}, max: 3 })
    },
    {
      title: '操作',
      key: 'actions',
      width: 120,
      align: 'right',
      render: (row) =>
        h(AgentRowActions, {
          agent: row,
          busy: handlers.isBusy(row.id),
          onEnter: () => handlers.onEnter(row)
        })
    }
  ]
}

/**
 * 行属性：离线/未知状态的行加上置灰 class。
 * 置灰是视觉弱化，真正的拦截在 AgentRowActions 里（disabled + tooltip），
 * 两者缺一不可：只置灰不拦截仍可能误点，只拦截不置灰用户不明白为什么点不动。
 */
export function agentRowProps(row: Agent): Record<string, unknown> {
  const style = mapAgentStatus(row.status)
  return style.dimmed ? { class: OFFLINE_ROW_CLASS } : {}
}
