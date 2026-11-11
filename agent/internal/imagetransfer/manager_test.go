package imagetransfer

// manager_test.go 覆盖传输管理器最容易出错的地方：
// offset 幂等、配额、状态机、校验和规则、清理安全性。
//
// 这些用例刻意都用"手写档案"的方式驱动（直接调 PutChunk/Cancel 等），
// 不依赖真实引擎：规则的正确性与引擎无关，而引擎相关的部分
// 用假 runtime 就能验证调用契约。

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"arkade/agent/internal/runtime"
)

// fakeTransferRuntime 是可控的 ImageTransferRuntime。
type fakeTransferRuntime struct {
	mu sync.Mutex

	pullErr   error
	pullRef   string
	pullDelay bool

	saveData []byte
	saveErr  error

	loadErr    error
	loadImages []runtime.Image

	inspectSize  int64
	inspectErr   error
	inspectCalls int
}

func (f *fakeTransferRuntime) PullImage(_ context.Context, ref, _, _ string,
	onProgress func(runtime.PullProgress)) (runtime.PullResult, error) {
	if onProgress != nil {
		onProgress(runtime.PullProgress{BytesDone: 50, BytesTotal: 100, Status: "downloading"})
	}
	if f.pullErr != nil {
		return runtime.PullResult{}, f.pullErr
	}
	return runtime.PullResult{Reference: ref, Digest: f.pullRef}, nil
}

func (f *fakeTransferRuntime) SaveImage(_ context.Context, _ string) (io.ReadCloser, error) {
	if f.saveErr != nil {
		return nil, f.saveErr
	}
	return io.NopCloser(bytes.NewReader(f.saveData)), nil
}

func (f *fakeTransferRuntime) LoadImage(_ context.Context, body io.Reader, size int64) ([]runtime.Image, error) {
	if f.loadErr != nil {
		return nil, f.loadErr
	}
	// 必须把 body 读完：否则调用方那边的"读取归档"路径没有被验证过。
	n, _ := io.Copy(io.Discard, body)
	if n != size {
		return nil, errors.New("读到的字节数与声明不符")
	}
	return f.loadImages, nil
}

func (f *fakeTransferRuntime) InspectImage(_ context.Context, _ string) (runtime.ImageDetail, error) {
	f.mu.Lock()
	f.inspectCalls++
	f.mu.Unlock()
	if f.inspectErr != nil {
		return runtime.ImageDetail{}, f.inspectErr
	}
	return runtime.ImageDetail{ID: "sha256:abc", Size: f.inspectSize}, nil
}

// ============================ 补齐 Runtime 主接口 ============================
//
// fakeTransferRuntime 需要同时实现 runtime.Runtime 与 ImageTransferRuntime：
// NewManager 的形参是完整的 Runtime，内部再断言传输能力。
// 这些方法与传输无关，返回零值即可——它们的存在只是为了满足接口，
// 写在这里也正说明了"可选接口"的价值：只有测试替身需要补齐，
// 生产代码里不实现传输的引擎**一行都不用改**。

func (f *fakeTransferRuntime) Info(context.Context) (runtime.Info, error) { return runtime.Info{}, nil }
func (f *fakeTransferRuntime) List(context.Context, bool) ([]runtime.Container, error) {
	return nil, nil
}
func (f *fakeTransferRuntime) Start(context.Context, string) error { return nil }
func (f *fakeTransferRuntime) Stop(context.Context, string, bool) error {
	return nil
}
func (f *fakeTransferRuntime) Restart(context.Context, string) error { return nil }
func (f *fakeTransferRuntime) RemoveContainer(context.Context, string, bool, bool) error {
	return nil
}
func (f *fakeTransferRuntime) Logs(context.Context, string, runtime.LogOptions) (runtime.LogResult, error) {
	return runtime.LogResult{}, nil
}
func (f *fakeTransferRuntime) ListImages(context.Context) ([]runtime.Image, error) { return nil, nil }
func (f *fakeTransferRuntime) RemoveImage(context.Context, string, bool) error     { return nil }
func (f *fakeTransferRuntime) ListNetworks(context.Context) ([]runtime.Network, error) {
	return nil, nil
}
func (f *fakeTransferRuntime) RemoveNetwork(context.Context, string) error { return nil }
func (f *fakeTransferRuntime) ListVolumes(context.Context) ([]runtime.Volume, error) {
	return nil, nil
}
func (f *fakeTransferRuntime) RemoveVolume(context.Context, string) error { return nil }

// newTestManager 构造一个使用临时目录的管理器。
func newTestManager(t *testing.T, rt runtime.Runtime) *Manager {
	t.Helper()
	m, err := NewManager(t.TempDir(), rt, DefaultLimits())
	if err != nil {
		t.Fatalf("构造管理器失败: %v", err)
	}
	return m
}

// waitTask 轮询直到任务进入终态（或超时）。
func waitTask(t *testing.T, m *Manager, id string) Snapshot {
	t.Helper()
	for i := 0; i < 400; i++ {
		s, err := m.Status(id)
		if err != nil {
			t.Fatalf("查询状态失败: %v", err)
		}
		if s.Status.terminal() {
			return s
		}
		// 用很短的重试间隔；任务都是内存+小文件，通常毫秒级完成。
		sleepMS(5)
	}
	t.Fatalf("任务 %s 未在预期时间内结束", id)
	return Snapshot{}
}

// TestBeginLoadRejectsNonPositiveSize 验证 begin 必须给出确切大小。
//
// 没有大小就无法在写入前判断配额，"传到 90% 才发现不够"会白白占掉配额。
func TestBeginLoadRejectsNonPositiveSize(t *testing.T) {
	m := newTestManager(t, &fakeTransferRuntime{})
	for _, size := range []int64{0, -1, -1000} {
		if _, err := m.BeginLoad(context.Background(), size, ""); err == nil {
			t.Fatalf("size_bytes=%d 应当被拒绝", size)
		}
	}
}

// TestPutChunkOffsetRules 覆盖 offset 幂等的三种情况。
func TestPutChunkOffsetRules(t *testing.T) {
	m := newTestManager(t, &fakeTransferRuntime{})
	part1 := bytes.Repeat([]byte("A"), 100)
	part2 := bytes.Repeat([]byte("B"), 100)
	total := int64(len(part1) + len(part2))

	snap, err := m.BeginLoad(context.Background(), total, "")
	if err != nil {
		t.Fatalf("begin 失败: %v", err)
	}

	// 正常追加。
	received, dup, err := m.PutChunk(context.Background(), snap.ID, 0, part1)
	if err != nil || received != 100 || dup {
		t.Fatalf("首次写入应成功且非重试: received=%d dup=%v err=%v", received, dup, err)
	}

	// 同字节重试：必须成功且标记 duplicate。
	received, dup, err = m.PutChunk(context.Background(), snap.ID, 0, part1)
	if err != nil || received != 100 || !dup {
		t.Fatalf("同字节重试应成功并标记重试: received=%d dup=%v err=%v", received, dup, err)
	}

	// 不同字节覆盖同一区间：必须拒绝。否则一次错位的重试会悄悄产生
	// "看起来完整、实际混了两个版本"的归档。
	if _, _, err := m.PutChunk(context.Background(), snap.ID, 0, part2); !errors.Is(err, ErrBadOffset) {
		t.Fatalf("覆盖不同字节应返回 ErrBadOffset，实际: %v", err)
	}

	// 跳过中间块：必须拒绝。
	if _, _, err := m.PutChunk(context.Background(), snap.ID, 500, part2); !errors.Is(err, ErrBadOffset) {
		t.Fatalf("offset 超过已接收量应返回 ErrBadOffset，实际: %v", err)
	}

	// 继续追加到完整。
	received, _, err = m.PutChunk(context.Background(), snap.ID, 100, part2)
	if err != nil || received != total {
		t.Fatalf("追加应成功: received=%d err=%v", received, err)
	}

	// 超出声明大小：必须拒绝。
	if _, _, err := m.PutChunk(context.Background(), snap.ID, total, part1); !errors.Is(err, ErrSizeMismatch) {
		t.Fatalf("超出声明大小应返回 ErrSizeMismatch，实际: %v", err)
	}
}

