package envelope

// 镜像、网络、存储卷与容器编排的载荷定义。
//
// 拆分原因：payload.go 承载连接/容器这类核心结构，本文件承载"资源类"扩展，
// 避免单文件过长。字段改动会同时影响 Hub 与 Agent 两端编译，不会一边改了忘另一边。

// ---------------------------------------------------------------- 镜像

// ImageSummary 是镜像列表项。只保留界面需要的信息。
type ImageSummary struct {
	ID         string   `json:"id"`         // 短 ID（sha256 前缀）
	Tags       []string `json:"tags"`       // 可能为多个 tag；无 tag 时为空（悬空镜像）
	Size       int64    `json:"size"`       // 字节数
	Created    int64    `json:"created"`    // Unix 秒
	Containers int      `json:"containers"` // 引用该镜像的容器数，删除前提示用
	Dangling   bool     `json:"dangling"`   // 悬空镜像（无 tag 无人引用）
}

// ImageListResult 是镜像列表应答。
type ImageListResult struct {
	Images []ImageSummary `json:"images"`
}

// ImageRemovePayload 是删除镜像的请求。
//
// Force 语义与 docker rmi -f 一致：允许删除被容器使用的镜像（容器会保留但镜像引用悬空），
// 属于危险操作，调用方必须二次确认。
type ImageRemovePayload struct {
	ImageID string `json:"image_id"`
	Force   bool   `json:"force"`
}

// ---------------------------------------------------------------- 网络

// NetworkSummary 是网络列表项。
type NetworkSummary struct {
	ID         string            `json:"id"`
	Name       string            `json:"name"`
	Driver     string            `json:"driver"`     // bridge / host / overlay …
	Scope      string            `json:"scope"`      // local / swarm
	Internal   bool              `json:"internal"`   // 内部网络（无外网出口）
	Containers int               `json:"containers"` // 接入的容器数
	Labels     map[string]string `json:"labels"`     // Docker Compose 会打 project 标签，用于识别归属
	Created    int64             `json:"created"`
}

// NetworkListResult 是网络列表应答。
type NetworkListResult struct {
	Networks []NetworkSummary `json:"networks"`
}

// NetworkRemovePayload 是删除网络的请求。
type NetworkRemovePayload struct {
	NetworkID string `json:"network_id"`
}

// ---------------------------------------------------------------- 存储卷

// VolumeSummary 是存储卷列表项。
type VolumeSummary struct {
	Name       string            `json:"name"`
	Driver     string            `json:"driver"`
	Mountpoint string            `json:"mountpoint"`
	Labels     map[string]string `json:"labels"` // Compose 会打 project 标签
	Created    int64             `json:"created"`
	InUse      bool              `json:"in_use"` // 是否有容器正在使用，删除前提示用
}

// VolumeListResult 是存储卷列表应答。
type VolumeListResult struct {
	Volumes []VolumeSummary `json:"volumes"`
}

// VolumeRemovePayload 是删除存储卷的请求。
//
// 这是数据丢失操作：卷里可能存着数据库文件，删除不可恢复。
// 不做 Force 开关——Docker 本身也不允许删除使用中的卷。
type VolumeRemovePayload struct {
	Name string `json:"name"`
}

// ---------------------------------------------------------------- 容器编排

// ComposeProjectSummary 是编排项目摘要。
type ComposeProjectSummary struct {
	// Name 是编排项目名，同时是 Agent 本机目录名，必须符合 [a-z0-9_-]+。
	Name string `json:"name"`
	// Status 是整体状态，由各服务状态归并：running / partial / stopped / unknown。
	Status string `json:"status"`
	// ConfigHash 是本机 compose.yml 的内容哈希（sha256 前 16 位）。
	//
	// 它是双向同步的判定依据：Hub 记录自己下发的哈希，Agent 上报本机哈希，
	// 两者不一致即说明有偏离（本地手改过、或同步没成功），界面要显式告警，
	// 而不是静默用某一端覆盖另一端。
	//
	// **外部项目此字段恒为空串**，理由见 Managed 的说明。
	ConfigHash string `json:"config_hash"`
	// Services 是服务名列表（来自 compose 配置）。
	Services []string `json:"services"`
	// Running 是当前处于 running 的服务数。
	Running int `json:"running"`
	// Total 是服务总数。
	Total int `json:"total"`
	// UpdatedAt 是 compose.yml 在本机的最后修改时间（Unix 秒）。
	// 外部项目为 0（不知道，且不该去猜）。
	UpdatedAt int64 `json:"updated_at"`
	// Managed 表示这个项目是否由 Arkade 管理（工作区里有目录、配置由 Hub 下发）。
	//
	// **false 的含义是"机器上已有、但 Arkade 不拥有它"**，例如运维直接
	// 用 docker compose 起的生产项目。这类项目只能看，不能改：
	// Editable 为 false、ConfigHash 为空、没有目录可写。
	//
	// 用显式布尔而不是靠"ConfigHash 是否为空"隐式判断：
	// 隐式判断会把"新项目还没算过哈希"误判成外部项目，
	// 前端据此隐藏编辑入口——那是一个很难排查的"按钮不见了"故障。
	Managed bool `json:"managed"`
}

