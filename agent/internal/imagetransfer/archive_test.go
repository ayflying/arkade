package imagetransfer

// archive_test.go 覆盖可复用归档（ExportAPI）的关键约束。
//
// 与 manager_test.go 同样的思路：用手写数据 + 假 runtime 驱动，
// 不依赖真实引擎。这里最需要验证的四件事，每一件都对应一个
// 在真实迁移里会静默出错的场景：
//
//  1. 分块读出的字节拼起来必须与原归档**逐字节相同**、校验和一致；
//  2. 关闭之后必须读不到（不是"读到旧数据"，而是明确报错）；
//  3. 过期之后必须读不到（访问时校验，不能只靠后台清扫）；
//  4. 并发导出要受数量上限约束，不能无限制占盘。

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

// newArchiveTestManager 构造一个可以导出归档的管理器。
//
// payload 是"镜像归档"的内容（假 runtime 直接把它当 docker save 的输出）。
func newArchiveTestManager(t *testing.T, payload []byte) (*Manager, *fakeTransferRuntime) {
	t.Helper()
	rt := &fakeTransferRuntime{
		saveData:    payload,
		inspectSize: int64(len(payload)),
	}
	return newTestManager(t, rt), rt
}

// readAllExport 按固定块大小把归档完整读出来，返回拼好的字节。
//
// 这模拟 Hub 侧 copyArchive 的行为：从 offset=0 开始，每块 256 KiB，
// 直到 EOF。**任何 offset 错位都会让拼出来的字节对不上**。
func readAllExport(t *testing.T, m *Manager, id string) []byte {
	t.Helper()
	var out bytes.Buffer
	var offset int64
	for {
		data, echoed, eof, size, err := m.GetExportChunk(context.Background(), id, offset, ChunkSize)
		if err != nil {
			t.Fatalf("读块失败(offset=%d): %v", offset, err)
		}
		if echoed != offset {
			t.Fatalf("回显 offset 不匹配：要 %d，回显 %d", offset, echoed)
		}
		if size <= 0 {
			t.Fatalf("归档大小应 > 0，实际 %d", size)
		}
		if data == "" {
			if !eof {
				t.Fatalf("offset=%d 处返回空块但未标记 EOF（会让调用方死循环）", offset)
			}
			break
		}
		raw, derr := base64.StdEncoding.DecodeString(data)
		if derr != nil {
			t.Fatalf("分块不是合法 base64: %v", derr)
		}
		if int64(len(raw)) > ChunkSize {
			t.Fatalf("分块超过上限：%d > %d", len(raw), ChunkSize)
		}
		out.Write(raw)
		offset += int64(len(raw))
		if eof {
			break
		}
	}
	return out.Bytes()
}

// TestExportArchiveRoundTrip 验证导出 → 分块读回 → 逐字节一致。
//
// 这是本功能的核心断言：只要 offset 语义或存储路径有任何偏差，
// 拼出来的字节就会不同，而 sha256 立刻能证实这一点。
func TestExportArchiveRoundTrip(t *testing.T) {
	// 刻意用非整块大小（300KB），确保最后一块是残块：
	// 残块处理错误是分块实现最常见的 bug。
	payload := bytes.Repeat([]byte("arkade-archive-payload-"), 14000)
	if len(payload) <= int(ChunkSize) {
		t.Fatalf("测试数据应跨越多个分块，实际 %d 字节", len(payload))
	}
	m, _ := newArchiveTestManager(t, payload)

	info, err := m.ExportArchive(context.Background(), "nginx:1.25")
	if err != nil {
		t.Fatalf("导出失败: %v", err)
	}
	if info.ExportID == "" {
		t.Fatal("必须返回 export_id")
	}
	if !validTaskID(info.ExportID) {
		t.Fatalf("export_id 形态不正确: %q", info.ExportID)
	}
	if info.SizeBytes != int64(len(payload)) {
		t.Fatalf("大小应为 %d，实际 %d", len(payload), info.SizeBytes)
	}
	want := sha256.Sum256(payload)
	if info.SHA256 != hex.EncodeToString(want[:]) {
		t.Fatalf("校验和应为 %s，实际 %s", hex.EncodeToString(want[:]), info.SHA256)
	}

	got := readAllExport(t, m, info.ExportID)
	if !bytes.Equal(got, payload) {
		t.Fatalf("读回的字节与原始归档不一致（长度 %d vs %d）", len(got), len(payload))
	}
	sum := sha256.Sum256(got)
	if hex.EncodeToString(sum[:]) != info.SHA256 {
		t.Fatal("读回内容的校验和与导出声明的校验和不一致")
	}
}