// TestCommitRequiresChecksum 验证"两个都空则拒绝"。
//
// 没有校验和就没有手段确认最终字节与 Hub 算的是同一份，
// 一次错位的重试会静默产生损坏归档。
func TestCommitRequiresChecksum(t *testing.T) {
	m := newTestManager(t, &fakeTransferRuntime{loadImages: []runtime.Image{{Tags: []string{"x:1"}}}})
	data := []byte("hello archive")

	snap, err := m.BeginLoad(context.Background(), int64(len(data)), "")
	if err != nil {
		t.Fatalf("begin 失败: %v", err)
	}
	if _, _, err := m.PutChunk(context.Background(), snap.ID, 0, data); err != nil {
		t.Fatalf("写入失败: %v", err)
	}

	if _, err := m.CommitLoad(context.Background(), snap.ID, ""); !errors.Is(err, ErrMissingChecksum) {
		t.Fatalf("两个校验和都空应返回 ErrMissingChecksum，实际: %v", err)
	}
}

// TestCommitRejectsConflictingChecksums 验证 begin/commit 两个非空但不同时必须拒绝。
//
// 这是本功能最重要的安全规则之一：begin 的 SHA 通常来自迁移源 Agent
// （这份归档**本来应该是**什么），commit 是 Hub 边收边算的结果
// （**实际收到了**什么）。两者不同恰恰是最该报出来的信号。
// 若允许以 commit 为准悄悄覆盖，损坏的归档会被当成正常数据 load 进引擎，
// 而所有记录都显示"校验通过"。
func TestCommitRejectsConflictingChecksums(t *testing.T) {
	m := newTestManager(t, &fakeTransferRuntime{loadImages: []runtime.Image{{Tags: []string{"x:1"}}}})
	data := []byte("payload")
	sum := sha256.Sum256(data)
	realSHA := hex.EncodeToString(sum[:])

	other, _ := hex.DecodeString(strings.Repeat("ab", 32))

	// begin 给"源侧"的 SHA，commit 给一个不同的值。
	snap, err := m.BeginLoad(context.Background(), int64(len(data)), realSHA)
	if err != nil {
		t.Fatalf("begin 失败: %v", err)
	}
	if _, _, err := m.PutChunk(context.Background(), snap.ID, 0, data); err != nil {
		t.Fatalf("写入失败: %v", err)
	}
	if _, err := m.CommitLoad(context.Background(), snap.ID, hex.EncodeToString(other)); !errors.Is(err, ErrChecksumMismatch) {
		t.Fatalf("不一致的两个校验和应返回 ErrChecksumMismatch，实际: %v", err)
	}
}

// TestCommitDetectsCorruptArchive 验证 commit 会真的重算哈希。
//
// 场景：分块全部"成功"但内容与期望不同（例如 Hub 侧算错或传错）。
// begin/commit 都只给了期望值，实际字节不符时必须失败，
// 而不是把损坏的 tar 交给引擎去 load。
func TestCommitDetectsCorruptArchive(t *testing.T) {
	m := newTestManager(t, &fakeTransferRuntime{loadImages: []runtime.Image{{Tags: []string{"x:1"}}}})
	data := []byte("actual bytes")
	expect := sha256.Sum256([]byte("different bytes"))
	expectSHA := hex.EncodeToString(expect[:])

	snap, _ := m.BeginLoad(context.Background(), int64(len(data)), expectSHA)
	if _, _, err := m.PutChunk(context.Background(), snap.ID, 0, data); err != nil {
		t.Fatalf("写入失败: %v", err)
	}
	out, err := m.CommitLoad(context.Background(), snap.ID, "")
	if err != nil {
		t.Fatalf("commit 受理失败: %v", err)
	}
	if out.Status == StatusFailed {
		// 有的实现会即时失败，也接受。
		return
	}
	final := waitTask(t, m, snap.ID)
	if final.Status != StatusFailed {
		t.Fatalf("损坏归档必须判定失败，实际状态: %s", final.Status)
	}
	if !strings.Contains(final.Message, "校验和") {
		t.Fatalf("失败说明应指出校验和问题，实际: %s", final.Message)
	}
}

// TestCommitSucceedsAndDeletesArchive 验证正常导入后归档被删除（释放配额）。
func TestCommitSucceedsAndDeletesArchive(t *testing.T) {
	rt := &fakeTransferRuntime{loadImages: []runtime.Image{{Tags: []string{"nginx:1.25"}}}}
	m := newTestManager(t, rt)
	data := []byte("valid archive bytes")
	sum := sha256.Sum256(data)

	snap, _ := m.BeginLoad(context.Background(), int64(len(data)), "")
	if _, _, err := m.PutChunk(context.Background(), snap.ID, 0, data); err != nil {
		t.Fatalf("写入失败: %v", err)
	}
	if _, err := m.CommitLoad(context.Background(), snap.ID, hex.EncodeToString(sum[:])); err != nil {
		t.Fatalf("commit 失败: %v", err)
	}
	final := waitTask(t, m, snap.ID)
	if final.Status != StatusSucceeded {
		t.Fatalf("导入应成功，实际: %s（%s）", final.Status, final.Message)
	}
	if final.Reference != "nginx:1.25" {
		t.Fatalf("应回传加载的镜像名，实际: %q", final.Reference)
	}
}

