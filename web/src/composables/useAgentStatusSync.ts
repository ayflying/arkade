/**
 * 让「当前服务器的在线状态」跟着后端走
 *
 * ## 为什么必须有
 *
 * `useCurrentAgent().isOnline` 读的是 agents store 的缓存，而 store 只在两个时机更新：
 * 打开服务器列表页、或首次深链进入时补拉一次。页面一旦打开，状态就定格了：
 * Hub 容器重启、内置本机执行器重新注册、远程 Agent 重连之后，页面仍然显示
 * 「服务器离线」，启动 / 保存并部署等按钮全部禁用。
 *
 * 实际踩到的现象：Hub 重启完成后，一直开着的编排详情页仍然提示
 * 「服务器当前离线，无法执行该操作」，而服务器列表页里这台机器已经是「在线 / 刚刚」，
 * 必须手动刷新整页才恢复——静默失败，用户只会以为平台坏了。
 *
 * ## 校准时机
 *
 * - **挂载时立即校准一次**：store 里可能是很久以前（上次打开列表页时）的快照，
 *   深链进入时尤其明显；这里传 0 绕过节流，保证首屏拿到真实状态。
 * - **自动刷新开启时按 interval 周期校准**：与其它页面的轮询同节奏（5 秒），
 *   恢复速度对用户来说几乎是实时的。
 * - **标签页从后台切回**：usePolling 在恢复可见时会立刻补一轮，不用等下一个周期。
 *
 * 关闭「自动刷新」时只剩挂载时那一次校准——这是用户的显式选择，
 * 与其它页面的轮询口径一致。
 *
 * ## 为什么放在 useCurrentAgent 里
 *
 * 在线状态是所有服务器页面（容器、编排、镜像、网络、存储卷、详情、创建）共用的，
 * 在 useCurrentAgent 里统一挂载，等于「一处修复，全部页面生效」；
 * 各页面自己写一遍必然出现有页面忘了接、行为不一致的问题。
 *
 * 请求本身由 store 的 syncStatus 做在途去重 + 最小间隔节流，
 * 所以布局、页面、面包屑多处同时使用也不会重复打后端。
 */

import { getCurrentScope, onMounted } from 'vue'
import { useAgentsStore } from '@/stores/agents'
import { usePolling } from '@/composables/usePolling'

/**
 * 周期校准间隔。
 *
 * 取 5 秒与容器/编排列表的轮询一致：服务器上下线属于低频事件，
 * 但「等它变绿」往往正是用户此刻唯一关心的事，太慢会被当成没反应。
 */
const STATUS_POLL_INTERVAL_MS = 5000

/**
 * 在当前组件作用域内挂载状态校准。
 *
 * 必须在 setup 中调用（内部依赖 onMounted / onScopeDispose）。
 */
export function useAgentStatusSync(): void {
  /*
   * 依赖组件作用域（onMounted / onScopeDispose）。万一将来有人在 setup 之外
   * 调用 useCurrentAgent，这里直接跳过，既不让 Vue 打出
   * 「no active component instance」告警，也不会把定时器挂到错误的作用域上。
   */
  if (getCurrentScope() === undefined) return

  const store = useAgentsStore()

  // 挂载即校准：不信任 store 里可能过期的在线状态（传 0 跳过节流）
  onMounted(() => {
    void store.syncStatus(0)
  })

  /*
   * 周期校准走 usePolling：页面隐藏暂停、切回立刻补一轮、连续失败退避、
   * 组件卸载自动清理，这些都不再自己实现一遍。
   * immediate: false —— 挂载那次已由上面的 onMounted 负责，避免重复请求。
   */
  usePolling(
    async () => {
      await store.syncStatus()
    },
    { name: 'agent-status', interval: STATUS_POLL_INTERVAL_MS, immediate: false }
  )
}
