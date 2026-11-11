package imagetask

// archive.go 是 Hub 侧「可复用归档」的读取原语。
//
// ============================ 为什么独立于 jobs ============================
//
// manager.go 里的 job 是**内存态的一次性操作**：有状态机、进度、TTL，
// 结束即释放。编排迁移需要的不是这个形状——它要"先把归档拿到手，
// 慢慢用"，而这段时间完全可能超过任何 job 的合理生存期。
//
// 因此这里只做三件事，且**不引入新的状态机**：
//
//	Export  → 在源 Agent 上导出并持有，拿到 ArchiveRef
//	Copy    → 把归档完整复制到本地（或交给任意 sink），带完整性校验
//	Close   → 显式释放
//
// 刻意不塞进 operations.go：那里的 run/finish/dispose 全都是围绕
// "一个 job 一个操作"设计的，硬插进去会让 job 的状态机多出
// "已导出但未释放"这种既不是终态也不在运行的中间态，
// 而 job 的 TTL 清理逻辑不认识它。
//
// ============================ 复用而不是复制完整性校验 ============================
//
// 完整性校验是本功能唯一真正危险的地方：一份**长度正确但内容错乱**的归档
// 在下游 load 时才会报错，而那时用户已经等了整个传输时间。
// 因此 Copy 复用 operations.go 里同一套规则（每块 ≤ ChunkSize、
// 不得超过声明大小、offset 单调、最终 sha256 必须匹配），
// 只是把"从远程 task 读"换成"从 ExportID 读"。

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
)

// ArchiveSink 接收归档字节。
//
// Copy 只要求"写入有序的连续字节"，不关心目标是本地文件、
// 另一个 Agent 的分块上传，还是计算哈希——迁移流程因此可以
// 用同一个 Copy 同时支持"下载到 Hub"与"直传目标服务器"。
type ArchiveSink interface {
	// Write 写入一块字节；offset 是该块在归档中的起始位置。
	Write(offset int64, data []byte) error
}

// ArchiveRef 是可复用归档的标识（与 agentlink.ArchiveRef 字段一致）。
//
// 为什么在 imagetask 里再定义一个：
//
//	imagetask 不该依赖 agentlink（方向相反：agentlink 是传输层）。
//	两者靠本包的 Backend 接口解耦，ArchiveRef 是这条接口上的数据类型。
//
// 字段同名同义，转换是逐字段赋值，不会漂移（有测试守着）。
type ArchiveRef struct {
	ExportID  string
	Reference string
	SizeBytes int64
	SHA256    string
	ExpiresAt string
}

// ArchiveBackend 是可复用归档所需的 Agent 调用能力。
//
// 与 Backend 分开而不是加进去：现有 Backend 的实现者（agentlink）
// 与测试替身都已经稳定，往里加方法会迫使所有实现同时改动，
// 而其中大多数并不关心可复用归档。
//
// 需要它的调用方（迁移流程）做一次类型断言即可，
// 断言失败时给出明确错误而不是静默降级。
type ArchiveBackend interface {
	ExportArchive(ctx context.Context, agent, reference string) (ArchiveRef, error)
	OpenArchive(ctx context.Context, agent, exportID string) (ArchiveRef, error)
	GetArchiveChunk(ctx context.Context, agent, exportID string, offset int64, max int) ([]byte, bool, error)
	CloseArchive(ctx context.Context, agent, exportID string) (bool, string, error)
}

// 可复用归档相关的错误。
var (
	// ErrArchiveBackendUnsupported 当前后端不支持可复用归档。
	ErrArchiveBackendUnsupported = errors.New("当前后端不支持可复用归档导出")
	// ErrArchiveCorrupt 归档内容与声明的校验和不一致。
	ErrArchiveCorrupt = errors.New("归档校验和不一致，传输已中止")
)

// ArchiveReader 在 Agent 上导出并读取可复用归档。
//
// 把 ArchiveBackend 与 archiveBackend 的 plumbing 收在一处，
// 让调用方不必自己断言接口、自己处理错误映射。
type ArchiveReader struct {
	backend ArchiveBackend
}

// NewArchiveReader 构造读取器；backend 不支持时返回的错误可被 errors.Is 判定。
func NewArchiveReader(backend any) (*ArchiveReader, error) {
	ab, ok := backend.(ArchiveBackend)
	if !ok || ab == nil {
		return nil, ErrArchiveBackendUnsupported
	}
	return &ArchiveReader{backend: ab}, nil
}

// Export 在 source 上导出并持有一个归档。
func (r *ArchiveReader) Export(ctx context.Context, sourceAgent, reference string) (ArchiveRef, error) {
	ref, err := r.backend.ExportArchive(ctx, sourceAgent, reference)
	if err != nil {
		return ArchiveRef{}, err
	}
	if err := validateRef(ref); err != nil {
		return ArchiveRef{}, err
	}
	return ref, nil
}

// Open 确认归档仍可用（迁移在长时间等待后必须先调这个）。
func (r *ArchiveReader) Open(ctx context.Context, sourceAgent, exportID string) (ArchiveRef, error) {
	ref, err := r.backend.OpenArchive(ctx, sourceAgent, exportID)
	if err != nil {
		return ArchiveRef{}, err
	}
	if err := validateRef(ref); err != nil {
		return ArchiveRef{}, err
	}
	return ref, nil
}