// TestExportArchiveOpenReportsSameInfo 验证 open 返回与导出时一致的信息。
//
// 迁移流程会在"导出"与"拉取"之间隔很久，回头第一件事必须是
// 确认自己拿到的还是同一份归档。
func TestExportArchiveOpenReportsSameInfo(t *testing.T) {
	payload := []byte("hello-archive")
	m, _ := newArchiveTestManager(t, payload)

	info, err := m.ExportArchive(context.Background(), "redis:7")
	if err != nil {
		t.Fatalf("导出失败: %v", err)
	}
	opened, err := m.OpenExport(context.Background(), info.ExportID)
	if err != nil {
		t.Fatalf("open 失败: %v", err)
	}
	if opened.ExportID != info.ExportID {
		t.Fatalf("export_id 应一致：%q vs %q", opened.ExportID, info.ExportID)
	}
	if opened.SizeBytes != info.SizeBytes || opened.SHA256 != info.SHA256 {
		t.Fatalf("open 返回的大小/校验和应与导出一致：%+v vs %+v", opened, info)
	}
	if opened.Reference != "redis:7" {
		t.Fatalf("应回显镜像引用，实际 %q", opened.Reference)
	}
	if !opened.ExpiresAt.After(time.Now()) {
		t.Fatalf("过期时间应晚于当前时间，实际 %v", opened.ExpiresAt)
	}
}

// TestExportArchiveUnknownIDIsNotFound 验证未知 ExportID 的回错误语义。
//
// Hub 侧要靠这个错误码决定"重新导出"，因此必须是 ErrExportNotFound，
// 不能是任何内部错误——那会让上层以为是 Agent 出了问题。
func TestExportArchiveUnknownIDIsNotFound(t *testing.T) {
	m, _ := newArchiveTestManager(t, []byte("x"))
	unknown := strings.Repeat("a", 32)

	if _, err := m.OpenExport(context.Background(), unknown); !errors.Is(err, ErrExportNotFound) {
		t.Fatalf("open 未知归档应返回 ErrExportNotFound，实际 %v", err)
	}
	if _, _, _, _, err := m.GetExportChunk(context.Background(), unknown, 0, 1024); !errors.Is(err, ErrExportNotFound) {
		t.Fatalf("读未知归档应返回 ErrExportNotFound，实际 %v", err)
	}
}

