package runtime

// resources.go 定义镜像、网络、存储卷在运行时抽象层的类型。
//
// 与 Container 同样的原则：只声明 Arkade 用得到的字段，不搬运运行时的全量结构。
// 协议侧的 ImageSummary / NetworkSummary / VolumeSummary 由 handler 层转换，
// 两层不直接耦合——这样运行时字段变化的影响被限制在本包内。

// Image 是运行时的镜像视图。
type Image struct {
	ID       string   // 完整 ID（sha256:xxx）；展示时由上层截短
	Tags     []string // 可能为空（悬空镜像）
	Size     int64    // 字节数
	Created  int64    // Unix 秒
	Dangling bool     // 悬空镜像
}

// Network 是网络视图。
type Network struct {
	ID         string
	Name       string
	Driver     string
	Scope      string
	Internal   bool
	Containers int               // 接入容器数
	Labels     map[string]string // Compose 会打 project 标签
	Created    int64
}

// Volume 是存储卷视图。
type Volume struct {
	Name       string
	Driver     string
	Mountpoint string
	Labels     map[string]string
	Created    int64
	// InUse 表示是否有容器正在使用。列表接口一次性算完，
	// 避免为每个卷单独发一次请求（N+1）。
	InUse bool
}