// TestSaveThenChunkGetRoundTrip 验证导出 → 分块读回 → 再导入的完整闭环。
//
// 这是跨 Agent 迁移的核心路径，必须端到端可验证。
func TestSaveThenChunkGetRoundTrip(t *testing.T) {
	payload := bytes.Repeat([]byte("arkade-migration-payload-"), 20000) // ~500KB，跨多块
	rt := &fakeTransferRuntime{
		saveData:    payload,
		inspectSize: int64(len(payload)),
		loadImages:  []runtime.Image{{Tags: []string{"app:latest"}}},
	}
	m := newTestManager(t, rt)

	saveSnap, err := m.Save(context.Background(), "app:latest")
	if err != nil {
		t.Fatalf("save 失败: %v", err)
	}
	saved := waitTask(t, m, saveSnap.ID)
	if saved.Status != StatusSucceeded {
		t.Fatalf("导出应成功，实际: %s（%s）", saved.Status, saved.Message)
	}
	if saved.SizeBytes != int64(len(payload)) {
		t.Fatalf("归档大小应为 %d，实际 %d", len(payload), saved.SizeBytes)
	}

	// 分块读回全部字节。
	var got []byte
	var offset int64
	for i := 0; i < 100; i++ {
		dataB64, off, eof, size, gerr := m.GetChunk(context.Background(), saveSnap.ID, offset, 0)
		if gerr != nil {
			t.Fatalf("读块失败: %v", gerr)
		}
		if size != int64(len(payload)) {
			t.Fatalf("返回的归档大小不符: %d", size)
		}
		chunk, derr := base64.StdEncoding.DecodeString(dataB64)
		if derr != nil {
			t.Fatalf("data_base64 解码失败: %v", derr)
		}
		if off != offset {
			t.Fatalf("offset 应回显 %d，实际 %d", offset, off)
		}
		got = append(got, chunk...)
		offset += int64(len(chunk))
		if eof {
			break
		}
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("读回的内容与导出内容不一致（len %d vs %d）", len(got), len(payload))
	}

	// 再导入到同一个管理器（模拟目标 Agent 侧）。
	sum := sha256.Sum256(payload)
	begin, berr := m.BeginLoad(context.Background(), int64(len(payload)), hex.EncodeToString(sum[:]))
	if berr != nil {
		t.Fatalf("begin 失败: %v", berr)
	}
	var woff int64
	for woff < int64(len(payload)) {
		end := woff + ChunkSize
		if end > int64(len(payload)) {
			end = int64(len(payload))
		}
		if _, _, perr := m.PutChunk(context.Background(), begin.ID, woff, payload[woff:end]); perr != nil {
			t.Fatalf("上传块失败: %v", perr)
		}
		woff = end
	}
	if _, cerr := m.CommitLoad(context.Background(), begin.ID, ""); cerr != nil {
		t.Fatalf("commit 失败: %v", cerr)
	}
	loaded := waitTask(t, m, begin.ID)
	if loaded.Status != StatusSucceeded {
		t.Fatalf("导入应成功，实际: %s（%s）", loaded.Status, loaded.Message)
	}
}

// TestSaveRejectsOversizedArchive 验证单归档硬上限。
func TestSaveRejectsOversizedArchive(t *testing.T) {
	rt := &fakeTransferRuntime{inspectSize: DefaultMaxArchiveBytes + 1}
	m := newTestManager(t, rt)
	if _, err := m.Save(context.Background(), "huge:1"); !errors.Is(err, ErrArchiveTooLarge) {
		t.Fatalf("超大归档应返回 ErrArchiveTooLarge，实际: %v", err)
	}
}

// TestBeginLoadRejectsQuotaExceeded 验证总量配额。
//
// 单归档上限与总量上限是两回事：四次 1GiB 导出都没超单归档上限，
// 合起来却吃掉整个传输临时区。
func TestBeginLoadRejectsQuotaExceeded(t *testing.T) {
	m := newTestManager(t, &fakeTransferRuntime{})
	m.limits.MaxTotalArchiveBytes = 1000
	m.limits.MaxArchiveBytes = 1000

	if _, err := m.BeginLoad(context.Background(), 800, ""); err != nil {
		t.Fatalf("第一次 begin 应成功: %v", err)
	}
	if _, err := m.BeginLoad(context.Background(), 800, ""); !errors.Is(err, ErrQuotaExceeded) && !errors.Is(err, ErrTooManyTasks) {
		t.Fatalf("第二次 begin 应被配额或并发上限拒绝，实际: %v", err)
	}
}

// TestTooManyTasks 验证并发上限。
func TestTooManyTasks(t *testing.T) {
	m := newTestManager(t, &fakeTransferRuntime{})
	m.limits.MaxConcurrentTasks = 2

	if _, err := m.BeginLoad(context.Background(), 10, ""); err != nil {
		t.Fatalf("第 1 个任务应成功: %v", err)
	}
	if _, err := m.BeginLoad(context.Background(), 10, ""); err != nil {
		t.Fatalf("第 2 个任务应成功: %v", err)
	}
	if _, err := m.BeginLoad(context.Background(), 10, ""); !errors.Is(err, ErrTooManyTasks) {
		t.Fatalf("第 3 个任务应返回 ErrTooManyTasks，实际: %v", err)
	}
}

// TestCancelLoadMentionsNoRollback 验证取消 load 时如实说明无法回滚。
//
// 镜像层一旦入库没有事务。"已回滚"是做不到的承诺，
// 文案必须让用户知道可能已写入部分镜像。
func TestCancelLoadMentionsNoRollback(t *testing.T) {
	m := newTestManager(t, &fakeTransferRuntime{})
	snap, err := m.BeginLoad(context.Background(), 100, "")
	if err != nil {
		t.Fatalf("begin 失败: %v", err)
	}
	canceled, msg, cerr := m.Cancel(snap.ID)
	if cerr != nil || !canceled {
		t.Fatalf("应取消成功: canceled=%v err=%v", canceled, cerr)
	}
	if !strings.Contains(msg, "无法回滚") {
		t.Fatalf("取消说明必须提示不可回滚，实际: %q", msg)
	}
}

// TestReleaseRefusesRunningTask 验证执行中的任务不能被释放。
//
// 释放执行中的任务会删掉它正在写的归档，导致任务失败且原因不明。
// "暂时不能释放"是正常时序，用 released=false 表达而不是报错。
func TestReleaseRefusesRunningTask(t *testing.T) {
	m := newTestManager(t, &fakeTransferRuntime{})
	snap, err := m.BeginLoad(context.Background(), 100, "")
	if err != nil {
		t.Fatalf("begin 失败: %v", err)
	}
	released, msg, rerr := m.Release(snap.ID)
	if rerr != nil {
		t.Fatalf("release 不应返回错误: %v", rerr)
	}
	if released {
		t.Fatalf("执行中的任务不应被释放")
	}
	if msg == "" {
		t.Fatalf("未释放时应给出说明")
	}
}

// TestStatusAfterReleaseIsNotFound 验证释放后任务不可再查。
func TestStatusAfterReleaseIsNotFound(t *testing.T) {
	m := newTestManager(t, &fakeTransferRuntime{})
	snap, _ := m.BeginLoad(context.Background(), 100, "")
	if _, _, err := m.Cancel(snap.ID); err != nil {
		t.Fatalf("取消失败: %v", err)
	}
	// 等状态转为 canceled。
	for i := 0; i < 100; i++ {
		s, _ := m.Status(snap.ID)
		if s.Status.terminal() {
			break
		}
		sleepMS(5)
	}
	released, _, err := m.Release(snap.ID)
	if err != nil || !released {
		t.Fatalf("终态任务应可释放: released=%v err=%v", released, err)
	}
	if _, err := m.Status(snap.ID); !errors.Is(err, ErrTaskNotFound) {
		t.Fatalf("释放后应返回 ErrTaskNotFound，实际: %v", err)
	}
}

// TestPullFailureMessageIsSanitized 验证拉取失败文案不含引擎原文。
//
// 引擎/registry 原文常含 registry 认证失败详情、完整内网地址。
func TestPullFailureMessageIsSanitized(t *testing.T) {
	rt := &fakeTransferRuntime{pullErr: errors.New("连接运行时失败: dial tcp 10.1.2.3:5000")}
	m := newTestManager(t, rt)
	snap, err := m.Pull(context.Background(), "app:1", "", "")
	if err != nil {
		t.Fatalf("pull 应受理: %v", err)
	}
	final := waitTask(t, m, snap.ID)
	if final.Status != StatusFailed {
		t.Fatalf("应判定失败，实际: %s", final.Status)
	}
	if strings.Contains(final.Message, "10.1.2.3") {
		t.Fatalf("失败文案不得包含内网地址，实际: %q", final.Message)
	}
}