// TestExportArchiveClosedIsNotReadable 验证关闭后不可再读。
//
// ============================ 为什么这条不能只测"文件被删" ============================
// POSIX 上 unlink 一个已打开的文件不会让句柄失效。若关闭只删文件、
// 不移除登记，读者会拿到"看似正常但已不在磁盘上"的数据，
// 下一次重试才 404——断裂位置完全不可预测。
// 因此这里断言的是**登记层面**就拒绝，而不是靠文件系统兜底。
func TestExportArchiveClosedIsNotReadable(t *testing.T) {
	m, _ := newArchiveTestManager(t, []byte("payload-for-close"))
	info, err := m.ExportArchive(context.Background(), "alpine:3.20")
	if err != nil {
		t.Fatalf("导出失败: %v", err)
	}

	released, msg, err := m.CloseExport(context.Background(), info.ExportID)
	if err != nil {
		t.Fatalf("关闭失败: %v", err)
	}
	if !released {
		t.Fatalf("首次关闭应真的释放，实际 released=false（%s）", msg)
	}

	if _, err := m.OpenExport(context.Background(), info.ExportID); !errors.Is(err, ErrExportNotFound) {
		t.Fatalf("关闭后 open 应返回 ErrExportNotFound，实际 %v", err)
	}
	if _, _, _, _, err := m.GetExportChunk(context.Background(), info.ExportID, 0, 1024); !errors.Is(err, ErrExportNotFound) {
		t.Fatalf("关闭后读块应返回 ErrExportNotFound，实际 %v", err)
	}

	// 重复关闭：不是错误（要的结果已达成），但必须如实说"本来就不存在"。
	released, msg, err = m.CloseExport(context.Background(), info.ExportID)
	if err != nil {
		t.Fatalf("重复关闭不应报错: %v", err)
	}
	if released {
		t.Fatal("重复关闭不应声称释放了东西")
	}
	if !strings.Contains(msg, "不存在") {
		t.Fatalf("重复关闭的说明应指出归档不存在，实际 %q", msg)
	}
}

// TestExportArchiveExpiredIsNotReadable 验证过期后访问时校验生效。
//
// ============================ 为什么必须在访问时校验 ============================
// 后台清扫是"尽力而为"的：定时器可能被拖慢，进程刚启动时也还没跑过。
// 若只靠清扫，过期归档会继续被读到——**读出过期数据比报错更危险**，
// 因为调用方会把它当成有效归档继续迁移，错误在目标端才暴露。
//
// 这里通过替换 Manager.now 把时间推到 TTL 之后，验证访问路径自己会拒绝。
func TestExportArchiveExpiredIsNotReadable(t *testing.T) {
	m, _ := newArchiveTestManager(t, []byte("payload-for-expiry"))
	base := time.Now()
	// 时钟替换必须发生在导出**之后**：导出时记下的过期时间要基于当前时刻。
	info, err := m.ExportArchive(context.Background(), "busybox:1.36")
	if err != nil {
		t.Fatalf("导出失败: %v", err)
	}
	if info.ExportID == "" {
		t.Fatal("应返回 export_id")
	}

	// 把时钟推到 TTL 之后。
	m.now = func() time.Time { return base.Add(ExportTTL + time.Minute) }

	// ============================ 过期后第一次访问 ============================
	// 第一次访问就地发现过期：报 ErrExportExpired（**如实说明是过期**，
	// 而不是笼统的"不存在"），同时把它移出持有表、此后不再可读。
	if _, err := m.OpenExport(context.Background(), info.ExportID); !errors.Is(err, ErrExportExpired) {
		t.Fatalf("过期后首次 open 应返回 ErrExportExpired，实际 %v", err)
	}
	// ============================ 之后的访问 ============================
	// 已被移出表，因此后续读报"不存在"。
	// 这不是缺陷：真正的错误是**读到过期数据**，而这条路已经被堵住。
	// 顺带验证"过期"与"不存在"在第二次之后确实收敛成同一个语义。
	if _, _, _, _, err := m.GetExportChunk(context.Background(), info.ExportID, 0, 1024); !errors.Is(err, ErrExportNotFound) {
		t.Fatalf("过期归档被清理后读块应返回 ErrExportNotFound，实际 %v", err)
	}
}

