/**
 * 镜像任务相关类型（pull / export / import / migrate）
 *
 * 契约来源：Lead 定稿的 Hub API，字段名逐条对照，不自行增删。
 *
 * ## 三个必须守住的事实（会影响界面文案，不写清楚就会误导用户）
 *
 * 1. **任务只存在 Hub 内存里**：Hub 重启后任务列表就没了。
 *    这不是「任务丢了」的 bug——镜像已经拉到/导出到的那部分，引擎侧状态是真实的。
 *    但界面不能承诺「随时能回来查看」，必须明说。
 *
 * 2. **导出的下载链接 30 分钟后过期**。链接是走登录 Cookie 的普通链接
 *    （不是预签名 URL），所以过期后重新点一次会重新校验权限，可能因权限变更被拒。
 *
 * 3. **取消不回滚已完成的写入**：取消只是「不再继续」，
 *    引擎已经写进去的层/已经导入的部分不会撤回。
 *    对 import/migrate 还可能已经更新了同名 tag。
 */

/**
 * 任务种类。
 *
 * `migrate` = 跨服务器复制镜像：**不启动容器、不删源**。
 */
export type ImageTaskKind = 'pull' | 'export' | 'import' | 'migrate'

/** 任务状态。取消后进入 canceled，不会回到 running */
export type ImageTaskStatus = 'pending' | 'running' | 'succeeded' | 'failed' | 'canceled'

/**
 * 镜像任务。
 *
 * 时间字段是 **ISO 字符串**（`created_at` / `finished_at`），
 * 与本项目其他接口的 Unix 秒不同——这是后端实际给的形状，展示时直接格式化字符串，
 * 不要当秒数传进 formatTime 的工具函数。
 */
export interface ImageTask {
  id: string
  kind: ImageTaskKind
  /** 执行任务的服务器（pull/import/export 就是它；migrate 的源） */
  agent_id: string
  /** migrate 的目标服务器；其他种类没有此字段 */
  target_agent_id?: string
  status: ImageTaskStatus
  /**
   * 阶段说明，直接展示给用户（如「下载层 3/7」「导入中」「等待取消」）。
   * 后端给的是**人类可读文案**，前端不要再翻译一遍。
   */
  phase: string
  /** 失败原因或成功摘要，直接展示 */
  message: string
  /**
   * 进度 0..1；**`null` 表示无法得知进度**。
   *
   * 绝不能把 null 当 0 显示成「0%」——
   * 很多阶段（如导出时不知道总大小、pull 的某些阶段）本来就算不出百分比。
   * 界面遇到 null 就显示不确定进度条/「—」，而不是一个假装在动的 0%。
   */
  progress: number | null
  /** 已传输字节数；未知时后端给 0，配合 bytes_total 判断 */
  bytes_done: number
  /** 总字节数；**`null` = 未知**（与 progress 同理，不能当 0） */
  bytes_total: number | null
  /** 创建时间（ISO 字符串） */
  created_at: string
  /** 结束时间（ISO 字符串）；未结束时没有此字段 */
  finished_at?: string
  /**
   * 导出是否已可下载。
   *
   * 只有 export 任务、且真正生成完文件后才为 true。
   * 为 true 时下载地址是 `GET /api/image-tasks/{id}/download`（走 Cookie 鉴权）。
   */
  download_ready: boolean
}

/** GET /api/image-tasks?agent_id={id} */
export interface ImageTaskListResponse {
  tasks: ImageTask[]
}

/** GET /api/image-tasks/{id} 与 POST /api/image-tasks/{id}/cancel */
export interface ImageTaskResponse {
  task: ImageTask
}

/** POST /api/agents/{id}/images/pull 请求体 */
export interface PullImageRequest {
  /** 镜像引用，如 `nginx:1.27` */
  reference: string
}

/** POST /api/agents/{id}/images/export 请求体 */
export interface ExportImageRequest {
  reference: string
}

/**
 * POST /api/agents/{id}/images/migrate 请求体。
 *
 * `confirmation: true` 是硬性要求：迁移会往目标服务器写入镜像，
 * 且**可能更新目标上已存在的同名 tag**，必须由用户明确确认。
 */
export interface MigrateImageRequest {
  reference: string
  /** 目标服务器 id；不能与源相同 */
  target_agent_id: string
  confirmation: true
}

/**
 * 提交类接口统一返回 `202 {task}`。
 *
 * 202 表示「已接受、正在执行」——不是「已完成」。
 * 页面必须依据 task.status 继续轮询，不能因为拿到 202 就当成功。
 */
export type ImageTaskAcceptedResponse = ImageTaskResponse
