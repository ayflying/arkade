package imagetask

// archive_test.go 覆盖可复用归档在 Hub 侧的读取与完整性校验。
//
// ============================ 这里最需要守住的是"损坏必须被发现" ============================
//
// 迁移场景里归档要从 A 机搬到 B 机，中间隔着几百 MB 到 2 GiB。
// 任何一处校验缺失，后果都是同一类：
//
//	传输看起来成功 → 目标端 load 时才报错 → 用户白等整个传输时间
//
// 因此这些用例的主要目的是**主动制造各种损坏**，证明每一种都能被抓住：
// 块超长、累计超出声明、空块未标 EOF、校验和不符、字节数不足。

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"sync"
	"testing"
)

// fakeArchiveBackend 是可控的可复用归档后端。
type fakeArchiveBackend struct {
	mu sync.Mutex

	exportRef ArchiveRef
	exportErr error
	openRef   ArchiveRef
	openErr   error
	chunkErr  error
	closeRel  bool
	closeMsg  string
	closeErr  error
	data      []byte
	// chunkSize 可覆盖默认块大小，用来制造"块超长"。
	chunkSize int
	// omitEOF 让最后一块不标 EOF（模拟服务端漏标）。
	omitEOF bool
	// extraByte 让累计字节超出声明大小。
	extraByte bool
	// emptyNoEOF 在末尾返回空块且不标 EOF（模拟死循环风险）。
	emptyNoEOF bool

	getCalls int
}

func (f *fakeArchiveBackend) ExportArchive(context.Context, string, string) (ArchiveRef, error) {
	if f.exportErr != nil {
		return ArchiveRef{}, f.exportErr
	}
	return f.exportRef, nil
}

func (f *fakeArchiveBackend) OpenArchive(context.Context, string, string) (ArchiveRef, error) {
	if f.openErr != nil {
		return ArchiveRef{}, f.openErr
	}
	return f.openRef, nil
}

func (f *fakeArchiveBackend) GetArchiveChunk(_ context.Context, _, _ string, offset int64, max int) ([]byte, bool, error) {
	f.mu.Lock()
	f.getCalls++
	f.mu.Unlock()
	if f.chunkErr != nil {
		return nil, false, f.chunkErr
	}
	if f.emptyNoEOF && offset >= int64(len(f.data)) {
		// 越过末尾后永远返回"空块 + 无 EOF"：这是服务端漏标 EOF 的真实形态。
		// 若返回空块的同时标了 EOF，调用方会正常退出，测不到死循环路径。
		return nil, false, nil
	}
	if offset >= int64(len(f.data)) {
		// 正常后端：越过末尾就是 EOF。
		// 但 emptyNoEOF 场景必须在**首块**就漏标 EOF，否则
		// 首块 end>=len 会直接标 true、循环当场结束，测不到目标路径。
		if f.emptyNoEOF {
			return f.data[offset:], false, nil
		}
		return nil, true, nil
	}
	size := max
	if f.chunkSize > 0 {
		size = f.chunkSize
	}
	end := offset + int64(size)
	if end > int64(len(f.data)) {
		end = int64(len(f.data))
	}
	chunk := f.data[offset:end]
	if f.extraByte && end >= int64(len(f.data)) && offset > 0 {
		// 多给一个字节：用于验证"累计超出声明大小"被拦下。
		chunk = append(append([]byte{}, chunk...), 0xFF)
	}
	eof := end >= int64(len(f.data))
	if f.omitEOF {
		eof = false
	}
	if f.emptyNoEOF {
		// 首块含全部数据但漏标 EOF；下一轮 offset 已越过末尾，
		// 由上面的分支持续返回"空块 + 无 EOF"，正好模拟死循环。
		eof = false
	}
	return chunk, eof, nil
}

func (f *fakeArchiveBackend) CloseArchive(context.Context, string, string) (bool, string, error) {
	return f.closeRel, f.closeMsg, f.closeErr
}

// memSink 把字节收在内存里。
type memSink struct {
	mu   sync.Mutex
	data []byte
}

func (s *memSink) Write(offset int64, data []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if int64(len(s.data)) < offset+int64(len(data)) {
		grown := make([]byte, offset+int64(len(data)))
		copy(grown, s.data)
		s.data = grown
	}
	copy(s.data[offset:], data)
	return nil
}

// newArchiveFixture 造一个"正确"的后端：数据、引用、校验和都自洽。
func newArchiveFixture(payload []byte) (*fakeArchiveBackend, ArchiveRef, *ArchiveReader) {
	sum := sha256.Sum256(payload)
	ref := ArchiveRef{
		ExportID:  strings.Repeat("a", 32),
		Reference: "nginx:1.25",
		SizeBytes: int64(len(payload)),
		SHA256:    hex.EncodeToString(sum[:]),
	}
	be := &fakeArchiveBackend{exportRef: ref, openRef: ref, data: payload, closeRel: true, closeMsg: "已释放"}
	r := &ArchiveReader{backend: be}
	return be, ref, r
}

