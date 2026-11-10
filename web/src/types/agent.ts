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

/**
 * Agent 连接状态。
 *
 * - `online`   已连接
 * - `offline`  曾连接过，当前断开
 * - `pending`  已在 Hub 预创建、从未连接过（专属密钥已生成但没人拿它启动过 Agent）
 * - `upgrading` 升级中
 *
 * `pending` 必须与 `offline` 区分开：
 * offline 是「本来连得好好的，掉了」，pending 是「还没开始接过」。
 * 混成一类会让用户对着一个从没部署成功的服务器去查网络问题。
 */
export type AgentStatus = 'online' | 'offline' | 'pending' | 'upgrading'

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
  /**
   * 该服务器对外可访问的直连地址（用户在浏览器里能打开的地址）。
   *
   * 用途：容器的 published_ports 里 `0.0.0.0` / `::` 这类通配绑定本身
   * 不是可点击的地址，需要用这个字段替换成真实可达的主机。
   *
   * 只允许主机部分：不带 scheme（`http://`）、不带路径、不带端口。
   * IPv6 需要方括号形式（`[2001:db8::1]`）。可为空——空表示用户没指定，
   * 此时不做任何地址推断，也不生成链接。
   */
  direct_address?: string
}

/** GET /api/agents 响应体 */
export type AgentListResponse = {
  agents: Agent[]
}

/**
 * PATCH /api/agents/{id} 请求体：编辑服务器。
 *
 * 字段都可选——只提交用户改过的部分，避免把未改动的字段一起覆盖。
 * 注意 `name` 不可改：它是 Agent 自报的标识，改名走 display_name。
 */
export type UpdateAgentRequest = {
  display_name?: string
  note?: string
  labels?: Record<string, string>
  /** 直连地址；提交空串表示清空（后端校验格式） */
  direct_address?: string
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

/**
 * 专属接入密钥（enrollment）。
 *
 * ## 这是整个前端里唯一的敏感值，处理规则必须严格遵守
 *
 * - `token` **只在创建 / 重置的响应里出现一次**，后端只存摘要，之后无法再取回；
 * - 因此它只存在于**当前弹窗组件的局部 ref里**，用完即弃：
 *   不写 Pinia、不写 localStorage / sessionStorage、不进 URL、不 console.log；
 * - 关闭弹窗立即清空，连组件卸载也要清（见 EnrollmentTokenModal）。
 *
 * 违反任意一条，密钥就会通过浏览器历史、开发者工具、备份或截图泄漏出去。
 */
export type Enrollment = {
  /** 一次性明文密钥；仅创建/重置时可获得 */
  token: string
  /** 该密钥对应的服务器 id */
  agent_id: string
}

/**
 * POST /api/agents 响应体：预创建服务器。
 *
 * 与 PATCH 同样是包装形状 `{ agent, enrollment }`。
 * `agent.status` 为 `pending`，`last_seen` 为 0——因为还没有任何 Agent 连上来过。
 */
export type CreateAgentResponse = {
  agent: Agent
  enrollment: Enrollment
}

/** POST /api/agents/{id}/token/reset 响应体：重置专属密钥 */
export type ResetAgentTokenResponse = {
  agent: Agent
  enrollment: Enrollment
}

/**
 * POST /api/agents 请求体：预创建服务器。
 *
 * `name` 是显示名且必填；`note` / `labels` 可选。
 * 重名时后端返回 409，页面需据此给出「名称已被占用」的明确提示。
 */
export type CreateAgentRequest = {
  name: string
  note?: string
  labels?: Record<string, string>
}
