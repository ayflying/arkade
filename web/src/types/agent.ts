/**
 * Agent（服务器）相关类型
 *
 * 对应后端契约 GET /api/agents：
 * {
 *   "agents": [{
 *     "id": "...", "name": "...", "status": "online|offline|upgrading",
 *     "version": "...", "runtime": "docker|podman|none", "runtime_version": "...",
 *     "last_seen": 1700000000, "labels": { "group": "测试" }
 *   }]
 * }
 *
 * 注意：labels 是 object（label 名 → 值），不是数组，因此用 Record<string, string>。
 *
 * 单体类型 Agent 刻意用 `type` 而非 `interface`：Naive UI 表格的 data 要求
 * 「带字符串索引签名的对象」，interface 不产生隐式索引签名，会卡住通用表格组件的
 * 泛型约束（详见 types/table.ts 的说明）。
 */

/** Agent 连接状态 */
export type AgentStatus = 'online' | 'offline' | 'upgrading'

/** Agent 探测到的容器运行时 */
export type RuntimeKind = 'docker' | 'podman' | 'none'

export type Agent = {
  id: string
  /**
   * 展示名。
   *
   * 说明：改造后服务器支持「显示名 / 备注」编辑，`name` 是 Agent 自报的原始名，
   * `display_name` 是用户在 Arkade 里改的显示名。页面上应优先展示 display_name，
   * 没有时回退到 name（见 utils/agentDisplay.ts）。
   */
  name: string
  /** 用户设置的显示名；未设置时为空/undefined */
  display_name?: string
  /** 用户填写的备注 */
  note?: string
  /** 后端可能出现契约外的状态值，用联合 + string 兜底展示，避免脏数据让页面崩 */
  status: AgentStatus | string
  /**
   * 是否被管理员禁用。
   * 禁用后：显示「已禁用」标签，且所有操作入口不可用（不下发任何请求）。
   */
  disabled?: boolean
  version: string
  runtime: RuntimeKind | string
  runtime_version: string
  /** Unix 秒级时间戳（非毫秒） */
  last_seen: number
  labels: Record<string, string>
}

/** GET /api/agents 响应体 */
export type AgentListResponse = {
  agents: Agent[]
}

/**
 * PATCH /api/agents/{id} 请求体：编辑服务器。
 *
 * 三个字段都可选——只提交用户改过的部分，避免把未改动的字段一起覆盖。
 * 注意 `name` 不可改：它是 Agent 自报的标识，改名走 display_name。
 */
export type UpdateAgentRequest = {
  display_name?: string
  note?: string
  labels?: Record<string, string>
}

/**
 * PATCH /api/agents/{id} 响应体。
 *
 * ⚠️ 注意是**包装**对象 `{ agent: {...} }`，不是裸的 agent：
 * 后端为了和列表统一字段形状（同样带 display_name/note/disabled/runtime_version）
 * 复用了列表 item 的形状，但额外包了一层。
 *
 * 解包放在 api 层完成（api/agents.ts 的 updateAgent），
 * 让调用方拿到的永远是 Agent 本身——否则每个调用点都要记得多剥一层，
 * 早晚有人写成 `res.id` 拿到 undefined。
 */
export type UpdateAgentResponse = {
  agent: Agent
}

/** 禁用 / 启用服务器的响应体：同样是包装对象 */
export type AgentToggleResponse = {
  agent: Agent
}

/**
 * DELETE /api/agents/{id} 响应体。
 *
 * `message` 是后端写好的中文说明（明确「不删除容器、编排等资产」），
 * 直接展示给用户即可，前端不要再自己拼一遍，否则两边说法容易不一致。
 */
export type DeleteAgentResponse = {
  /**
   * 删除后有多少编排**配置仍留在 Hub**（原属主服务器已被移除）。
   *
   * ⚠️ 与编排列表接口里的 `orphans` 方向相反，别混用：
   * - 编排列表的 `orphans` = Agent 上有、Hub 里没有 → 可以在编排页看到并纳管；
   * - 这里的 `orphan_stacks` = Hub 里有、但服务器被删了 → 界面上**已无入口可访问**。
   *
   * 因此不能对用户说「可在编排列表中查看」——那是假的。
   * 正确说法：「配置仍保留在 Hub，服务器重新接入后可继续访问」。
   */
  orphan_stacks: number
  /** 后端给出的中文结果说明 */
  message: string
}
