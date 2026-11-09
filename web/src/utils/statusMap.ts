/**
 * 状态文案与颜色映射（各页面共用，保证口径一致）
 *
 * 设计原因：徽标文案/颜色分散在组件里最容易出现「同一个 online 在一处叫在线、
 * 一处叫正常」的不一致，统一收敛在这里。所有映射函数都对未知值兜底，
 * 后端新增状态值（例如 upgrading 之外的维护态）也不会让界面空白或报错。
 */

/** Naive UI Tag 支持的 type 集合（与 n-tag 的 type 属性一致） */
export type TagType = 'default' | 'success' | 'info' | 'warning' | 'error'

export interface StatusStyle {
  /** 中文文案 */
  label: string
  /** n-tag 的类型（决定配色） */
  type: TagType
  /** 是否整行置灰（离线行视觉弱化） */
  dimmed: boolean
}

/** Agent 状态映射：online 在线 / offline 离线 / upgrading 升级中 */
export function mapAgentStatus(status: string): StatusStyle {
  switch (status) {
    case 'online':
      return { label: '在线', type: 'success', dimmed: false }
    case 'offline':
      return { label: '离线', type: 'error', dimmed: true }
    case 'upgrading':
      return { label: '升级中', type: 'warning', dimmed: false }
    default:
      // 未知状态：灰色显示原值，避免前端把新状态误判为可操作
      return { label: status === '' ? '未知' : status, type: 'default', dimmed: true }
  }
}

/** 容器来源映射：arkade / adopted / discovered，颜色区分见架构文档 4.2 */
export function mapContainerOrigin(origin: string): StatusStyle {
  switch (origin) {
    case 'arkade':
      return { label: '方舟部署', type: 'info', dimmed: false }
    case 'adopted':
      return { label: '已接管', type: 'warning', dimmed: false }
    case 'discovered':
      return { label: '未接管', type: 'default', dimmed: false }
    default:
      return { label: origin === '' ? '未知' : origin, type: 'default', dimmed: false }
  }
}

/** 容器状态映射：只区分运行/停止两档配色，文案保留后端原始 state 便于排障 */
export function mapContainerState(state: string): StatusStyle {
  switch (state) {
    case 'running':
      return { label: '运行中', type: 'success', dimmed: false }
    case 'exited':
    case 'stopped':
      return { label: '已停止', type: 'default', dimmed: false }
    case 'paused':
      return { label: '已暂停', type: 'warning', dimmed: false }
    case 'restarting':
      return { label: '重启中', type: 'warning', dimmed: false }
    case 'created':
      return { label: '已创建', type: 'info', dimmed: false }
    case 'dead':
      return { label: '异常退出', type: 'error', dimmed: false }
    default:
      return { label: state === '' ? '未知' : state, type: 'default', dimmed: false }
  }
}

/** 容器运行时映射：docker / podman / none */
export function mapRuntime(runtime: string): string {
  switch (runtime) {
    case 'docker':
      return 'Docker'
    case 'podman':
      return 'Podman'
    case 'none':
      return '未探测到运行时'
    default:
      return runtime === '' ? '未知' : runtime
  }
}