// TestArchiveCopyRoundTrip 验证完整复制后字节与校验和一致。
func TestArchiveCopyRoundTrip(t *testing.T) {
	payload := []byte(strings.Repeat("migrate-me-", 30000)) // > 256 KiB
	_, ref, r := newArchiveFixture(payload)

	sink := &memSink{}
	if err := r.Copy(context.Background(), "agent-a", ref, sink, nil); err != nil {
		t.Fatalf("复制失败: %v", err)
	}
	if string(sink.data) != string(payload) {
		t.Fatalf("复制后的字节与原归档不一致（%d vs %d 字节）", len(sink.data), len(payload))
	}
	sum := sha256.Sum256(sink.data)
	if hex.EncodeToString(sum[:]) != ref.SHA256 {
		t.Fatal("复制结果的校验和与声明不一致")
	}
}

// TestArchiveCopyReportsProgress 验证进度回调单调递增并到达总量。
func TestArchiveCopyReportsProgress(t *testing.T) {
	payload := []byte(strings.Repeat("x", 700000))
	_, ref, r := newArchiveFixture(payload)

	var last int64
	calls := 0
	err := r.Copy(context.Background(), "agent-a", ref, &memSink{},
		func(done, total int64) {
			calls++
			if done <= last {
				t.Fatalf("进度应单调递增：%d → %d", last, done)
			}
			if total != ref.SizeBytes {
				t.Fatalf("总量应为 %d，实际 %d", ref.SizeBytes, total)
			}
			last = done
		})
	if err != nil {
		t.Fatalf("复制失败: %v", err)
	}
	if calls == 0 {
		t.Fatal("应至少回调一次进度")
	}
	if last != ref.SizeBytes {
		t.Fatalf("最终进度应等于总大小：%d vs %d", last, ref.SizeBytes)
	}
}

// TestArchiveCopyDetectsOversizeChunk 验证块超长被立即拒绝。
//
// 服务端给超长块说明它没有遵守协议边界，继续接受会让
// "每块 256 KiB"的内存约束失效（大归档可能被要求一次读进几十 MB）。
func TestArchiveCopyDetectsOversizeChunk(t *testing.T) {
	payload := []byte(strings.Repeat("y", 900000))
	be, ref, r := newArchiveFixture(payload)
	be.chunkSize = ChunkSize + 1 // 故意超一块

	err := r.Copy(context.Background(), "agent-a", ref, &memSink{}, nil)
	if !errors.Is(err, ErrArchiveCorrupt) {
		t.Fatalf("超长块应返回 ErrArchiveCorrupt，实际 %v", err)
	}
}

// TestArchiveCopyDetectsExtraBytes 验证累计字节超出声明大小被拒绝。
func TestArchiveCopyDetectsExtraBytes(t *testing.T) {
	be, ref, r := newArchiveFixture([]byte(strings.Repeat("z", 600000)))
	be.extraByte = true

	err := r.Copy(context.Background(), "agent-a", ref, &memSink{}, nil)
	if !errors.Is(err, ErrArchiveCorrupt) {
		t.Fatalf("超出声明大小应返回 ErrArchiveCorrupt，实际 %v", err)
	}
}

// TestArchiveCopyDetectsEmptyChunkWithoutEOF 验证空块未标 EOF 被拒绝。
//
// ============================ 这条防的是死循环 ============================
// 若服务端返回空块却不标 EOF，一个朴素的循环会永远空转，
// 表现为整台 Hub 的迁移卡住不动、CPU 占满。
func TestArchiveCopyDetectsEmptyChunkWithoutEOF(t *testing.T) {
	be, ref, r := newArchiveFixture([]byte("small"))
	be.emptyNoEOF = true

	err := r.Copy(context.Background(), "agent-a", ref, &memSink{}, nil)
	if !errors.Is(err, ErrArchiveCorrupt) {
		t.Fatalf("空块未标 EOF 应返回 ErrArchiveCorrupt，实际 %v", err)
	}
}

// TestArchiveCopyDetectsChecksumMismatch 验证校验和不符被拒绝。
//
// 这是最后一道、也是最重要的一道防线：前面所有长度检查都通过，
// 内容仍然可能是错的（例如源端归档在导出后被替换）。
func TestArchiveCopyDetectsChecksumMismatch(t *testing.T) {
	payload := []byte(strings.Repeat("payload", 1000))
	be, ref, r := newArchiveFixture(payload)
	// 声明一个不同的校验和：长度对得上，内容对不上。
	ref.SHA256 = strings.Repeat("0", 64)
	be.exportRef = ref

	err := r.Copy(context.Background(), "agent-a", ref, &memSink{}, nil)
	if !errors.Is(err, ErrArchiveCorrupt) {
		t.Fatalf("校验和不符应返回 ErrArchiveCorrupt，实际 %v", err)
	}
}

