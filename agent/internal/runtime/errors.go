package runtime

import "errors"

// ErrNone 表示本机没有可用的运行时。
//
// 语义边界：这是「环境事实」而不是「程序错误」。Agent 在无运行时的机器上
// 依然要能连上 Hub 并如实上报 RuntimeNone（便于运维看到"这台机器没装引擎"），
// 而不是启动失败或崩溃。
var ErrNone = errors.New("未探测到可用的容器运行时（Docker / Podman）")

// ErrNotFound 表示容器不存在。
//
// 由各实现把运行时的 404 统一翻译成本错误，上层据此回 not_found，
// 不必关心底层是 Docker 还是 Podman 的哪种 404 文案。
var ErrNotFound = errors.New("容器不存在")

// ErrUnsupported 表示运行时明确拒绝该操作（如 rootless Podman 绑定特权端口）。
//
// 与 ErrNone 的区别：ErrNone 是"没有引擎"，ErrUnsupported 是"引擎在但做不到"，
// 两者在界面上的提示文案完全不同，不能合并。
var ErrUnsupported = errors.New("运行时不支持该操作")

// Kind 是运行时类型。取值与协议层 envelope.RuntimeKind 一一对应，
// 但这里保留独立类型：运行时层不应该为了一个字符串去依赖协议包。
type Kind string

const (
	KindDocker Kind = "docker"
	KindPodman Kind = "podman"
	KindNone   Kind = "none" // 未探测到可用运行时
)
