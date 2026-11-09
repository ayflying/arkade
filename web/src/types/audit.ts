/**
 * 操作审计类型
 *
 * 对应契约：
 *   GET /api/audit?limit=&offset=&user_id=&agent_id=&action=&result=&from=&to=
 *   GET /api/audit/actions → { actions: [{action, label}] }
 *
 * 分页用 **limit/offset**，不是 page/page_size：
 * 前端负责把页码换算成 offset=(page-1)*limit，换算逻辑集中在 api 层，
 * 页面只关心「第几页」。
 *
 * 时间字段 `created_at` 为 **Unix 秒**（与项目其余接口一致），展示走 utils/formatTime。
 * 筛选参数 from/to 后端同时接受 Unix 秒与 RFC3339，这里统一传秒，避免时区歧义。
 *
 * 注意：本文件里的单体类型用 `type` 而非 `interface`——
 * NDataTable 的 data 要求带字符串索引签名的对象，interface 不产生隐式索引签名，
 * 会卡住通用表格组件的泛型约束（同 types/resource.ts 的说明）。
 */

/**
 * 审计结果。
 *
 * ⚠️ 后端取值是 `success` / `failed`（是 **failed**，不是 failure）。
 * 详见 hub/internal/audit/actions.go 的 ResultSuccess / ResultFailed 常量，
 * 以及 handlers_audit.go 里对 result 筛选参数的校验——传 failure 会被判为非法值。
 *
 * 保留 `| string`：后端将来可能新增取值，前端按原样展示而不是渲染成空白。
 */
export type AuditResult = 'success' | 'failed' | string

/** 结果常量：集中在这里，避免页面里散落字符串字面量拼错 */
export const AUDIT_RESULT_SUCCESS = 'success'
export const AUDIT_RESULT_FAILED = 'failed'

/**
 * 登录动作标识。
 *
 * 单独抽成常量：只有这条动作在失败时，`username` 才是**用户输入的**名称
 * （账号可能不存在），界面据此决定是否加「可能并非已存在的账号」的说明。
 * 取值见 hub/internal/audit/actions.go 的 ActionUserLogin。
 */
export const AUDIT_ACTION_USER_LOGIN = 'user.login'

/** 一条审计记录 */
export type AuditEntry = {
  id: string
  /**
   * 操作人 id。
   *
   * 为空有两种情况，**都表示身份没确定，不能当成「系统」**：
   * 1. 未通过认证的请求（middleware 在 !hasUser 时把 user_id/username 一起清空）；
   * 2. 登录失败的记录（账号可能根本不存在）。
   */
  user_id: string
  /**
   * 操作人用户名快照。
   *
   * 两种特殊来源，展示时必须区分：
   * - **登录失败**时写的是用户**输入**的名称，该账号可能不存在
   *   （见 handlers_auth.go 的 writeLoginAudit 注释）；
   * - 未认证请求时为空串（middleware 会一起清空。
   * 不能把空名一律标成「系统」——那是在伪造身份。
   */
  username: string
  /** 所属服务器 id */
  agent_id: string
  /** 所属服务器名称 */
  agent_name: string
  /** 动作标识，如 container.start */
  action: string
  /** 动作的中文描述，优先展示这个 */
  action_label: string
  /** 操作目标（容器名/编排名/资源名等） */
  target: string
  /** 补充说明 */
  detail: string
  /** 执行结果 */
  result: AuditResult
  /** 失败时的错误信息 */
  error: string
  /** 耗时（毫秒） */
  duration_ms: number
  /** 发生时间（Unix 秒） */
  created_at: number
}

/** 审计列表响应 */
export type AuditListResponse = {
  /** 符合条件的总条数，用于分页 */
  total: number
  /** 当前页记录 */
  items: AuditEntry[]
}

/**
 * 审计查询条件（接口层用的形态）。
 *
 * 注意这里用的是 limit/offset 与 user_id，
 * 与页面上的「页码 / 操作人用户名」是两个层次，换算在 api 层完成。
 */
export type AuditQuery = {
  limit?: number
  offset?: number
  /** 操作人 id */
  user_id?: string
  /** 服务器 id */
  agent_id?: string
  /** 动作标识 */
  action?: string
  /** 结果筛选 */
  result?: string
  /** 起始时间（Unix 秒） */
  from?: number
  /** 结束时间（Unix 秒） */
  to?: number
}

/** 动作字典项：GET /api/audit/actions 的返回 */
export type AuditActionOption = {
  action: string
  label: string
}

/** 动作字典响应 */
export type AuditActionsResponse = {
  actions: AuditActionOption[]
}
