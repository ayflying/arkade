package runtime

import "strings"

// hostpath.go：宿主机绝对路径 → 本进程容器内可见路径的换算。
//
// ============================ 为什么需要这一层 ============================
//
// Agent 可以用两种方式部署：
//   1. 原生二进制跑在宿主机上 —— 容器标签里的宿主路径直接就能读；
//   2. 容器化部署（本项目的默认镜像）—— Agent 看到的是自己的文件系统，
//      宿主机的 /www/server/panel/... 在容器里根本不存在。
//
// 第 2 种形态下，若不换算就会把"路径不存在"误报成"读取失败"，
// 用户无论怎么授权都读不到，而真正的原因（路径不通）从未出现过。
//
// 换算只做一件事：把前缀 HostRoot 换成 HostMount。
// 其余情况（未配置、路径含 ..、非绝对路径、不在 HostRoot 之下）
// 一律**拒绝换算**并返回 ok=false，由调用方按"读不到"如实报告——
// 绝不"找一份相近的"当作替代，那会把别的项目的配置当成这个项目的。

// DefaultHostMount 是宿主机目录在 Agent 容器内的默认挂载点。
//
// 选择固定路径而不是沿用宿主机路径：沿用会让挂载点与容器自身文件系统
// 重名（例如挂 /opt 会遮住 Agent 自己的程序目录），排障时极易看错。
const DefaultHostMount = "/arkade/external"

// hostPath 把宿主绝对路径换算成容器内路径。
//
// 返回 ok=false 表示"这条路径在本进程里不可用"，调用方必须当成读不到，
// 不要退化成猜测路径再试。
func (e *engine) hostPath(p string) (string, bool) {
	if e == nil || e.hostRoot == "" {
		// 未配置=原生部署：宿主路径就是本机路径。
		return p, true
	}
	mount := e.hostMount
	if mount == "" {
		mount = DefaultHostMount
	}
	root := strings.TrimSuffix(e.hostRoot, "/")
	if !strings.HasPrefix(p, root+"/") {
		return "", false
	}
	rest := p[len(root):]
	if hasDotDotSegment(rest) {
		// 带 .. 的路径意味着它可能逃出被授权挂载的那一个目录，
		// 换成容器内路径后就没有隔离意义了，必须拒绝。
		return "", false
	}
	return mount + rest, true
}

// hasDotDotSegment 判断路径中是否出现 ".." 这样的目录层级。
func hasDotDotSegment(p string) bool {
	for _, seg := range strings.Split(p, "/") {
		if seg == ".." {
			return true
		}
	}
	return false
}
