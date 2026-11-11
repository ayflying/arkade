package agentlink

// ops_image_archive.go 是 Hub 侧「可复用归档」的后端调用。
//
// ============================ 与 ops_image_transfer.go 的分工 ============================
//
// ops_image_transfer.go 里的 StartSave/GetChunk/Release 服务于
// **任务内**归档：导出、拉走、释放都在同一个 job 里完成。
//
// 本文件服务于**跨阶段**归档：先导出并持有（ExportID），
// 过一段时间再回头读，最后由调用方显式关闭。
// 两者刻意用不同的协议方法，因为它们的生命周期契约不同：
//   - 任务内归档随 job 终态被释放；
//   - 可复用归档只在 close 或 TTL 过期时消失。
//
// 混用同一套方法会让"谁负责释放"变得无法推理——
// job 结束时若顺手 release 了可复用的 ExportID，
// 迁移就会在回头拉取时 404，而失败现场看起来像调用方写错了 id。

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"

	"arkade/protocol/envelope"
)

// 分块与大小上限：与协议、Agent 侧保持同一组数字。
//
// 为什么不从 imagetask 包引用：agentlink 不依赖 imagetask
// （那是 job 管理，方向相反）。这里只是把协议的硬边界写清楚，
// 任何一侧改动都会被 "Hub 请求超上限 → Agent 截断" 的测试抓住。
const (
	// ChunkSizeHardLimit 与 Agent 侧 ChunkSize 一致（256 KiB）。
	ChunkSizeHardLimit = 256 << 10
	// MaxArchiveSizeHard 与 Agent 侧 DefaultMaxArchiveBytes 一致（2 GiB）。
	MaxArchiveSizeHard int64 = 2 << 30
)

// 可复用归档的错误语义（与 Agent 侧 handler 的映射一致）。
//
// ============================ 为什么在这里再分一层 ============================
//
// 调用方需要区分"归档没了，重新导出"与"归档被关了，是流程错误"。
// 若不区分，上层只能一律重新导出——每次重试都白跑几分钟并写 2 GiB。
//
// 因此这里把"归档不可用"抽成哨兵错误，调用方用 errors.Is 判断即可，
// 不必解析错误文本（文本是给人看的，错误码才是稳定契约）。
var (
	// ErrArchiveGone 归档已不存在或已过期——正确处理是**重新导出**。
	ErrArchiveGone = errors.New("镜像归档已不存在或已过期")
	// ErrArchiveClosed 归档已被显式关闭——这是流程错误，重试无意义。
	ErrArchiveClosed = errors.New("镜像归档已被关闭")
)

// mapArchiveError 把 Agent 的协议错误码翻成可判定的哨兵错误。
//
// Agent 侧的映射（见 agent/internal/handler/image_archive.go）：
//
//	not_found   → 不存在 / 已过期 / 文件被外部删除
//	conflict    → 已关闭 / 数量达上限 / 内容被外部修改
//	bad_request → ExportID 形态非法（客户端 bug）
//
// 只有 not_found 与"已关闭"在这里被单独抽出来，其余保留原 *ConnError：
// 其余情况调用方不需要特殊分支，用连接层既有错误处理即可。
func mapArchiveError(err error) error {
	if err == nil {
		return nil
	}
	var ce *ConnError
	if !errors.As(err, &ce) {
		return err
	}
	switch ce.Code {
	case envelope.CodeNotFound:
		return fmt.Errorf("%w: %s", ErrArchiveGone, ce.Message)
	case envelope.CodeConflict:
		// conflict 既可能是"已关闭"也可能是"内容被外部修改"。
		// 两者对调用方都是"这份归档不能用了，且重试无用"，
		// 因此都归到 ErrArchiveClosed 的语义里，但保留原文案供展示。
		return fmt.Errorf("%w: %s", ErrArchiveClosed, ce.Message)
	}
	return err
}

// ArchiveRef 是一份可复用归档的完整标识。
//
// 三个字段缺一不可：
//   - ExportID 用于后续读取；
//   - SizeBytes 是分块循环的终止条件与"收到的不多不少"的判据；
//   - SHA256 是传输结束后唯一能证明"拉到的就是导出的那份"的凭证。
type ArchiveRef struct {
	ExportID  string
	Reference string
	SizeBytes int64
	SHA256    string
	// ExpiresAt 是 Agent 侧的过期时间（RFC3339），供调用方判断是否来得及。
	ExpiresAt string
}

// ExportArchive 请求 Agent 导出并**持有**一个镜像归档。
//
// hold 恒为 true：本原语存在的目的就是"导出后不自动释放"。
// 保留协议字段是为了将来可能有"临时导出"的用法，
// 但这里不提供开关——一个没有调用方需要的开关比没有开关更容易被误用。
func (b *ImageBackend) ExportArchive(ctx context.Context, agent, reference string) (ArchiveRef, error) {
	var out envelope.ImageArchiveExportResult
	err := b.call(ctx, agent, envelope.MethodImageArchiveExport,
		envelope.ImageArchiveExportPayload{Reference: reference, Hold: true}, &out)
	if err != nil {
		return ArchiveRef{}, mapArchiveError(err)
	}
	ref := ArchiveRef{
		ExportID: out.ExportID, Reference: out.Reference,
		SizeBytes: out.SizeBytes, SHA256: out.SHA256, ExpiresAt: out.ExpiresAt,
	}
	// 校验 Agent 回的标识：一个不可用的 ArchiveRef 若被交给后续步骤，
	// 会在复制到一半时才失败，那时已经花掉了大部分时间。
	if err := validateArchiveRef(ref); err != nil {
		return ArchiveRef{}, err
	}
	return ref, nil
}