// TestUnsupportedWhenRuntimeLacksCapability 验证没有传输能力时降级为 unsupported。
//
// 这正是"可选接口"的意义：现有不实现 ImageTransferRuntime 的 runtime
// 一行都不用改，只是传输方法不可用。
func TestUnsupportedWhenRuntimeLacksCapability(t *testing.T) {
	m := newTestManager(t, &plainRuntime{})
	if m.Supported() {
		t.Fatalf("不实现 ImageTransferRuntime 的 runtime 不应被认为支持传输")
	}
	if _, err := m.Pull(context.Background(), "x:1", "", ""); !errors.Is(err, errUnsupported) {
		t.Fatalf("应返回 errUnsupported，实际: %v", err)
	}
}

// plainRuntime 只实现 Runtime 主接口，不实现 ImageTransferRuntime。
type plainRuntime struct{}

func (p *plainRuntime) Info(context.Context) (runtime.Info, error) { return runtime.Info{}, nil }
func (p *plainRuntime) List(context.Context, bool) ([]runtime.Container, error) {
	return nil, nil
}
func (p *plainRuntime) Start(context.Context, string) error      { return nil }
func (p *plainRuntime) Stop(context.Context, string, bool) error { return nil }
func (p *plainRuntime) Restart(context.Context, string) error    { return nil }
func (p *plainRuntime) RemoveContainer(context.Context, string, bool, bool) error {
	return nil
}
func (p *plainRuntime) Logs(context.Context, string, runtime.LogOptions) (runtime.LogResult, error) {
	return runtime.LogResult{}, nil
}
func (p *plainRuntime) ListImages(context.Context) ([]runtime.Image, error) { return nil, nil }
func (p *plainRuntime) RemoveImage(context.Context, string, bool) error     { return nil }
func (p *plainRuntime) ListNetworks(context.Context) ([]runtime.Network, error) {
	return nil, nil
}
func (p *plainRuntime) RemoveNetwork(context.Context, string) error { return nil }
func (p *plainRuntime) ListVolumes(context.Context) ([]runtime.Volume, error) {
	return nil, nil
}
func (p *plainRuntime) RemoveVolume(context.Context, string) error { return nil }

// TestCleanupOrphansOnlyTouchesTaskDirs 验证启动清理的保守性。
//
// 只清理符合任务 id 形态的目录，绝不碰别的东西：
// 宁可留下垃圾，也不误删用户手工放置的文件。
func TestCleanupOrphansOnlyTouchesTaskDirs(t *testing.T) {
	dir := t.TempDir()
	st, err := newStorage(dir)
	if err != nil {
		t.Fatalf("建临时区失败: %v", err)
	}

	// 一个"看起来像任务"的残留目录。
	orphan := filepath.Join(st.root, strings.Repeat("a", 32))
	if err := os.MkdirAll(orphan, 0o700); err != nil {
		t.Fatalf("建残留目录失败: %v", err)
	}
	// 一个不符合 id 形态的目录：必须保留。
	keep := filepath.Join(st.root, "user-notes")
	if err := os.MkdirAll(keep, 0o700); err != nil {
		t.Fatalf("建保留目录失败: %v", err)
	}

	removed := st.cleanupOrphans(nil)
	if removed != 1 {
		t.Fatalf("应只清理 1 个残留目录，实际 %d", removed)
	}
	if _, err := os.Stat(orphan); !os.IsNotExist(err) {
		t.Fatalf("残留目录应被清理")
	}
	if _, err := os.Stat(keep); err != nil {
		t.Fatalf("不符合 id 形态的目录不应被删除: %v", err)
	}
}