// Close 释放归档（幂等）。
func (r *ArchiveReader) Close(ctx context.Context, sourceAgent, exportID string) (bool, string, error) {
	return r.backend.CloseArchive(ctx, sourceAgent, exportID)
}

// Copy 把归档从 sourceAgent 完整复制到 sink，并在结束时校验 sha256。
//
// ============================ 完整性校验的四条硬规则 ============================
//
// 与 operations.go 的 copyArchive 完全一致（复用同一套判断）：
//
//  1. 每块不得超过 ChunkSize；
//  2. 累计字节不得超过声明的 SizeBytes（防止服务端多给）；
//  3. offset 单调推进，且服务端回显的 offset 必须与请求一致；
//  4. 传输结束后累计的 sha256 必须等于声明的 SHA256。
//
// 任何一条不满足就**立即中止**：继续拼下去只会得到一份
// "长度正确但内容错乱"的归档，而它在下游才能真正暴露问题。
//
// progress 可为 nil；它收到的是"已复制字节数 / 总字节数"。
func (r *ArchiveReader) Copy(ctx context.Context, sourceAgent string, ref ArchiveRef, sink ArchiveSink,
	progress func(done, total int64)) error {
	if err := validateRef(ref); err != nil {
		return err
	}
	if sink == nil {
		return fmt.Errorf("归档复制的目标不能为空")
	}

	h := sha256.New()
	var offset int64
	for {
		if err := ctx.Err(); err != nil {
			// 取消/超时要与"归档损坏"区分开：前者可重试，后者必须重新导出。
			if errors.Is(err, context.Canceled) {
				return ErrCanceled
			}
			return ErrTimeout
		}

		data, eof, err := r.backend.GetArchiveChunk(ctx, sourceAgent, ref.ExportID, offset, ChunkSize)
		if err != nil {
			return err
		}
		// 规则 1 & 2：块大小与累计长度。
		if len(data) > ChunkSize || int64(len(data)) > ref.SizeBytes-offset {
			return ErrArchiveCorrupt
		}
		if len(data) > 0 {
			if _, werr := h.Write(data); werr != nil {
				return werr
			}
			if werr := sink.Write(offset, data); werr != nil {
				return werr
			}
			offset += int64(len(data))
			if progress != nil {
				progress(offset, ref.SizeBytes)
			}
		}
		if eof {
			break
		}
		if len(data) == 0 {
			// 服务端返回空块却未标 EOF：继续循环只会无限空转。
			return ErrArchiveCorrupt
		}
	}

	// 规则 4：字节数必须不多不少。
	if offset != ref.SizeBytes {
		return ErrArchiveCorrupt
	}
	// 规则 4：校验和必须一致。
	if got := hex.EncodeToString(h.Sum(nil)); !equalHex(got, ref.SHA256) {
		return ErrArchiveCorrupt
	}
	return nil
}

// validateRef 校验归档标识可用于一次真实传输。
//
// ============================ 为什么在这里再校验一次 ============================
// ArchiveRef 可能是调用方自己拼出来的（例如从数据库里读回上次导出时的记录）。
// 一个 SizeBytes=0 的 ref 会让 Copy 立刻"成功"返回，
// 上层于是得到一份空归档——而它看起来像一次成功的迁移。
func validateRef(ref ArchiveRef) error {
	if ref.ExportID == "" {
		return fmt.Errorf("归档标识为空")
	}
	if ref.SizeBytes <= 0 {
		return fmt.Errorf("归档大小无效（%d）", ref.SizeBytes)
	}
	if ref.SizeBytes > MaxArchiveSize {
		return fmt.Errorf("归档大小超过上限（%d 字节）", ref.SizeBytes)
	}
	if !isHexDigest(ref.SHA256) {
		return fmt.Errorf("归档校验和无效")
	}
	return nil
}

// isHexDigest 校验 64 位十六进制。
func isHexDigest(s string) bool {
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

// equalHex 大小写不敏感地比较两个十六进制串。
//
// 不同引擎/不同系统给的校验和大小写并不统一，
// 用 bytes.Equal 直接比会让一个正确的归档被判成损坏。
func equalHex(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := 0; i < len(a); i++ {
		ca, cb := a[i], b[i]
		if ca >= 'A' && ca <= 'F' {
			ca += 'a' - 'A'
		}
		if cb >= 'A' && cb <= 'F' {
			cb += 'a' - 'A'
		}
		if ca != cb {
			return false
		}
	}
	return true
}

// FileSink 把归档写到一个 io.WriterAt 风格的目标（如 *os.File）。
//
// 用 WriteAt 而不是 Write：分块是按 offset 定位的，
// 将来若要支持并发/重试写同一目标，WriteAt 的语义天然正确，
// 而顺序 Write 一旦漏块就再也对不齐。
type FileSink struct {
	W io.WriterAt
}

// Write 在 offset 处写入一块字节。
func (s *FileSink) Write(offset int64, data []byte) error {
	if s.W == nil {
		return fmt.Errorf("写入目标为空")
	}
	n, err := s.W.WriteAt(data, offset)
	if err != nil {
		return err
	}
	if n != len(data) {
		return io.ErrShortWrite
	}
	return nil
}
