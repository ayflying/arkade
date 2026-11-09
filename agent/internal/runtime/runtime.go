// Package runtime 是 Agent 的「运行时抽象层」。
//
// 设计目标（决策 D-01）：首期只支持 Docker 与 Podman，两者都对外暴露 Docker 兼容的
// HTTP API，因此差异被收敛到三件事上——套接字位置、版本探测、引擎类型判定。
// 上层的处理器（internal/handler）只依赖这里的 Runtime 接口，不感知具体引擎，
// 新增引擎时只需补一个实现，方法名与载荷格式完全不变。
//
// 禁止引入 docker/docker SDK：本项目只用很少的几个端点，SDK 会带进一大串依赖，
// 并且把错误模型、客户端复用方式都绑死在别人的实现上。这里用标准库手写一个
// 足够小的 HTTP 客户端（client.go），端点仅作为常量集中在 api.go 一处维护。
package runtime

import "context"

// Container 是运行时侧的容器视图。
//
// 字段刻意收敛到「Arkade 自身需要」的范围：不搬运运行时的全量 JSON 结构，
// 避免引擎版本升级带来的字段漂移污染上层。协议侧的 ContainerSummary 由
// internal/handler 负责转换，两者不直接耦合。
type Container struct {
	ID          string            // 完整 ID；上层展示时自行截短
	Name        string            // 容器名（已去掉运行时添加的前导斜杠）
	Image       string            // 镜像引用
	State       string            // running / exited / paused / created / restarting / dead
	Status      string            // 人类可读状态文本
	Ports       []Port            // 端口映射
	Labels      map[string]string // 容器标签（识别 Arkade 归属需要）
	CreatedUnix int64             // 创建时间 Unix 秒
}

// Port 是一条端口映射。
type Port struct {
	IP      string // 绑定地址，可能为空
	Private int    // 容器内端口
	Public  int    // 宿主机端口，未映射时为 0
	Type    string // tcp / udp
}

// LogOptions 控制日志拉取。
//
// TailLines 必须有上限：日志是无界数据，全量拉取既会撑爆 WebSocket 队列，
// 也会让 CLI 卡死。上限由调用方（handler）统一裁剪，运行时不假设更大。
type LogOptions struct {
	TailLines  int  // 只取末尾 N 行
	Stdout     bool // 是否包含 stdout
	Stderr     bool // 是否包含 stderr
	Timestamps bool // 是否带时间戳前缀
}

// Runtime 是运行时抽象层的核心接口。
//
// 所有方法都以 context 控制取消：Hub 侧超时后必须能让底层请求真正中断，
// 否则僵尸请求会拖住整个 Agent。空实现（RuntimeNone）返回 ErrNone，
// 上层据此回 unsupported 错误，而不是空指针崩溃。
type Runtime interface {
	// Info 返回运行时类型、版本与端点信息，用于 hello 上报与诊断。
	Info(ctx context.Context) (Info, error)
	// List 列出容器；all=false 时只列运行中的容器。
	List(ctx context.Context, all bool) ([]Container, error)
	// Start 启动容器。
	Start(ctx context.Context, idOrName string) error
	// Stop 停止容器；force=true 时跳过优雅退出（SIGKILL）。
	Stop(ctx context.Context, idOrName string, force bool) error
	// Remove 删除容器；force=true 时即使运行中也强制删除。
	Remove(ctx context.Context, idOrName string, force bool) error
	// Logs 拉取日志，返回已按行拆分的文本（不含结尾空行噪声）。
	Logs(ctx context.Context, idOrName string, opts LogOptions) (LogResult, error)
}

// LogResult 是 LimitLines 之后的日志结果。
type LogResult struct {
	Lines     []string
	Truncated bool // 是否因行数上限被截断
}

// Info 描述探测到的运行时。
type Info struct {
	Kind     Kind   // docker / podman / none
	Version  string // 引擎版本，如 27.3.1
	APIVer   string // HTTP API 版本
	Endpoint string // 脱敏后的端点描述（unix 路径或 tcp 主机）
	Rootless bool   // Podman 无 root 模式；Docker 恒为 false
}