// OpenArchive 确认一份归档仍然可用（不读取字节）。
//
// 迁移流程在长时间等待后必须调用它：ExportID 可能已过期，
// 而"直接开始拉取再失败"会浪费一次完整传输的等待。
func (b *ImageBackend) OpenArchive(ctx context.Context, agent, exportID string) (ArchiveRef, error) {
	var out envelope.ImageArchiveOpenResult
	err := b.call(ctx, agent, envelope.MethodImageArchiveOpen,
		envelope.ImageArchiveOpenPayload{ExportID: exportID}, &out)
	if err != nil {
		return ArchiveRef{}, mapArchiveError(err)
	}
	ref := ArchiveRef{
		ExportID: out.ExportID, Reference: out.Reference,
		SizeBytes: out.SizeBytes, SHA256: out.SHA256, ExpiresAt: out.ExpiresAt,
	}
	if err := validateArchiveRef(ref); err != nil {
		return ArchiveRef{}, err
	}
	return ref, nil
}

// GetArchiveChunk 读取可复用归档的一块字节。
//
// 返回的 offset 必须与请求的一致：这是分块实现的幂等契约，
// 不一致说明服务端错位，继续拼下去只会得到一份"长度正确但内容错乱"的归档，
// 而它在传输结束前都不会暴露问题。
func (b *ImageBackend) GetArchiveChunk(ctx context.Context, agent, exportID string, offset int64, max int) ([]byte, bool, error) {
	if offset < 0 {
		return nil, false, fmt.Errorf("分块偏移不能为负")
	}
	if max <= 0 || max > ChunkSizeHardLimit {
		max = ChunkSizeHardLimit
	}
	var out envelope.ImageChunkGetResult
	err := b.call(ctx, agent, envelope.MethodImageArchiveChunkGet,
		envelope.ImageArchiveChunkGetPayload{ExportID: exportID, Offset: offset, MaxBytes: int64(max)}, &out)
	if err != nil {
		return nil, false, mapArchiveError(err)
	}
	if out.Offset != offset {
		return nil, false, fmt.Errorf("服务器返回的分块偏移不一致（请求 %d，返回 %d）", offset, out.Offset)
	}
	// 与 PutChunk 同一套边界检查：base64 长度先粗筛，解码后再精确判上限。
	if len(out.DataBase64) > ((ChunkSizeHardLimit)+2)/3*4 {
		return nil, false, fmt.Errorf("服务器返回的分块越界")
	}
	data, derr := base64.StdEncoding.DecodeString(out.DataBase64)
	if derr != nil {
		return nil, false, fmt.Errorf("服务器返回的分块不是合法 base64")
	}
	if len(data) > ChunkSizeHardLimit {
		return nil, false, fmt.Errorf("服务器返回的分块超过上限")
	}
	return data, out.EOF, nil
}

// CloseArchive 释放一份可复用归档。
//
// released=false 且无错误表示 Agent 侧本来就没有这份归档
// （已过期或被别的调用方关掉）。这里**不返回错误**：
// 调用方要的结果（不再占盘）已经达成，而把它变成错误只会
// 逼上层写"吞掉 not_found"的代码，反而掩盖真正的问题。
func (b *ImageBackend) CloseArchive(ctx context.Context, agent, exportID string) (bool, string, error) {
	var out envelope.ImageArchiveCloseResult
	err := b.call(ctx, agent, envelope.MethodImageArchiveClose,
		envelope.ImageArchiveClosePayload{ExportID: exportID}, &out)
	if err != nil {
		// 归档本就不存在时，Agent 可能选择报 not_found 而不是返回 released=false
		// （两种实现都符合协议）。这里统一收敛成"没释放任何东西"。
		if errors.Is(mapArchiveError(err), ErrArchiveGone) {
			return false, "归档已不存在（可能已释放或过期）", nil
		}
		return false, "", mapArchiveError(err)
	}
	return out.Released, out.Message, nil
}

// validateArchiveRef 校验 Agent 返回的归档标识可用。
//
// ============================ 为什么必须校验 ============================
// ArchiveRef 会被用来驱动一次**几百 MB 到 2 GiB** 的传输。
// 若 ExportID 为空或大小为 0，复制循环会立刻结束并"成功"，
// 于是上层得到一份空归档——而它看起来像一次成功的迁移。
func validateArchiveRef(r ArchiveRef) error {
	if r.ExportID == "" {
		return fmt.Errorf("Agent 未返回归档标识")
	}
	if r.SizeBytes <= 0 {
		return fmt.Errorf("Agent 返回的归档大小无效（%d），拒绝继续", r.SizeBytes)
	}
	if r.SizeBytes > MaxArchiveSizeHard {
		return fmt.Errorf("Agent 返回的归档大小超过上限（%d 字节）", r.SizeBytes)
	}
	if !isHexSHA256(r.SHA256) {
		return fmt.Errorf("Agent 返回的归档校验和无效，拒绝继续")
	}
	return nil
}

// isHexSHA256 校验 64 位十六进制。
func isHexSHA256(s string) bool {
	if len(s) != 64 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') && (c < 'A' || c > 'F') {
			return false
		}
	}
	return true
}
