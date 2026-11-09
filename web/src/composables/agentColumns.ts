/**
 * Agent 表格列定义
 *
 * 为什么单独成文件：列表页要把「列定义」和「加载/跳转/刷新逻辑」分开，
 * 避免单文件过长；列定义是纯渲染声明，不依赖页面状态，便于复用与审阅。
 *
 * 离线行的处理（需求硬性要求）：
 * - 整行置灰：通过 rowProps 加 class，父页面用 :deep(.row--offline) 降透明度；
 * - 操作按钮的可用性由 AgentRowActions 自己判断并给出 tooltip 说明。
 */

import { h } from 'vue'
import type { DataTableColumns } from 'naive-ui'
import AgentStatusTag from '@/components/agent/AgentStatusTag.vue'
import AgentLabelList from '@/components/agent/AgentLabelList.vue'
import RelativeTime from '@/components/common/RelativeTime.vue'
import AgentRowActions from '@/components/agent/AgentRowActions.vue'
import type { Agent } from '@/types/agent'
import { agentDisplayName, isAgentDisabled } from '@/utils/agentDisplay'
import { mapAgentStatus, mapRuntime } from '@/utils/statusMap'

/** 离线行的 class 名，父组件样式与测试都依赖这个常量 */
export const OFFLINE_ROW_CLASS = 'agent-row--offline'

/**
 * 列定义的处理器集合。
 *
 * 全部是回调而非直接调接口：页面负责二次确认与请求编排，
 * 列定义保持纯函数，便于测试与复用。
 */
export interface AgentColumnHandlers {
  onEnter: (agent: Agent) => void
  onEdit: (agent: Agent) => void
  onResetToken: (agent: Agent) => void
  onToggleDisabled: (agent: Agent) => void
  onDelete: (agent: Agent) => void
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
      render: (row) => {
        // 展示名优先：用户设置了 display_name 时显示它，原始名放进 tooltip
        const label = agentDisplayName(row)
        const showOriginal = (row.display_name ?? '').trim() !== '' && row.name !== ''
        return h(
          'a',
          {
            class: 'agent-name-link',
            title: showOriginal ? `原始名称：${row.name}` : row.id,
            onClick: (e: MouseEvent) => {
              // 阻止冒泡，避免触发行点击重复跳转
              e.stopPropagation()
              handlers.onEnter(row)
            }
          },
          label
        )
      }
    },
    {
      title: '状态',
      key: 'status',
      width: 110,
      render: (row) => h(AgentStatusTag, { status: row.status, disabled: isAgentDisabled(row) })
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
          row.runtime_version
            ? h('span', { class: 'runtime-cell__version' }, ` ${row.runtime_version}`)
            : null
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
      minWidth: 180,
      render: (row) => h(AgentLabelList, { labels: row.labels ?? {}, max: 3 })
    },
    {
      title: '备注',
      key: 'note',
      minWidth: 160,
      ellipsis: { tooltip: true },
      render: (row) => row.note || '—'
    },
    {
      title: '操作',
      key: 'actions',
      // 五个按钮横排，给足宽度；窄屏靠 scroll-x 横向滚动兜底
      width: 300,
      fixed: 'right',
      render: (row) =>
        h(AgentRowActions, {
          agent: row,
          busy: handlers.isBusy(row.id),
          onEnter: () => handlers.onEnter(row),
          onEdit: () => handlers.onEdit(row),
          'onReset-token': () => handlers.onResetToken(row),
          'onToggle-disabled': () => handlers.onToggleDisabled(row),
          onDelete: () => handlers.onDelete(row)
        })
    }
  ]
}

/**
 * 行属性：离线/未知/pending 状态的行加上置灰 class。
 *
 * pending 也置灰，并且额外加一个「不可点」的标记：
 * 它没有容器数据可看，整行置灰能让用户点之前就预期到「这台还没通」，
 * 而不是点进去看到空列表再疑惑。真正的拦截在页面 enterAgent 里。
 */
export function agentRowProps(row: Agent): Record<string, unknown> {
  const style = mapAgentStatus(row.status)
  return style.dimmed ? { class: OFFLINE_ROW_CLASS } : {}
}