// TestArchiveCopyDetectsShortArchive 验证字节数不足被拒绝。
//
// 少给字节同样是损坏：不能因为"校验和还没算完"就放过去，
// 那会让下游 load 拿到一个被截断的 tar。
func TestArchiveCopyDetectsShortArchive(t *testing.T) {
	be, ref, r := newArchiveFixture([]byte(strings.Repeat("q", 500000)))
	// 声明比实际多：服务端必然"少给"，触发 offset != SizeBytes。
	ref.SizeBytes = int64(len(be.data)) + 1000
	be.exportRef = ref

	err := r.Copy(context.Background(), "agent-a", ref, &memSink{}, nil)
	if !errors.Is(err, ErrArchiveCorrupt) {
		t.Fatalf("字节数不足应返回 ErrArchiveCorrupt，实际 %v", err)
	}
}

// TestArchiveCopyRespectsCancel 验证取消能中断复制。
func TestArchiveCopyRespectsCancel(t *testing.T) {
	payload := []byte(strings.Repeat("c", 1000000))
	_, ref, r := newArchiveFixture(payload)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := r.Copy(ctx, "agent-a", ref, &memSink{}, nil)
	if !errors.Is(err, ErrCanceled) {
		t.Fatalf("已取消的上下文应返回 ErrCanceled，实际 %v", err)
	}
}

// TestArchiveCopyRejectsInvalidRef 验证不可用的引用被提前拒绝。
//
// ============================ 这条防的是"空归档假成功" ============================
// SizeBytes=0 的引用会让复制循环立刻结束并"成功"，
// 上层于是拿到一份空归档，而它看起来像一次成功的迁移。
func TestArchiveCopyRejectsInvalidRef(t *testing.T) {
	_, _, r := newArchiveFixture([]byte("x"))
	valid := strings.Repeat("a", 64)

	cases := []struct {
		name string
		ref  ArchiveRef
	}{
		{"空 export_id", ArchiveRef{SizeBytes: 10, SHA256: valid}},
		{"大小为 0", ArchiveRef{ExportID: "abc", SizeBytes: 0, SHA256: valid}},
		{"大小为负", ArchiveRef{ExportID: "abc", SizeBytes: -1, SHA256: valid}},
		{"超过上限", ArchiveRef{ExportID: "abc", SizeBytes: MaxArchiveSize + 1, SHA256: valid}},
		{"校验和非 hex", ArchiveRef{ExportID: "abc", SizeBytes: 10, SHA256: "zz"}},
		{"校验和过短", ArchiveRef{ExportID: "abc", SizeBytes: 10, SHA256: "abc"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if err := r.Copy(context.Background(), "agent-a", c.ref, &memSink{}, nil); err == nil {
				t.Fatalf("无效引用应被拒绝（%s）", c.name)
			}
		})
	}
}

// TestNewArchiveReaderRequiresBackend 验证不支持的后端给出可识别错误。
func TestNewArchiveReaderRequiresBackend(t *testing.T) {
	if _, err := NewArchiveReader(nil); !errors.Is(err, ErrArchiveBackendUnsupported) {
		t.Fatalf("nil 后端应返回 ErrArchiveBackendUnsupported，实际 %v", err)
	}
	// 只实现旧的 Backend、不实现 ArchiveBackend 时同样要明确报错，
	// 而不是在第一次使用时才 panic。
	if _, err := NewArchiveReader(struct{}{}); !errors.Is(err, ErrArchiveBackendUnsupported) {
		t.Fatalf("不支持的后端应返回 ErrArchiveBackendUnsupported，实际 %v", err)
	}
}

// TestFileSinkWritesAtOffset 验证 FileSink 按 offset 落位。
//
// 用 WriteAt 而不是顺序 Write：一旦漏块，顺序写会永久错位，
// 而 WriteAt 至少让"哪一块没写"变得可判断。
func TestFileSinkWritesAtOffset(t *testing.T) {
	buf := make([]byte, 16)
	s := &FileSink{W: &memWriterAt{buf: buf}}
	if err := s.Write(4, []byte("ABCD")); err != nil {
		t.Fatalf("写入失败: %v", err)
	}
	if string(buf[4:8]) != "ABCD" {
		t.Fatalf("应在 offset 4 处写入，实际 %q", string(buf))
	}
	// 空目标必须报错，而不是静默丢数据。
	if err := (&FileSink{}).Write(0, []byte("x")); err == nil {
		t.Fatal("空写入目标应报错")
	}
}

// memWriterAt 是 io.WriterAt 的最小实现。
type memWriterAt struct{ buf []byte }

func (m *memWriterAt) WriteAt(p []byte, off int64) (int, error) {
	if off < 0 || off+int64(len(p)) > int64(len(m.buf)) {
		return 0, errors.New("越界写入")
	}
	copy(m.buf[off:], p)
	return len(p), nil
}

// TestEqualHexIsCaseInsensitive 验证校验和比较忽略大小写。
//
// 不同引擎/系统给出的十六进制大小写并不统一，
// 直接字节比较会把一个正确的归档判成损坏。
func TestEqualHexIsCaseInsensitive(t *testing.T) {
	lower := strings.Repeat("ab", 32)
	upper := strings.Repeat("AB", 32)
	if !equalHex(lower, upper) {
		t.Fatal("大小写不同的同一校验和应判为相等")
	}
	if equalHex(lower, strings.Repeat("cd", 32)) {
		t.Fatal("不同校验和不应判为相等")
	}
}