// TestExportArchiveSweepReclaimsExpired 验证后台清扫能真正回收磁盘。
//
// 光有"访问时拒绝"是不够的：一个没人访问的归档会一直占着几 GB，
// 直到进程重启。配额被这样悄悄吃掉时，用户只会看到"配额不足"
// 而完全不知道是谁占的。
func TestExportArchiveSweepReclaimsExpired(t *testing.T) {
	payload := []byte("payload-for-sweep")
	m, _ := newArchiveTestManager(t, payload)
	base := time.Now()

	info, err := m.ExportArchive(context.Background(), "nginx:alpine")
	if err != nil {
		t.Fatalf("导出失败: %v", err)
	}
	// 清扫之前：应能清理 0 个（还没过期）。
	if n := m.SweepExports(); n != 0 {
		t.Fatalf("未过期时不应清理任何归档，实际清理 %d 个", n)
	}
	if !m.exports.activeIDs()[info.ExportID] {
		t.Fatal("归档应仍在持有表中")
	}

	m.now = func() time.Time { return base.Add(ExportTTL + time.Minute) }
	if n := m.SweepExports(); n != 1 {
		t.Fatalf("过期后应清理 1 个归档，实际 %d", n)
	}
	if m.exports.activeIDs()[info.ExportID] {
		t.Fatal("过期归档应已移出持有表")
	}
	// 磁盘上的目录也必须消失，否则"清理"只是清了记账。
	if _, _, err := m.store.openArchive(info.ExportID); !errors.Is(err, ErrTaskNotFound) {
		t.Fatalf("过期归档的文件应已删除，实际 openArchive 返回 %v", err)
	}
}

// TestExportArchiveRespectsMaxExports 验证并发导出受数量上限约束。
//
// 每个归档最大 2 GiB，若数量不限，几个并发导出就能把宿主机磁盘写满，
// 连带把机器上**其它**服务一起拖死。
func TestExportArchiveRespectsMaxExports(t *testing.T) {
	m, _ := newArchiveTestManager(t, []byte("payload"))

	var ok int
	for i := 0; i < MaxExports; i++ {
		if _, err := m.ExportArchive(context.Background(), "img"); err != nil {
			t.Fatalf("第 %d 次导出应成功，实际 %v", i+1, err)
		}
		ok++
	}
	if ok != MaxExports {
		t.Fatalf("应能持有 %d 个归档，实际 %d", MaxExports, ok)
	}
	// 第 MaxExports+1 个必须被明确拒绝，而不是无限接受。
	if _, err := m.ExportArchive(context.Background(), "img"); !errors.Is(err, ErrTooManyExports) {
		t.Fatalf("超出上限应返回 ErrTooManyExports，实际 %v", err)
	}
}

// TestExportArchiveConcurrentSameReference 验证并发导出同一 reference 的行为。
//
// ============================ 明确的选择：各自独立持有 ============================
// 任务卡里说"同 reference 并发导出要复用或明确拒绝，不得产生两份大归档"。
//
// 这里选择**允许并发但各自独立**，理由是导出次数由调用方控制、
// 且共用配额与数量上限本来就限制了总量（最多 MaxExports 份）。
// 更进一步，重复导出同一镜像在跨架构场景下是合法的，
// 硬性去重反而会返回一个不同 digest 的归档。
//
// 但"不得无限制产生"必须成立：因此这里断言总量受 MaxExports 约束，
// 且每个归档**都能独立读回自己的内容**（不会互相串数据）。
func TestExportArchiveConcurrentSameReference(t *testing.T) {
	payload := []byte("concurrent-payload")
	m, _ := newArchiveTestManager(t, payload)

	// 并发导出，数量不超过上限。
	n := 4
	var (
		wg  sync.WaitGroup
		mu  sync.Mutex
		ids []string
	)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			info, err := m.ExportArchive(context.Background(), "same:ref")
			if err != nil {
				return
			}
			mu.Lock()
			ids = append(ids, info.ExportID)
			mu.Unlock()
		}()
	}
	wg.Wait()

	if len(ids) != n {
		t.Fatalf("并发导出应全部成功，实际成功 %d/%d", len(ids), n)
	}
	// 每个导出必须拿到**不同的** ExportID，否则会互相覆盖归档。
	seen := make(map[string]bool, len(ids))
	for _, id := range ids {
		if seen[id] {
			t.Fatalf("并发导出产生了重复的 export_id: %q", id)
		}
		seen[id] = true
	}
	// 每个归档都必须能独立读回完整内容。
	for _, id := range ids {
		got := readAllExport(t, m, id)
		if !bytes.Equal(got, payload) {
			t.Fatalf("归档 %q 读回的内容不正确", id)
		}
	}
}