// TestArchiveIsSecretMode 验证归档权限位（POSIX 才可验证）。
func TestArchiveIsSecretMode(t *testing.T) {
	if os.PathSeparator != '/' {
		t.Skip("Windows 无 POSIX 权限位，跳过")
	}
	m := newTestManager(t, &fakeTransferRuntime{})
	snap, _ := m.BeginLoad(context.Background(), 10, "")
	if _, _, err := m.PutChunk(context.Background(), snap.ID, 0, []byte("0123456789")); err != nil {
		t.Fatalf("写入失败: %v", err)
	}
	dir, _ := m.store.taskDir(snap.ID)
	info, err := os.Stat(filepath.Join(dir, archiveFileName))
	if err != nil {
		t.Fatalf("归档文件应存在: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("归档权限应为 0600，实际 %o", perm)
	}
}

// sleepMS 是测试用的短睡眠（避免各用例重复写 time.Sleep）。
func sleepMS(n int) {
	time.Sleep(time.Duration(n) * time.Millisecond)
}

// TestNewStorageRejectsSymlinkRoot 验证 transfers 本身是符号链接时被拒绝。
//
// 这是最危险的场景：若跟随链接，启动清理会去删**链接目标**里的目录，
// 一次"清理残留传输"就变成删除系统目录。
// 用 sentinel 目录证明目标真的没被碰过。
func TestNewStorageRejectsSymlinkRoot(t *testing.T) {
	if os.PathSeparator != '/' {
		t.Skip("Windows 创建符号链接需要额外权限，跳过")
	}
	dataDir := t.TempDir()
	sentinel := t.TempDir()

	// 在 sentinel 里放一个"看起来像残留任务"的目录：
	// 如果代码跟随了链接，它会被 cleanupOrphans 删掉。
	orphan := filepath.Join(sentinel, strings.Repeat("a", 32))
	if err := os.MkdirAll(orphan, 0o700); err != nil {
		t.Fatalf("建哨兵目录失败: %v", err)
	}

	// 把 <dataDir>/transfers 做成指向 sentinel 的链接。
	if err := os.Symlink(sentinel, filepath.Join(dataDir, transfersDirName)); err != nil {
		t.Skipf("无法创建符号链接: %v", err)
	}

	if _, err := newStorage(dataDir); err == nil {
		t.Fatalf("transfers 是符号链接时必须拒绝")
	}

	// 哨兵目录必须完好无损。
	if _, err := os.Stat(orphan); err != nil {
		t.Fatalf("哨兵目录被删掉了，说明代码跟随了符号链接: %v", err)
	}
}

// TestCreateArchiveRejectsSymlinkDir 验证任务目录被替换成链接时拒绝写入。
func TestCreateArchiveRejectsSymlinkDir(t *testing.T) {
	if os.PathSeparator != '/' {
		t.Skip("Windows 创建符号链接需要额外权限，跳过")
	}
	st, err := newStorage(t.TempDir())
	if err != nil {
		t.Fatalf("建临时区失败: %v", err)
	}
	sentinel := t.TempDir()
	taskID := strings.Repeat("b", 32)

	// 把任务目录做成指向哨兵的链接。
	if err := os.Symlink(sentinel, filepath.Join(st.root, taskID)); err != nil {
		t.Skipf("无法创建符号链接: %v", err)
	}

	if _, _, err := st.createArchive(taskID); err == nil {
		t.Fatalf("任务目录是符号链接时必须拒绝创建归档")
	}
	// 哨兵目录里不应出现任何文件。
	entries, _ := os.ReadDir(sentinel)
	if len(entries) != 0 {
		t.Fatalf("归档被写进了链接目标（%d 个文件）", len(entries))
	}
}

// TestCleanupOrphansDoesNotDeleteSymlinkTarget 验证清理只删链接本身。
func TestCleanupOrphansDoesNotDeleteSymlinkTarget(t *testing.T) {
	if os.PathSeparator != '/' {
		t.Skip("Windows 创建符号链接需要额外权限，跳过")
	}
	st, err := newStorage(t.TempDir())
	if err != nil {
		t.Fatalf("建临时区失败: %v", err)
	}
	sentinel := t.TempDir()
	// 哨兵里放一个子目录，若被跟随删除就会消失。
	inner := filepath.Join(sentinel, "precious")
	if err := os.MkdirAll(inner, 0o700); err != nil {
		t.Fatalf("建哨兵失败: %v", err)
	}
	if err := os.WriteFile(filepath.Join(inner, "data.txt"), []byte("keep"), 0o600); err != nil {
		t.Fatalf("写哨兵文件失败: %v", err)
	}

	// 一个符合任务 id 形态、但实际是链接的条目。
	linkName := strings.Repeat("c", 32)
	if err := os.Symlink(sentinel, filepath.Join(st.root, linkName)); err != nil {
		t.Skipf("无法创建符号链接: %v", err)
	}

	removed := st.cleanupOrphans(nil)
	if removed != 1 {
		t.Fatalf("应删除 1 个链接，实际 %d", removed)
	}
	// 链接本身没了。
	if _, err := os.Lstat(filepath.Join(st.root, linkName)); !os.IsNotExist(err) {
		t.Fatalf("链接本身应被删除")
	}
	// 目标里的内容必须完好。
	if _, err := os.Stat(filepath.Join(inner, "data.txt")); err != nil {
		t.Fatalf("链接目标被删掉了，清理逻辑跟随了符号链接: %v", err)
	}
}

// TestCleanupOrphansSkipsRegularFiles 验证普通文件不被删除。
func TestCleanupOrphansSkipsRegularFiles(t *testing.T) {
	st, err := newStorage(t.TempDir())
	if err != nil {
		t.Fatalf("建临时区失败: %v", err)
	}
	// 名字符合 id 形态，但是普通文件——不是我们建的，不能删。
	name := strings.Repeat("d", 32)
	if err := os.WriteFile(filepath.Join(st.root, name), []byte("x"), 0o600); err != nil {
		t.Fatalf("写文件失败: %v", err)
	}
	st.cleanupOrphans(nil)
	if _, err := os.Stat(filepath.Join(st.root, name)); err != nil {
		t.Fatalf("普通文件不应被清理: %v", err)
	}
}

// endlessSaveRuntime 的 SaveImage 返回一个**无限输出**的流，模拟异常/恶意引擎。
//
// 这类引擎在任何时刻都在合法地"继续给数据"，唯一的防线是写入前的限额。
// 若限额是事后检查，磁盘会被写满之后才报错。
type endlessSaveRuntime struct {
	fakeTransferRuntime
	// closed 在流被 Close 时关闭，用于验证我们会主动关流。
	closed chan struct{}
	once   sync.Once
}

func (e *endlessSaveRuntime) SaveImage(ctx context.Context, _ string) (io.ReadCloser, error) {
	return &endlessReader{closed: e.closed, once: &e.once}, nil
}

type endlessReader struct {
	closed chan struct{}
	once   *sync.Once
	n      int64
}

func (r *endlessReader) Read(p []byte) (int, error) {
	// 无限供给数据（每次填满缓冲区）。
	for i := range p {
		p[i] = 'X'
	}
	r.n += int64(len(p))
	return len(p), nil
}

func (r *endlessReader) Close() error {
	r.once.Do(func() { close(r.closed) })
	return nil
}

// TestConcurrentLoadAndSaveNotFalselyRejected 验证并发 load + save 不被误拒。
//
// 这是迁移主路径：从源 Agent 导出（save）到目标 Agent 导入（load）。
// 若配额记账过于激进（例如把每个任务的"最坏增量"都按整个单归档上限算，
// 2 个任务就是 4GiB），在默认 4GiB 总量下会导致**任何**迁移都失败。
// 这个用例把这条底线钉住。
func TestConcurrentLoadAndSaveNotFalselyRejected(t *testing.T) {
	// 8 MiB 归档，远小于 2GiB 单归档上限与 4GiB 总量上限。
	payload := bytes.Repeat([]byte("M"), 8<<20)
	sum := sha256.Sum256(payload)
	rt := &fakeTransferRuntime{
		saveData:    payload,
		inspectSize: int64(len(payload)),
		loadImages:  []runtime.Image{{Tags: []string{"app:1"}}},
	}
	m := newTestManager(t, rt) // 用默认限额：2GiB / 4GiB / 2 并发

	// 任务 1：load（声明 8MiB）。
	beginSnap, err := m.BeginLoad(context.Background(), int64(len(payload)), hex.EncodeToString(sum[:]))
	if err != nil {
		t.Fatalf("默认限额下 load.begin 不应失败: %v", err)
	}

	// 任务 2：save（同时存在）。
	saveSnap, err := m.Save(context.Background(), "app:1")
	if err != nil {
		t.Fatalf("默认限额下 save 不应失败（与 load 并发）: %v", err)
	}

	// 把 load 的分块传完并提交。
	var off int64
	for off < int64(len(payload)) {
		end := off + ChunkSize
		if end > int64(len(payload)) {
			end = int64(len(payload))
		}
		if _, _, perr := m.PutChunk(context.Background(), beginSnap.ID, off, payload[off:end]); perr != nil {
			t.Fatalf("上传失败: %v", perr)
		}
		off = end
	}
	if _, cerr := m.CommitLoad(context.Background(), beginSnap.ID, ""); cerr != nil {
		t.Fatalf("commit 失败: %v", cerr)
	}

	if got := waitTask(t, m, beginSnap.ID); got.Status != StatusSucceeded {
		t.Fatalf("load 应成功，实际: %s（%s）", got.Status, got.Message)
	}
	if got := waitTask(t, m, saveSnap.ID); got.Status != StatusSucceeded {
		t.Fatalf("save 应成功，实际: %s（%s）", got.Status, got.Message)
	}
}

// TestSmallSaveNotRejectedByDefaultQuota 验证默认限额下小归档导出不会被误拒。
//
// 回归用例：曾经把每个写者的"最坏增量"按整个单归档上限（2GiB）计入预留，
// 于是一个 8MB 的导出也被算成占 2GiB，两个任务就吃满 4GiB，
// 连最基本的迁移都跑不通。
func TestSmallSaveNotRejectedByDefaultQuota(t *testing.T) {
	payload := bytes.Repeat([]byte("S"), 8<<20)
	rt := &fakeTransferRuntime{saveData: payload, inspectSize: int64(len(payload))}
	m := newTestManager(t, rt)

	snap, err := m.Save(context.Background(), "app:1")
	if err != nil {
		t.Fatalf("8MB 导出在默认限额下不应被拒: %v", err)
	}
	if got := waitTask(t, m, snap.ID); got.Status != StatusSucceeded {
		t.Fatalf("导出应成功，实际: %s（%s）", got.Status, got.Message)
	}
}

// TestCancelThenImmediateReleaseLeavesNoOrphan 验证"取消后立刻释放"不留孤儿目录。
//
// 这个用例针对一个真实竞态：若 writer 登记发生在 go 语句**内部**，
// 那么 acquireTask 返回与 goroutine 真正启动之间有一个窗口。
// 此刻 Cancel 紧接着 Release，Release 看到 writers==0 就把任务移出表并删目录；
// 随后 goroutine 启动、`dirFor` 重新建出目录并写入——
// 这份占用逃过了配额记账，且无人能查询或取消，直到进程退出。
//
// 循环多次以提高命中概率（这类窗口在单次运行里可能不出现）。
func TestCancelThenImmediateReleaseLeavesNoOrphan(t *testing.T) {
	for i := 0; i < 30; i++ {
		rt := &fakeTransferRuntime{
			saveData:    bytes.Repeat([]byte("O"), 4096),
			inspectSize: 4096,
		}
		m := newTestManager(t, rt)

		snap, err := m.Save(context.Background(), "app:1")
		if err != nil {
			t.Fatalf("第 %d 次 save 应受理: %v", i, err)
		}
		// 立刻取消 + 释放（不给 worker 任何喘息时间）。
		_, _, _ = m.Cancel(snap.ID)
		_, _, _ = m.Release(snap.ID)

		// 等 worker 收尾（若它已在跑，会自己清理）。
		for j := 0; j < 400; j++ {
			m.mu.Lock()
			w := int32(0)
			if tt, ok := m.tasks[snap.ID]; ok {
				w = tt.writers
			}
			m.mu.Unlock()
			if w == 0 {
				break
			}
			sleepMS(2)
		}
		// 再释放一次（幂等）。
		_, _, _ = m.Release(snap.ID)

		// 关键断言：transfers 下不应留下任何归档。
		if used := m.store.usage(); used != 0 {
			// 给 worker 一点点时间完成清理，再复查一次。
			for j := 0; j < 100; j++ {
				if m.store.usage() == 0 {
					break
				}
				sleepMS(5)
			}
			if got := m.store.usage(); got != 0 {
				t.Fatalf("第 %d 次：取消后释放留下孤儿归档 %d 字节", i, got)
			}
		}
	}
}

// TestLoadSuccessDeletesArchiveImmediately 验证导入成功后归档立即删除。
//
// 回归用例：runLoad 曾用 defer endRead，而成功分支同步调
// removeArchiveWhenIdle（等 readers 归零）——等的是自己，
// 必然耗到 2s 超时，归档还可能遗留到下次启动才被清理。
// 正确行为是读完即归零，删除几乎瞬时完成。
func TestLoadSuccessDeletesArchiveImmediately(t *testing.T) {
	payload := bytes.Repeat([]byte("L"), 512<<10)
	sum := sha256.Sum256(payload)
	rt := &fakeTransferRuntime{loadImages: []runtime.Image{{Tags: []string{"app:1"}}}}
	m := newTestManager(t, rt)

	snap, err := m.BeginLoad(context.Background(), int64(len(payload)), hex.EncodeToString(sum[:]))
	if err != nil {
		t.Fatalf("begin 失败: %v", err)
	}
	var off int64
	for off < int64(len(payload)) {
		end := off + ChunkSize
		if end > int64(len(payload)) {
			end = int64(len(payload))
		}
		if _, _, perr := m.PutChunk(context.Background(), snap.ID, off, payload[off:end]); perr != nil {
			t.Fatalf("上传失败: %v", perr)
		}
		off = end
	}
	if _, cerr := m.CommitLoad(context.Background(), snap.ID, ""); cerr != nil {
		t.Fatalf("commit 失败: %v", cerr)
	}

	final := waitTask(t, m, snap.ID)
	if final.Status != StatusSucceeded {
		t.Fatalf("导入应成功，实际: %s（%s）", final.Status, final.Message)
	}

	// 关键断言：不该等到 2s 超时才清理。这里给 500ms 的宽限，
	// 正常路径应在几毫秒内完成删除。
	deadline := time.Now().Add(500 * time.Millisecond)
	for {
		if m.store.usage() == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("成功后归档未及时删除（仍占 %d 字节），疑似 endRead 自锁", m.store.usage())
		}
		sleepMS(2)
	}
	// 归档文件本身也必须消失。
	if _, statErr := os.Stat(filepath.Join(m.store.root, snap.ID)); !os.IsNotExist(statErr) {
		t.Fatalf("成功后任务目录应被删除，实际: %v", statErr)
	}
}

