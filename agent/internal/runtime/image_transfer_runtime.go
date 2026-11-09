package runtime

// image_transfer_runtime.go 定义镜像传输的**可选**能力接口。
//
// ============================ 为什么是独立接口而不是塞进 Runtime ============================
//
// `Runtime` 是被 handler 全量依赖的主接口，任何新增方法都会让**所有**实现
// （包括测试里的假 runtime）被迫补一个方法。镜像传输只有真实引擎实现，
// 那些假实现既不关心也用不到——强行要求它们实现，结果是到处出现
// 返回 `errors.New("not implemented")` 的桩函数，把接口变成一份"假装完整"的清单。
//
// 因此做成可选接口：handler 用类型断言取能力，失败就是 unsupported。
// 这样：
//   - 现有 mock 一行都不用改（这正是 Lead 要求的"避免所有 mock 爆改"）；
//   - 能力边界由类型系统表达，而不是靠文档；
//   - 将来加别的引擎（例如只实现列表的远程代理）也不会被绑架。
//
// 取能力的标准写法（见 handler/image_transfer.go）：
//
//	if tr, ok := rt.(runtime.ImageTransferRuntime); ok { ... }
//
// ============================ 三个动作的共同约定 ============================
//
//  1. **全部流式**：Save 返回 io.ReadCloser 由调用方边读边消费；
//     Load 从 io.Reader 边读边发。任何一方都不把归档读进内存。
//     这是硬要求：归档是几百 MB 到几 GB，缓冲一次就足以打死 Agent。
//
//  2. **全部响应 ctx 取消**：调用方取消时底层 HTTP 请求必须立即中断。
//     注意取消 Save/Load 的中途状态**不可回滚**（见各方法说明）。
//
//  3. **进度回调必须廉价**：onProgress 可能在下载的每个 chunk 上被调用，
//     实现侧只做"记录数字"，不做 IO、不取锁。调用方负责限频。
//
//  4. **错误文案不转发引擎原文**：由 handler 层统一造句，
//     这里返回的错误只供内部判断与日志。

import (
	"context"
	"io"
)

// PullProgress 是一次拉取进度快照。
//
// 所有字段都是"当前知道多少"：引擎不总是同时给出 current/total
// （分层拉取时每层各有一组数字），实现侧需要把它们聚合成一个总量。
type PullProgress struct {
	// BytesDone 已完成字节数（可能是估算，见 Exact）。
	BytesDone int64
	// BytesTotal 总字节数；0 表示引擎没有给出（**不能当作确实为 0**）。
	BytesTotal int64
	// Status 是语义化阶段，形如 pulling / downloading / extracting / done。
	Status string
}

// ImageTransferRuntime 是镜像 拉取/导出/导入 的可选能力。
type ImageTransferRuntime interface {
	// PullImage 从公共 registry 拉取镜像。
	//
	// reference 必须是已校验的合法镜像引用；platform/digest 可空。
	// onProgress 可为 nil。返回的 digest 是拉取结果的实际 digest（若引擎给出）。
	//
	// **不接收任何凭据参数**：首批只支持公共 registry。
	// 私有仓库凭据属于 Agent 本地配置（第二阶段），
	// 让它经过 Hub 会同时出现在请求日志、审计与浏览器网络面板里。
	PullImage(ctx context.Context, reference, platform, digest string, onProgress func(PullProgress)) (PullResult, error)

	// SaveImage 导出镜像，返回**流式**的 tar 读取端。
	//
	// 调用方**必须**关闭返回的 ReadCloser。
	// 实现在返回前已完成"镜像是否存在"的确认，因此 404 会在这里以 error 返回，
	// 而不是让调用方在读流时才发现。
	SaveImage(ctx context.Context, reference string) (io.ReadCloser, error)

	// LoadImage 从流中导入镜像。
	//
	// size 是声明的归档字节数（>0），用于设置 Content-Length：
	// 明确长度比 chunked 编码更稳（部分引擎版本对 load 的 chunked 支持不一致）。
	// 返回引擎报告的已加载镜像列表。
	//
	// **中途取消不可回滚**：镜像层一旦入库没有事务。取消后可能已经
	// 写入了部分镜像，这一点必须如实告诉用户，不能宣称"已回滚"。
	LoadImage(ctx context.Context, body io.Reader, size int64) ([]Image, error)

	// InspectImage 返回单个镜像的详情（大小、digest、标签）。
	//
	// 用途：save 之前预估归档大小（做磁盘配额预检），以及 load 之后
	// 确认镜像是否真的进来了。不存在时返回 ErrNotFound。
	InspectImage(ctx context.Context, reference string) (ImageDetail, error)
}

// PullResult 是一次拉取的最终结果。
type PullResult struct {
	// Reference 是实际使用的完整引用（可能被补全了 library/ 前缀与 :latest）。
	Reference string
	// Digest 是引擎报告的实际 digest，可空。
	Digest string
}

// ImageDetail 是镜像详情。
type ImageDetail struct {
	ID       string
	Tags     []string
	Size     int64 // 未压缩大小（引擎口径），用于配额预估
	Created  int64
	Platform string
	// Digests 是 repo digests（形如 repo@sha256:...），用于校验。
	Digests []string
}
