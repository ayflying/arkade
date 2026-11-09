/**
 * 服务器资源类型：本地镜像 / 网络 / 存储卷
 *
 * 三者都是「服务器上的资源」而非容器，共享同一套列表 + 删除的交互模式，
 * 因此放在同一个文件里；各自的组件仍按职责拆在 components/resource/ 下。
 *
 * 时间字段：`created` 为 **Unix 秒**，展示走 utils/formatTime。
 *
 * 注意：这里的单体类型（ImageInfo / NetworkInfo / VolumeInfo）刻意用
 * `type` 而不是 `interface`。原因是 NDataTable 的 data 要求 `RowData`
 * （带字符串索引签名的对象），而 interface 不产生隐式索引签名，
 * 会导致通用表格组件（ResourcePage）的泛型约束不通过。
 * 用 type 别名则天然满足，页面里就不必写 as 断言。
 */

/** GET /api/agents/{id}/images 响应中的单个镜像 */
export type ImageInfo = {
  /** 完整镜像 ID（sha256），删除时用作路径参数 */
  id: string
  /** 仓库名:标签列表；同一个镜像 ID 可能有多个标签 */
  tags: string[]
  /** 大小（字节），展示时换算成 MB/GB */
  size: number
  /** 创建时间（Unix 秒） */
  created: number
  /** 引用该镜像的容器数量；>0 时不加 force 删不掉，前端要提示 */
  containers: number
}

export type ImageListResponse = {
  images: ImageInfo[]
}

/** GET /api/agents/{id}/networks 响应中的单个网络 */
export type NetworkInfo = {
  id: string
  name: string
  /** 驱动类型：bridge / host / overlay / null 等 */
  driver: string
  /** 作用范围：local / swarm 等 */
  scope: string
  /** 接入该网络的容器数量；>0 时删除会被拒绝 */
  containers: number
  /** 网络标签 */
  labels: Record<string, string>
  /** 是否为 Docker 预置网络（bridge/host/none）：这类网络删不掉也不该删 */
  builtin?: boolean
}

export type NetworkListResponse = {
  networks: NetworkInfo[]
}

/** GET /api/agents/{id}/volumes 响应中的单个卷 */
export type VolumeInfo = {
  /** 卷名，删除时用作路径参数 */
  name: string
  driver: string
  /** 宿主机上的挂载点路径 */
  mountpoint: string
  /**
   * 是否正被容器使用。
   * true 时删除会被后端拒绝；即便能删，前端也必须在确认框里强调
   * 「卷内数据将永久丢失且不可恢复」。
   */
  in_use: boolean
  labels: Record<string, string>
}

export type VolumeListResponse = {
  volumes: VolumeInfo[]
}