// TestSaveEnforcesLimitBeforeWriting 验证硬限额在**写入前**生效。
//
// 这是本功能最重要的一条安全性质：
// 异常或恶意的引擎可以无限输出数据，归档落盘量必须**永远不超过**单归档上限，
// 而不是"写到磁盘满了才报错"。
func TestSaveEnforcesLimitBeforeWriting(t *testing.T) {
	const limit = 3 << 20 // 3 MiB，便于在测试里快速触发
	rt := &endlessSaveRuntime{
		fakeTransferRuntime: fakeTransferRuntime{inspectSize: 0}, // 引擎不报大小（模拟不可信）
		closed:              make(chan struct{}),
	}
	m := newTestManager(t, rt)
	m.limits.MaxArchiveBytes = limit
	m.limits.MaxTotalArchiveBytes = limit * 4

	snap, err := m.Save(context.Background(), "huge:1")
	if err != nil {
		t.Fatalf("save 应受理: %v", err)
	}
	final := waitTask(t, m, snap.ID)
	if final.Status != StatusFailed {
		t.Fatalf("无限输出的导出必须失败，实际: %s", final.Status)
	}

	// Terminal status is published before the writer's cleanup completes by design.
	// Wait on the existing lifecycle event, not a sleep or quota polling loop:
	// Status alone is never a resource-release receipt (Release also waits on done).
	m.mu.Lock()
	task := m.tasks[snap.ID]
	m.mu.Unlock()
	if task == nil || !m.waitWritersIdle(task, 5*time.Second) {
		t.Fatal("failed save worker did not finish cleanup")
	}

	// 关键断言：落盘量不超过上限。
	// 任务失败且 writer 已结束后归档已被删除，因此用失败消息与磁盘状态共同判断。
	// 这里直接查磁盘：transfers 下不应留下任何 archive.tar。
	if got := m.store.usage(); got != 0 {
		t.Fatalf("失败后不应留下归档，实际占用 %d 字节", got)
	}

	// 流必须被我们主动关闭，否则引擎侧会一直占着连接与临时文件。
	select {
	case <-rt.closed:
	case <-time.After(5 * time.Second):
		t.Fatalf("超限后必须关闭上游流")
	}
}

// TestLimitWriterNeverWritesBeyondLimit 直接验证 limitWriter 的边界。
//
// 这是最纯粹的单元验证：给出无限源与一个上限，断言写入总量
// **恰好不超过上限**，且错误是 ErrArchiveTooLarge。
func TestLimitWriterNeverWritesBeyondLimit(t *testing.T) {
	const limit = 1000
	var buf bytes.Buffer
	var written int64

	lw := &limitWriter{
		dst:   &buf,
		limit: limit,
		onProgress: func(n int64) {
			written = n
		},
	}
	err := copyWithLimit(lw, &endlessReader{closed: make(chan struct{}), once: &sync.Once{}}, make([]byte, 256))
	if !errors.Is(err, ErrArchiveTooLarge) {
		t.Fatalf("应返回 ErrArchiveTooLarge，实际: %v", err)
	}
	if int64(buf.Len()) > limit {
		t.Fatalf("落盘量 %d 超过上限 %d", buf.Len(), limit)
	}
	if written != int64(buf.Len()) {
		t.Fatalf("进度与实际写入不一致: %d vs %d", written, buf.Len())
	}
}

