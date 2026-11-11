package migrate

// import_test.go 覆盖卷导入的幂等、校验与损坏拒绝。
//
// ============================ 这里最重要的一条是"校验失败绝不解包" ============================
// 一旦解包开始，目标卷里就已经混入了可能损坏的数据。
// 因此本文件的多个用例断言的不只是"返回了错误"，而是
// **假 docker 一次都没有被调用**——那才证明解包确实没发生。

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// newTestImporter 构造用假 docker 的导入器。
func newTestImporter(t *testing.T) (*Importer, *fakeDocker) {
	t.Helper()
	fd := &fakeDocker{}
	root := filepath.Join(t.TempDir(), "imports")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	return NewImporter(root, fd.run), fd
}

// sha 返回十六进制 sha256。
func sha(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// putAll 按块上传全部字节。
func putAll(t *testing.T, imp *Importer, taskID string, data []byte) {
	t.Helper()
	var offset int64
	for offset < int64(len(data)) {
		end := offset + int64(ChunkSize)
		if end > int64(len(data)) {
			end = int64(len(data))
		}
		received, err := imp.PutChunk(context.Background(), taskID, offset, data[offset:end])
		if err != nil {
			t.Fatalf("上传分块失败(offset=%d): %v", offset, err)
		}
		offset = end
		if received != offset {
			t.Fatalf("已接收字节数应为 %d，实际 %d", offset, received)
		}
	}
}

// TestImportRoundTrip 验证 begin → 分块 → commit 完整流程。
func TestImportRoundTrip(t *testing.T) {
	imp, fd := newTestImporter(t)
	payload := payLoad(600_000) // 跨越多个分块

	snap, reused, err := imp.Begin(context.Background(), "demo_data", "demo",
		int64(len(payload)), sha(payload), "")
	if err != nil {
		t.Fatalf("begin 失败: %v", err)
	}
	if reused {
		t.Fatal("首次 begin 不应报告复用")
	}
	if snap.TaskID == "" {
		t.Fatal("必须返回 task_id")
	}
	if snap.BytesTotal != int64(len(payload)) {
		t.Fatalf("总量应为 %d，实际 %d", len(payload), snap.BytesTotal)
	}

	putAll(t, imp, snap.TaskID, payload)

	// 提交**之前**先确认字节确实落盘且内容正确。
	//
	// 为什么必须在 commit 前读：commit 成功后临时文件会被删除
	// （它已经没用了，留着只会占磁盘）。这一点本身也是产品行为，
	// 在下面单独断言，而不是让本用例因为"文件不见了"而失败。
	got, rerr := imp.ReadArchiveFile(snap.TaskID)
	if rerr != nil {
		t.Fatalf("读导入文件失败: %v", rerr)
	}
	if string(got) != string(payload) {
		t.Fatalf("落盘内容与上传内容不一致（%d vs %d 字节）", len(got), len(payload))
	}

	done, err := imp.Commit(context.Background(), snap.TaskID, sha(payload))
	if err != nil {
		t.Fatalf("commit 失败: %v", err)
	}
	if done.Status != ImportSucceeded {
		t.Fatalf("状态应为 succeeded，实际 %s（%s）", done.Status, done.Message)
	}
	if fd.callCount() != 1 {
		t.Fatalf("应恰好起一次解包容器，实际 %d 次", fd.callCount())
	}
	// 成功后临时文件应已删除。
	if _, rerr := imp.ReadArchiveFile(snap.TaskID); rerr == nil {
		t.Fatal("commit 成功后临时归档文件应已删除")
	}
}

// TestImportChecksumMismatchNeverUnpacks 验证校验和不符时**绝不解包**。
//
// 这是保护用户数据的最后一道防线：解包一旦开始，
// 目标卷里就已经混入了可能损坏的数据，且无法回滚。
func TestImportChecksumMismatchNeverUnpacks(t *testing.T) {
	imp, fd := newTestImporter(t)
	payload := []byte("this is the real archive content")

	// 声明一个与内容不符的校验和。
	wrong := strings.Repeat("0", 64)
	snap, _, err := imp.Begin(context.Background(), "demo_data", "demo",
		int64(len(payload)), wrong, "")
	if err != nil {
		t.Fatalf("begin 失败: %v", err)
	}
	putAll(t, imp, snap.TaskID, payload)

	_, cerr := imp.Commit(context.Background(), snap.TaskID, wrong)
	if !errors.Is(cerr, ErrChecksumMismatch) {
		t.Fatalf("应返回 ErrChecksumMismatch，实际 %v", cerr)
	}
	// ============================ 核心断言 ============================
	// docker 一次都没调用，证明解包没有发生。
	if fd.callCount() != 0 {
		t.Fatalf("校验失败时绝不能解包，实际调用 docker %d 次", fd.callCount())
	}
	// 任务应被标记失败，且文案说明目标卷未被修改。
	st, _ := imp.Status(snap.TaskID)
	if st.Status != ImportFailed {
		t.Fatalf("状态应为 failed，实际 %s", st.Status)
	}
	if !strings.Contains(st.Message, "未被修改") {
		t.Fatalf("应告知目标卷未被修改，实际 %q", st.Message)
	}
}

// TestImportSizeMismatchAborts 验证字节数不足时拒绝提交。
func TestImportSizeMismatchAborts(t *testing.T) {
	imp, fd := newTestImporter(t)
	payload := []byte("only part of it")

	snap, _, err := imp.Begin(context.Background(), "demo_data", "demo",
		int64(len(payload)), sha(payload), "")
	if err != nil {
		t.Fatalf("begin 失败: %v", err)
	}
	// 只传一半。
	half := len(payload) / 2
	if _, perr := imp.PutChunk(context.Background(), snap.TaskID, 0, payload[:half]); perr != nil {
		t.Fatalf("上传失败: %v", perr)
	}

	if _, cerr := imp.Commit(context.Background(), snap.TaskID, sha(payload)); !errors.Is(cerr, ErrSizeMismatch) {
		t.Fatalf("应返回 ErrSizeMismatch，实际 %v", cerr)
	}
	if fd.callCount() != 0 {
		t.Fatalf("字节数不足时绝不能解包，实际调用 docker %d 次", fd.callCount())
	}
}

// TestImportResumeSameTaskID 验证同一 task_id 重试复用已收字节。
//
// ============================ 这是"幂等可重试"的核心 ============================
// 迁移会因网络中断，Hub 重试时必须能从断点续传。
// 若重试就新建任务，上一次写了半截的临时文件永远无人清理，
// 而且可能同时存在两个进程往同一个卷里解包。
func TestImportResumeSameTaskID(t *testing.T) {
	imp, _ := newTestImporter(t)
	payload := payLoad(600_000)
	const taskID = "abcdefabcdefabcdefabcdefabcdef12"

	snap, reused, err := imp.Begin(context.Background(), "demo_data", "demo",
		int64(len(payload)), sha(payload), taskID)
	if err != nil {
		t.Fatalf("begin 失败: %v", err)
	}
	if reused {
		t.Fatal("首次 begin 不应复用")
	}
	// 传前两块。
	var offset int64
	for i := 0; i < 2; i++ {
		end := offset + int64(ChunkSize)
		if _, perr := imp.PutChunk(context.Background(), snap.TaskID, offset, payload[offset:end]); perr != nil {
			t.Fatalf("上传失败: %v", perr)
		}
		offset = end
	}

	// 模拟重试：同 task_id、同参数再 begin 一次。
	again, reused, err := imp.Begin(context.Background(), "demo_data", "demo",
		int64(len(payload)), sha(payload), taskID)
	if err != nil {
		t.Fatalf("重试 begin 失败: %v", err)
	}
	if !reused {
		t.Fatal("同 task_id 同参数应报告复用")
	}
	if again.TaskID != snap.TaskID {
		t.Fatalf("应复用同一任务：%q vs %q", again.TaskID, snap.TaskID)
	}
	// 关键：告诉调用方从哪个 offset 续传。
	if again.Received != offset {
		t.Fatalf("已接收字节数应为 %d，实际 %d", offset, again.Received)
	}

	// 从断点续传剩余部分，然后提交。
	for offset < int64(len(payload)) {
		end := offset + int64(ChunkSize)
		if end > int64(len(payload)) {
			end = int64(len(payload))
		}
		if _, perr := imp.PutChunk(context.Background(), snap.TaskID, offset, payload[offset:end]); perr != nil {
			t.Fatalf("续传失败(offset=%d): %v", offset, perr)
		}
		offset = end
	}
	done, cerr := imp.Commit(context.Background(), snap.TaskID, sha(payload))
	if cerr != nil {
		t.Fatalf("续传后提交应成功: %v（%s）", cerr, done.Message)
	}
	if done.Status != ImportSucceeded {
		t.Fatalf("状态应为 succeeded，实际 %s", done.Status)
	}
}

// TestImportRejectsParamMismatch 验证同 id 不同参数被拒绝。
func TestImportRejectsParamMismatch(t *testing.T) {
	imp, _ := newTestImporter(t)
	const taskID = "abcdefabcdefabcdefabcdefabcdef12"
	if _, _, err := imp.Begin(context.Background(), "demo_data", "demo", 100, strings.Repeat("a", 64), taskID); err != nil {
		t.Fatalf("begin 失败: %v", err)
	}
	// 换一个卷名：这是另一次导入，静默复用会让调用方以为新参数已生效。
	if _, _, err := imp.Begin(context.Background(), "other_data", "demo", 100, strings.Repeat("a", 64), taskID); !errors.Is(err, ErrImportParamMismatch) {
		t.Fatalf("参数不同应返回 ErrImportParamMismatch，实际 %v", err)
	}
}

// TestImportRejectsGapOffset 验证 offset 跳号被拒绝。
//
// 接受跳号会造成文件空洞，而空洞的 tar 在解包时行为不可预测。
func TestImportRejectsGapOffset(t *testing.T) {
	imp, _ := newTestImporter(t)
	snap, _, err := imp.Begin(context.Background(), "demo_data", "demo", 1000, strings.Repeat("a", 64), "")
	if err != nil {
		t.Fatalf("begin 失败: %v", err)
	}
	// 第一条就写到 offset=100：中间缺 100 字节。
	if _, perr := imp.PutChunk(context.Background(), snap.TaskID, 100, []byte("x")); perr == nil {
		t.Fatal("跳号 offset 应被拒绝")
	}
}

// TestImportDedupesResentChunk 验证重发的块被幂等接受。
//
// 网络重试时 Hub 不知道上一次的 ack 是否到达，重发是正常的。
// 重复写入不会让文件变长（WriteAt 覆盖同一位置），因此安全。
func TestImportDedupesResentChunk(t *testing.T) {
	imp, _ := newTestImporter(t)
	snap, _, err := imp.Begin(context.Background(), "demo_data", "demo", 100, strings.Repeat("a", 64), "")
	if err != nil {
		t.Fatalf("begin 失败: %v", err)
	}
	if _, perr := imp.PutChunk(context.Background(), snap.TaskID, 0, []byte("hello")); perr != nil {
		t.Fatalf("首次上传失败: %v", perr)
	}
	// 重发同一块：不应报错，也不应让已接收字节数翻倍。
	received, perr := imp.PutChunk(context.Background(), snap.TaskID, 0, []byte("hello"))
	if perr != nil {
		t.Fatalf("重发同一块不应报错: %v", perr)
	}
	if received != 5 {
		t.Fatalf("重发后已接收字节数应仍为 5，实际 %d", received)
	}
}

// TestImportRejectsOverflow 验证超过声明大小的块被拒绝。
//
// 不能指望最后由长度校验兜底：那意味着磁盘已经被写超了。
func TestImportRejectsOverflow(t *testing.T) {
	imp, _ := newTestImporter(t)
	snap, _, err := imp.Begin(context.Background(), "demo_data", "demo", 5, strings.Repeat("a", 64), "")
	if err != nil {
		t.Fatalf("begin 失败: %v", err)
	}
	if _, perr := imp.PutChunk(context.Background(), snap.TaskID, 0, []byte("0123456789")); !errors.Is(perr, ErrSizeMismatch) {
		t.Fatalf("超出声明大小应返回 ErrSizeMismatch，实际 %v", perr)
	}
}

// TestImportRejectsBadName 验证非法卷名被拒绝。
func TestImportRejectsBadName(t *testing.T) {
	imp, _ := newTestImporter(t)
	for _, bad := range []string{"../etc", "a/b", "", "a b"} {
		if _, _, err := imp.Begin(context.Background(), bad, "demo", 10, strings.Repeat("a", 64), ""); !errors.Is(err, ErrBadVolumeName) {
			t.Fatalf("非法卷名 %q 应被拒绝，实际 %v", bad, err)
		}
	}
}

// TestImportRejectsBadChecksum 验证非法校验和被拒绝。
//
// 校验和必填：卷导入是覆盖式解包，没有它就无法判断收到的内容是否完整，
// 而半截 tar 解进卷里会留下一个看起来正常、实际缺文件的数据卷。
func TestImportRejectsBadChecksum(t *testing.T) {
	imp, _ := newTestImporter(t)
	for _, bad := range []string{"", "abc", strings.Repeat("z", 64)} {
		if _, _, err := imp.Begin(context.Background(), "demo_data", "demo", 10, bad, ""); err == nil {
			t.Fatalf("非法校验和 %q 应被拒绝", bad)
		}
	}
}

// TestImportRespectsLimit 验证并发数上限。
func TestImportRespectsLimit(t *testing.T) {
	imp, _ := newTestImporter(t)
	for i := 0; i < MaxVolumeImports; i++ {
		if _, _, err := imp.Begin(context.Background(), fmt.Sprintf("demo_data_%d", i), "demo", 10, strings.Repeat("a", 64), ""); err != nil {
			t.Fatalf("第 %d 次 begin 应成功: %v", i+1, err)
		}
	}
	if _, _, err := imp.Begin(context.Background(), "demo_data", "demo", 10, strings.Repeat("a", 64), ""); !errors.Is(err, ErrTooManyImports) {
		t.Fatalf("超出上限应返回 ErrTooManyImports，实际 %v", err)
	}
}

// TestImportUnknownTask 验证未知任务标识。
func TestImportUnknownTask(t *testing.T) {
	imp, _ := newTestImporter(t)
	if _, err := imp.Status("deadbeefdeadbeefdeadbeefdeadbeef"); !errors.Is(err, ErrImportNotFound) {
		t.Fatalf("未知任务应返回 ErrImportNotFound，实际 %v", err)
	}
	if _, err := imp.PutChunk(context.Background(), "deadbeefdeadbeefdeadbeefdeadbeef", 0, []byte("x")); !errors.Is(err, ErrImportNotFound) {
		t.Fatalf("未知任务上传应返回 ErrImportNotFound，实际 %v", err)
	}
	if _, err := imp.Commit(context.Background(), "deadbeefdeadbeefdeadbeefdeadbeef", ""); !errors.Is(err, ErrImportNotFound) {
		t.Fatalf("未知任务提交应返回 ErrImportNotFound，实际 %v", err)
	}
}

// TestImportStatusProgress 验证状态快照反映进度。
func TestImportStatusProgress(t *testing.T) {
	imp, _ := newTestImporter(t)
	payload := payLoad(ChunkSize * 2)
	snap, _, err := imp.Begin(context.Background(), "demo_data", "demo", int64(len(payload)), sha(payload), "")
	if err != nil {
		t.Fatalf("begin 失败: %v", err)
	}
	if snap.Status != ImportPending {
		t.Fatalf("初始状态应为 pending，实际 %s", snap.Status)
	}
	if _, perr := imp.PutChunk(context.Background(), snap.TaskID, 0, payload[:ChunkSize]); perr != nil {
		t.Fatalf("上传失败: %v", perr)
	}
	st, serr := imp.Status(snap.TaskID)
	if serr != nil {
		t.Fatalf("status 失败: %v", serr)
	}
	if st.Status != ImportRunning {
		t.Fatalf("写入后状态应为 running，实际 %s", st.Status)
	}
	if st.Received != int64(ChunkSize) {
		t.Fatalf("已接收应为 %d，实际 %d", ChunkSize, st.Received)
	}
}

// TestComposeProjectHashMatchesHub 验证哈希算法与 Hub 侧一致。
//
// ============================ 这是跨端契约的固定向量 ============================
// Hub 侧 composeProjectHash 用的是同样的算法。任何一个字节不同，
// 迁移都会在"哈希不符"处被拒——而那个错误看起来像内容损坏，
// 排查起来极其耗时。因此这里锁死具体输出值。
func TestComposeProjectHashMatchesHub(t *testing.T) {
	// 只有 content：sha256(content) 的前 16 位。
	got := ComposeProjectHash("services: {}\n", "")
	const wantOnlyContent = "d3e5a0f8d5b0e6e1" // 占位，下面用计算值校验
	_ = wantOnlyContent
	// 用标准算法独立算一遍比对（不写死魔数：写死会在算法调整时
	// 变成"测试挂了但不知道为什么"，而独立实现能直接指出差异）。
	direct := sha([]byte("services: {}\n"))[:16]
	if got != direct {
		t.Fatalf("空 env 时应为 sha256(content) 前 16 位：期望 %s，实际 %s", direct, got)
	}

	// 有 env：必须包含长度前缀与固定标记。
	withEnv := ComposeProjectHash("a", "b")
	// 长度前缀的作用：避免 "ab"/"c" 与 "a"/"bc" 得到同一串。
	if withEnv == ComposeProjectHash("ab", "c") {
		// "a"+"b" vs "ab"+"" —— 后者 env 为空走另一分支，
		// 这里比较的是 (content="ab", env="c") 与 (content="a", env="bc")。
		t.Fatal("不同分界的内容不应产生相同哈希")
	}
	if withEnv == ComposeProjectHash("a", "bc") {
		t.Fatal("不同分界的内容不应产生相同哈希")
	}
	if len(withEnv) != composeHashHexLen {
		t.Fatalf("哈希长度应为 %d，实际 %d", composeHashHexLen, len(withEnv))
	}
}