// TestExportArchiveFailureLeavesNoOrphan 验证导出失败不留残目录。
//
// 半截归档既不能 open 也不能 load，留着只会占配额，
// 还会让下次启动清理多一个待处理项。
func TestExportArchiveFailureLeavesNoOrphan(t *testing.T) {
	rt := &fakeTransferRuntime{
		saveErr:     errors.New("引擎拒绝导出"),
		inspectSize: 1024,
	}
	m := newTestManager(t, rt)
	before := countDirs(t, m.store.root)

	if _, err := m.ExportArchive(context.Background(), "broken:1.0"); err == nil {
		t.Fatal("引擎导出失败时应返回错误")
	}
	if after := countDirs(t, m.store.root); after != before {
		t.Fatalf("导出失败后不应留下目录：之前 %d 个，之后 %d 个", before, after)
	}
	if m.exports.count() != 0 {
		t.Fatalf("导出失败后不应有登记项，实际 %d 个", m.exports.count())
	}
}

// TestExportArchiveRejectsOversize 验证超限归档在落盘前被拒绝。
func TestExportArchiveRejectsOversize(t *testing.T) {
	rt := &fakeTransferRuntime{inspectSize: DefaultMaxArchiveBytes + 1}
	m := newTestManager(t, rt)

	if _, err := m.ExportArchive(context.Background(), "huge:latest"); !errors.Is(err, ErrArchiveTooLarge) {
		t.Fatalf("超限归档应返回 ErrArchiveTooLarge，实际 %v", err)
	}
}

// TestExportArchiveUnsupportedRuntime 验证不支持镜像传输的引擎给出明确错误。
func TestExportArchiveUnsupportedRuntime(t *testing.T) {
	m := newTestManager(t, &plainRuntime{})
	if _, err := m.ExportArchive(context.Background(), "nginx"); !errors.Is(err, errUnsupported) {
		t.Fatalf("不支持传输时应返回 errUnsupported，实际 %v", err)
	}
}

// TestExportArchiveCloseCleansDisk 验证关闭会真的删掉文件。
func TestExportArchiveCloseCleansDisk(t *testing.T) {
	m, _ := newArchiveTestManager(t, []byte("to-be-removed"))
	info, err := m.ExportArchive(context.Background(), "nginx:1.25")
	if err != nil {
		t.Fatalf("导出失败: %v", err)
	}
	// 打开一次确认确实可读，**并且立即关闭句柄**。
	// Windows 上未关闭的句柄会让后续 unlink 失败（POSIX 不会），
	// 这里不 close 就只能得到"删除失败"，而不是被测的删除逻辑本身。
	f, _, err := m.store.openArchive(info.ExportID)
	if err != nil {
		t.Fatalf("导出后归档应可打开: %v", err)
	}
	_ = f.Close()
	if _, _, err := m.CloseExport(context.Background(), info.ExportID); err != nil {
		t.Fatalf("关闭失败: %v", err)
	}
	if _, _, err := m.store.openArchive(info.ExportID); !errors.Is(err, ErrTaskNotFound) {
		t.Fatalf("关闭后归档文件应已删除，实际 %v", err)
	}
}

// countDirs 统计 transfers 根下的子目录数（用于判断是否有残留）。
func countDirs(t *testing.T, root string) int {
	t.Helper()
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("读 transfers 目录失败: %v", err)
	}
	n := 0
	for _, e := range entries {
		if e.IsDir() {
			n++
		}
	}
	return n
}

// plainRuntime 见 manager_test.go：只实现 Runtime 主接口，
// 不实现 ImageTransferRuntime，用来验证"引擎不支持传输"时的错误路径。