// TestLimitWriterRespectsTotalBudget 验证总量预算在写入前生效。
func TestLimitWriterRespectsTotalBudget(t *testing.T) {
	const limit = 1 << 20
	var buf bytes.Buffer
	calls := 0

	lw := &limitWriter{
		dst:   &buf,
		limit: limit,
		beforeWrite: func(n int64) error {
			calls++
			// 第二块开始就报配额不足。
			if calls >= 2 {
				return ErrQuotaExceeded
			}
			return nil
		},
	}
	err := copyWithLimit(lw, &endlessReader{closed: make(chan struct{}), once: &sync.Once{}}, make([]byte, 512))
	if !errors.Is(err, ErrQuotaExceeded) {
		t.Fatalf("应返回 ErrQuotaExceeded，实际: %v", err)
	}
	if buf.Len() > 512 {
		t.Fatalf("预算拒绝后不应继续写入，实际写了 %d 字节", buf.Len())
	}
}

// TestSaveRepeatedReservationDoesNotSelfReject 验证任务不会因自己的预留而自我否决。
//
// 曾经写成"把自己也算进已用"，结果任务写第一个字节时就判超配额，
// 导出永远无法完成。正确语义是排除自己。
func TestSaveRepeatedReservationDoesNotSelfReject(t *testing.T) {
	payload := bytes.Repeat([]byte("Z"), 1<<20) // 1 MiB
	rt := &fakeTransferRuntime{
		saveData:    payload,
		inspectSize: int64(len(payload)),
	}
	m := newTestManager(t, rt)
	// 总量上限刚好等于这一份归档：自己占满自己，应当成功。
	m.limits.MaxArchiveBytes = int64(len(payload))
	m.limits.MaxTotalArchiveBytes = int64(len(payload))

	snap, err := m.Save(context.Background(), "app:1")
	if err != nil {
		t.Fatalf("save 应受理: %v", err)
	}
	final := waitTask(t, m, snap.ID)
	if final.Status != StatusSucceeded {
		t.Fatalf("占满自己配额但不超限应成功，实际: %s（%s）", final.Status, final.Message)
	}
	if final.SizeBytes != int64(len(payload)) {
		t.Fatalf("归档大小应为 %d，实际 %d", len(payload), final.SizeBytes)
	}
}

// blockingSaveRuntime 的 SaveImage 返回一个会阻塞的读端，用于验证
// "取消后 worker 仍在写"这种情况下的并发与配额记账。
//
// release 通道关闭时读端返回 io.EOF，让 save 的 goroutine 正常收尾。
type blockingSaveRuntime struct {
	fakeTransferRuntime
	started chan struct{} // SaveImage 被调用时关闭
	release chan struct{} // 关闭后读端返回 EOF
	once    sync.Once
}

func (b *blockingSaveRuntime) SaveImage(ctx context.Context, _ string) (io.ReadCloser, error) {
	b.once.Do(func() { close(b.started) })
	return &blockingReader{ctx: ctx, release: b.release}, nil
}

// blockingReader 在 release 关闭或 ctx 取消后返回 EOF。
type blockingReader struct {
	ctx     context.Context
	release chan struct{}
	done    bool
}

func (r *blockingReader) Read(p []byte) (int, error) {
	if r.done {
		return 0, io.EOF
	}
	select {
	case <-r.release:
		r.done = true
		return 0, io.EOF
	case <-r.ctx.Done():
		r.done = true
		return 0, r.ctx.Err()
	}
}

func (r *blockingReader) Close() error { return nil }

// TestCancelKeepsConcurrencySlotUntilWorkerExits 验证取消后并发位不被立刻让出。
//
// 这是本功能最危险的记账错误：
//
//	任务 A cancel → 状态 canceled → 若并发上限只看状态，会认为空出一位
//	→ 立刻允许 B、C 启动 → 实际同时有 3 个写者在跑，而设计上限是 2。
//
// 磁盘峰值占用因此成倍上升，配额判断也被绕过。
func TestCancelKeepsConcurrencySlotUntilWorkerExits(t *testing.T) {
	rt := &blockingSaveRuntime{
		fakeTransferRuntime: fakeTransferRuntime{inspectSize: 100},
		started:             make(chan struct{}),
		release:             make(chan struct{}),
	}
	m := newTestManager(t, rt)
	m.limits.MaxConcurrentTasks = 1

	snap, err := m.Save(context.Background(), "app:1")
	if err != nil {
		t.Fatalf("save 应受理: %v", err)
	}
	select {
	case <-rt.started:
	case <-time.After(2 * time.Second):
		t.Fatalf("SaveImage 未被调用")
	}

	// 取消：状态会立刻变 canceled。
	if _, _, cerr := m.Cancel(snap.ID); cerr != nil {
		t.Fatalf("取消失败: %v", cerr)
	}
	if s, _ := m.Status(snap.ID); s.Status != StatusCanceled {
		t.Fatalf("取消后状态应为 canceled，实际 %s", s.Status)
	}

	// 关键断言：worker 还在写，此时**不能再起新任务**。
	if _, berr := m.BeginLoad(context.Background(), 10, ""); !errors.Is(berr, ErrTooManyTasks) {
		t.Fatalf("worker 未退出时应拒绝新任务（ErrTooManyTasks），实际: %v", berr)
	}

	close(rt.release)
	waitWorkerExit(t, m, snap.ID)

	if _, berr := m.BeginLoad(context.Background(), 10, ""); berr != nil {
		t.Fatalf("worker 退出后应允许新任务，实际: %v", berr)
	}
}

// TestCancelKeepsQuotaReservedUntilWorkerExits 验证取消后配额不被立刻释放。
func TestCancelKeepsQuotaReservedUntilWorkerExits(t *testing.T) {
	rt := &blockingSaveRuntime{
		fakeTransferRuntime: fakeTransferRuntime{inspectSize: 800},
		started:             make(chan struct{}),
		release:             make(chan struct{}),
	}
	m := newTestManager(t, rt)
	m.limits.MaxTotalArchiveBytes = 1000
	m.limits.MaxArchiveBytes = 1000
	m.limits.MaxConcurrentTasks = 4

	snap, err := m.Save(context.Background(), "app:1")
	if err != nil {
		t.Fatalf("save 应受理: %v", err)
	}
	select {
	case <-rt.started:
	case <-time.After(2 * time.Second):
		t.Fatalf("SaveImage 未被调用")
	}

	if _, _, cerr := m.Cancel(snap.ID); cerr != nil {
		t.Fatalf("取消失败: %v", cerr)
	}

	// 800 + 800 > 1000：worker 未退出时必须拒绝。
	if _, berr := m.BeginLoad(context.Background(), 800, ""); !errors.Is(berr, ErrQuotaExceeded) {
		t.Fatalf("worker 未退出时应因配额拒绝（ErrQuotaExceeded），实际: %v", berr)
	}

	close(rt.release)
	waitWorkerExit(t, m, snap.ID)

	if _, berr := m.BeginLoad(context.Background(), 800, ""); berr != nil {
		t.Fatalf("worker 退出后应允许新任务，实际: %v", berr)
	}
}

