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
	// EnvContent 是该项目同名 .env 的完整文本。
	//
	// 空串有两种含义，必须靠 ContentHash 区分，因此这里只表达"文本为空"：
	//   - 旧版 Hub 不发这个字段 → 反序列化为空串 → 继续按旧方式工作（只有 compose.yml）；
	//   - 新版 Hub 明确发空串 → 表示"清空该项目的 .env"（删除文件），
	//     而不是"保留上一次的 .env"。保留会留下用户以为已经删掉的密钥。
	//
	// 之所以把 .env 放在消息里而不是让 Agent 去某处读：
	// Agent 运行在容器里，**读不到宿主机上的编排目录**，
	// 任何"Agent 自己去读宿主 .env"的设计在真实部署里都不成立。
	EnvContent string `json:"env_content"`
	// ContentHash 是 Hub 侧计算的**项目哈希**（compose.yml 与 .env 的联合哈希）。
	//
	// 与旧版的兼容性靠算法保证：当 EnvContent 为空时，
	// 联合哈希就等于旧版的 sha256(content) 前 16 位，
	// 所以旧 Hub（只发 content）与新版 Agent 仍然算得出同一个值。
	ContentHash string `json:"content_hash"`
}

// ComposePreviewPayload 是"能否导入某个外部编排"的预检请求。
//
// **必须带 Content / EnvContent。** 只给 name 的预检没有意义：
// Agent 在容器里运行，**读不到宿主机上的编排文件**，
// 没有内容就只能回答 supported=false，前端将永远无法点"导入"。
// 用户在前端粘贴 compose 与 .env，随请求一起下发，Agent 才能做真实的安全校验。
//
// 内容为空时仍可预检，但结论会是 supported=false 并给出"需要用户提供…"的原因；
// 这不是失败，而是如实回答"材料不齐"。
type ComposePreviewPayload struct {
	Name string `json:"name"`
	// Content 是用户提供的 compose.yml 文本（可空）。
	Content string `json:"content"`
	// EnvContent 是用户提供的 .env 文本（可空；空表示该项目按无 .env 处理）。
	EnvContent string `json:"env_content"`
}

// ComposePreviewResult 是预检结果。
//
// 字段分三类，前端据此决定按钮状态：
//   - 事实：discovered / engine_exists / workspace_exists / source / running / total
//   - 结论：can_import（能否导入）
//   - 原因：blockers（必须解决）、warnings（提示，不阻塞）
//
// 区分"阻塞"与"提示"很重要：把阻塞写成提示会让用户点下按钮后才失败；
// 把提示写成阻塞会让本来可行的导入被无谓挡住。
type ComposePreviewResult struct {
	Name string `json:"name"`
	// Discovered 表示引擎里存在该名字的外部（非 Arkade）编排项目。
	Discovered bool `json:"discovered"`
	// EngineExists / WorkspaceExists 分别对应引擎与工作区，便于前端解释结论。
	EngineExists    bool `json:"engine_exists"`
	WorkspaceExists bool `json:"workspace_exists"`
	// Source 描述这个名字目前来自哪里：engine / workspace / both / none。
	Source string `json:"source"`
	// Running / Total 是该外部项目的服务运行数/总数。
	Running int `json:"running"`
	Total   int `json:"total"`
	// CanImport 是**唯一**决定"导入"按钮是否可点的字段。
	//
	// 它等价于：发现外部同名项目 且 工作区无冲突 且 内容齐备 且 无安全阻塞项。
	// 前端不应自己组合这些条件——组合逻辑一旦与 Agent 不一致，
	// 就会出现"按钮可点但请求必失败"或"明明能导入却点不了"。
	CanImport bool `json:"can_import"`
	// ConfigHash 是 Agent 用 HashProject(Content, EnvContent) 算出的项目哈希。
	// 前端把它作为 expected_content_hash 回传，避免"预览与实际导入内容不一致"。
	ConfigHash string `json:"config_hash"`
	// Blockers 是**必须解决**才能导入的原因。
	Blockers []string `json:"blockers"`
	// Warnings 是不阻塞导入的提示。
	Warnings []string `json:"warnings"`
}

// ComposeAdoptPayload 是"把已发现的外部编排显式纳管"的请求。
//
// 这是一个**只落盘、不启动**的操作：绝不执行 up/down/pull/stop。
// 原因：外部项目正在跑着生产负载，导入的正确动作是"先把配置存下来并表明归属"，
// 任何"顺手 up 一下让它符合新配置"都会重建容器，那是用户没有要求的破坏。
type ComposeAdoptPayload struct {
	Name string `json:"name"`
	// Content / EnvContent 必须由用户提供（Hub 前端粘贴）。
	// Agent 读不到宿主机文件，也不能从运行中的容器反推出可信的原始配置。
	Content    string `json:"content"`
	EnvContent string `json:"env_content"`
	// ExpectedContentHash 是 Hub 用同一算法算出的项目哈希，落盘前校验。
	ExpectedContentHash string `json:"expected_content_hash"`
	// Confirmation 必须为 true。
	//
	// 这是"用户明确知道自己在纳管一个已在运行的外部项目"的显式确认。
	// 用布尔而不是省略即默认，是为了让"误调接口"不可能完成导入。
	Confirmation bool `json:"confirmation"`
}

// ComposeAdoptResult 是导入结果。
type ComposeAdoptResult struct {
	Name       string `json:"name"`
	Status     string `json:"status"`
	ConfigHash string `json:"config_hash"`
	Output     string `json:"output"`
	// Adopted 表示已落盘并纳管（工作区存在该编排）。
	Adopted bool `json:"adopted"`
	// EnvWritten 表示本次写入了 .env（false 且 EnvContent 为空时表示已清除）。
	EnvWritten bool `json:"env_written"`
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