// ComposeListResult 是编排列表应答。
type ComposeListResult struct {
	// Projects 是 Arkade 工作区里的项目（可管理）。
	//
	// 保留这个字段名与含义是为了向后兼容：Hub 现有代码与测试都按
	// "工作区项目"理解它。外部项目放在 External 里而不是混进来，
	// 是因为两者可做的操作完全不同，混在一个列表里迟早会被误操作。
	Projects []ComposeProjectSummary `json:"projects"`
	// External 是机器上已有、但 Arkade 未纳管的编排项目。
	//
	// 来源只有 Docker 容器标签（com.docker.compose.project/service），
	// **不读取任何外部配置文件、不执行任何外部 up/down**。
	// 这些项目不属于 Arkade，不得自动导入、不得改动。
	//
	// 新增字段而非复用 Projects：旧版 Hub 不认识它会被忽略（json 反序列化
	// 忽略未知字段），因此这是向后兼容的增量变化。
	External []ComposeProjectSummary `json:"external"`
}

// ComposeSpecPayload 承载一份编排文件内容。
//
// 关键设计：编排文件内容直接在消息里传输，Agent 不访问 Hub 的数据库。
// 这样"两边都存"的同步方向永远只有 Hub → Agent 一条下行链路，
// 上行只传状态与哈希，不会出现双向写冲突。
type ComposeSpecPayload struct {
	Name string `json:"name"`
	// Content 是完整的 compose.yml 文本。
	Content string `json:"content"`
	// ContentHash 是 Hub 侧计算的哈希，Agent 落盘后回报自己算的哈希用于校验。
	ContentHash string `json:"content_hash"`
}

// ComposeOpPayload 是针对已有编排的操作请求。
type ComposeOpPayload struct {
	Name string `json:"name"`
	// Services 为空表示针对整个项目；非空表示只操作指定服务。
	Services []string `json:"services"`
	// RemoveVolumes 仅对 down/remove 有意义：是否连带删除数据卷（-v）。
	RemoveVolumes bool `json:"remove_volumes"`
	// Force 用于 rebuild：是否 --force-recreate。
	Force bool `json:"force"`
}

// ComposeLogsPayload 是编排日志请求。
// Service 为空时取全部服务的日志合并。
type ComposeLogsPayload struct {
	Name      string `json:"name"`
	Service   string `json:"service"`
	TailLines int    `json:"tail_lines"`
}

// ComposeLogsResult 是编排日志应答。
type ComposeLogsResult struct {
	Lines     []string `json:"lines"`
	Truncated bool     `json:"truncated"`
}

// ComposeBackupPayload 是备份请求。
type ComposeBackupPayload struct {
	Name string `json:"name"`
	// IncludeVolumes 为 true 时额外备份数据卷内容（用 tar 打包），体积可能很大。
	IncludeVolumes bool `json:"include_volumes"`
}

// ComposeBackupResult 是备份结果。
//
// 备份产物保存在 Agent 本机固定目录（<data_dir>/backups/<name>/<时间戳>/），
// 不回传二进制内容：编排文件很小，但卷数据可能是几百 MB，
// 通过 WebSocket 传输会拖垮整条连接。
type ComposeBackupResult struct {
	Name      string   `json:"name"`
	Path      string   `json:"path"`       // Agent 本机的备份目录绝对路径
	SizeBytes int64    `json:"size_bytes"` // 备份总大小
	Files     []string `json:"files"`      // 备份内含的文件清单
	CreatedAt int64    `json:"created_at"`
}

// ComposeOpResult 是编排操作结果，回传最新状态，前端无需再查一次。
type ComposeOpResult struct {
	Name       string `json:"name"`
	Status     string `json:"status"`
	ConfigHash string `json:"config_hash"`
	// Output 是 compose 命令的输出（截断后），便于在界面上展示"为什么失败"。
	Output string `json:"output"`
}