// stallSaveRuntime 的 SaveImage 返回一个**忽略 ctx**的读端：
// 只有 release 关闭时才返回数据。
//
// 为什么需要它：blockingReader 会响应 ctx 取消立刻返回 EOF，
// 那样 save 的 goroutine 会马上收尾，"取消时 worker 仍在写"的
// 窗口只有几微秒，无法稳定验证。
// 真实的引擎调用在取消后**也可能迟迟不返回**（HTTP 请求已发出、
// 引擎正在打包镜像），所以"忽略 ctx 的慢 worker"是真实场景，
// 不是人为构造。
type stallSaveRuntime struct {
	fakeTransferRuntime
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (s *stallSaveRuntime) SaveImage(ctx context.Context, _ string) (io.ReadCloser, error) {
	s.once.Do(func() { close(s.started) })
	return &stallReader{release: s.release}, nil
}

// stallReader 完全忽略 ctx：只有 release 关闭才返回数据。
type stallReader struct {
	release chan struct{}
	done    bool
}

func (r *stallReader) Read(p []byte) (int, error) {
	if r.done {
		return 0, io.EOF
	}
	<-r.release
	r.done = true
	return 0, io.EOF
}

func (r *stallReader) Close() error { return nil }

// TestReleaseWaitsForWorkerExit 验证 Release 会等 worker 真正退出。
//
// 不能只检查状态：Cancel 已把状态写成 canceled，而 save goroutine
// 可能还在写盘（引擎的 HTTP 调用不会因为 ctx 取消就立刻返回）。
// 直接 unlink 会让它继续"写成功"到一个不可见的文件，同时提前释放配额。
func TestReleaseWaitsForWorkerExit(t *testing.T) {
	rt := &stallSaveRuntime{
		fakeTransferRuntime: fakeTransferRuntime{inspectSize: 100},
		started:             make(chan struct{}),
		release:             make(chan struct{}),
	}
	m := newTestManager(t, rt)

	snap, err := m.Save(context.Background(), "app:1")
	if err != nil {
		t.Fatalf("save 应受理: %v", err)
	}
	select {
	case <-rt.started:
	case <-time.After(2 * time.Second):
		t.Fatalf("SaveImage 未被调用")
	}
	if _, _, cerr := m.Cancel(snap.ID); cerr != nil {
		t.Fatalf("取消失败: %v", cerr)
	}

	// 确认 worker 确实还在跑（writers > 0），这是本用例的前提。
	m.mu.Lock()
	writers := m.tasks[snap.ID].writers
	m.mu.Unlock()
	if writers == 0 {
		t.Fatalf("前提不成立：取消后 worker 不应立刻退出")
	}

	// 在另一个 goroutine 里释放：它应当阻塞，直到 worker 退出。
	type relResult struct {
		released bool
		msg      string
		err      error
	}
	relCh := make(chan relResult, 1)
	go func() {
		r, msg, rerr := m.Release(snap.ID)
		relCh <- relResult{released: r, msg: msg, err: rerr}
	}()

	// 确认它还没返回（worker 仍阻塞）。
	select {
	case r := <-relCh:
		t.Fatalf("worker 未退出时 Release 不应返回: %+v", r)
	case <-time.After(150 * time.Millisecond):
	}

	// 放开 worker。
	close(rt.release)

	// Release 必须在 worker 退出后成功返回——真正的事件等待，不是 sleep。
	select {
	case r := <-relCh:
		if r.err != nil || !r.released {
			t.Fatalf("worker 退出后 Release 应成功: released=%v msg=%q err=%v", r.released, r.msg, r.err)
		}
	case <-time.After(15 * time.Second):
		t.Fatalf("Release 未在 worker 退出后返回")
	}
}

// TestCancelStalledWorkerKeepsQuota 验证被判 canceled 但 worker 仍在写的任务
// 继续占用配额，使并发任务**无法绕限**。
func TestCancelStalledWorkerKeepsQuota(t *testing.T) {
	rt := &stallSaveRuntime{
		fakeTransferRuntime: fakeTransferRuntime{inspectSize: 800},
		started:             make(chan struct{}),
		release:             make(chan struct{}),
	}
	m := newTestManager(t, rt)
	m.limits.MaxTotalArchiveBytes = 1000
	m.limits.MaxArchiveBytes = 1000
	m.limits.MaxConcurrentTasks = 4

	snap, err := m.Save(context.Background(), "app:1")
	if err != nil {
		t.Fatalf("save 应受理: %v", err)
	}
	select {
	case <-rt.started:
	case <-time.After(2 * time.Second):
		t.Fatalf("SaveImage 未被调用")
	}
	if _, _, cerr := m.Cancel(snap.ID); cerr != nil {
		t.Fatalf("取消失败: %v", cerr)
	}
	// 状态已是 canceled，但 worker 还在写。
	if s, _ := m.Status(snap.ID); s.Status != StatusCanceled {
		t.Fatalf("状态应为 canceled，实际 %s", s.Status)
	}

	// 关键断言：不能让新任务绕过配额。
	if _, berr := m.BeginLoad(context.Background(), 800, ""); !errors.Is(berr, ErrQuotaExceeded) {
		t.Fatalf("worker 未退出时新任务必须因配额被拒，实际: %v", berr)
	}

	close(rt.release)
	waitWorkerExit(t, m, snap.ID)

	if _, berr := m.BeginLoad(context.Background(), 800, ""); berr != nil {
		t.Fatalf("worker 退出后应允许新任务，实际: %v", berr)
	}
}

// waitWorkerExit 等任务的 worker 真正退出（通过 done 通道，不用 sleep）。
//
// 直接读 Manager 内部的 done 通道：这是"操作确实结束"的权威信号，
// 比轮询状态可靠——状态可能已经终态而 worker 还在收尾。
func waitWorkerExit(t *testing.T, m *Manager, taskID string) {
	t.Helper()
	m.mu.Lock()
	task, ok := m.tasks[taskID]
	var done chan struct{}
	if ok {
		done = task.done
	}
	m.mu.Unlock()
	if !ok || done == nil {
		// 任务可能已被 worker 自己清掉，那就确认 writers 归零。
		for i := 0; i < 400; i++ {
			m.mu.Lock()
			var w int32
			if tt, ok2 := m.tasks[taskID]; ok2 {
				w = tt.writers
			}
			m.mu.Unlock()
			if w == 0 {
				return
			}
			sleepMS(5)
		}
		t.Fatalf("任务 %s 的 worker 未退出", taskID)
	}
	select {
	case <-done:
	case <-time.After(15 * time.Second):
		t.Fatalf("任务 %s 的 worker 未在预期时间内退出", taskID)
	}
}

// TestValidTaskIDRejectsPathTraversal 验证 id 校验挡住路径穿越。
func TestValidTaskIDRejectsPathTraversal(t *testing.T) {
	bad := []string{
		"", "..", "../etc", strings.Repeat("a", 31), strings.Repeat("a", 33),
		strings.Repeat("g", 32), "../../" + strings.Repeat("a", 26),
	}
	for _, id := range bad {
		if validTaskID(id) {
			t.Fatalf("非法 id 应被拒绝: %q", id)
		}
	}
	if !validTaskID(strings.Repeat("0123456789abcdef", 2)) {
		t.Fatalf("合法 id 应通过")
	}
}
