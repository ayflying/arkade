package migrate

// archive_test.go 覆盖卷归档的导出、读取、释放与过期。
//
// 全部用**注入的假 docker** 驱动，不起真实容器：这里要验证的是
// 归档生命周期与完整性规则，那些与 docker 无关；
// 真实 docker 的行为由 compose 包既有的容器测试覆盖。

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeDocker 返回可控的 docker 执行器。
//
// payload 是"卷被打成的 tar"的内容（假 docker 直接把它写到目标路径）。
type fakeDocker struct {
	mu      sync.Mutex
	payload []byte
	err     error
	calls   [][]string
	owners  map[string]string
}

func (f *fakeDocker) run(ctx context.Context, args ...string) ([]byte, error) {
	if len(args) > 0 && args[0] == "volume" {
		f.mu.Lock()
		defer f.mu.Unlock()
		if args[1] == "create" {
			if f.owners == nil {
				f.owners = map[string]string{}
			}
			f.owners[args[len(args)-1]] = strings.TrimPrefix(args[3], importOwnerLabel+"=")
			return nil, nil
		}
		if args[1] == "inspect" {
			name := args[2]
			if owner := f.owners[name]; owner != "" {
				return []byte(fmt.Sprintf(`[{"Name":%q,"Labels":{%q:%q}}]`, name, importOwnerLabel, owner)), nil
			}
		}
		return nil, nil
	}
	if len(args) > 0 && args[0] == "inspect" {
		return []byte(strings.TrimPrefix(args[len(args)-1], "arkade-migrate-")), nil
	}
	if len(args) > 0 && args[0] == "rm" {
		return nil, nil
	}
	f.mu.Lock()
	f.calls = append(f.calls, args)
	f.mu.Unlock()
	if f.err != nil {
		return nil, f.err
	}

	// ============================ 必须翻译容器内路径 ============================
	// 真实的 docker run 里 tar 看到的是**容器内**路径（/backup/volume.tar），
	// 它由 "-v <宿主目录>:/backup" 映射到宿主机。
	// 假 docker 若直接按字面路径写文件，会写到 C:\backup\volume.tar，
	// 于是"归档文件不存在"——测试失败的原因看起来像产品 bug，其实是替身没模拟挂载。
	//
	// 因此这里先把 -v 的映射关系解析出来，再把容器内路径翻译回宿主路径。
	mounts := map[string]string{}
	for i := 0; i+1 < len(args); i++ {
		if args[i] != "-v" {
			continue
		}
		// ============================ 不能用 Split(":") ============================
		// Windows 宿主路径本身含冒号（C:\...），一刀切会把
		// "C:\tmp\x:/backup" 切成三段，宿主路径随之丢失。
		// 这里从右往左找：最后一段是模式（:ro），倒数第二段才是容器内路径。
		spec := args[i+1]
		spec = strings.TrimSuffix(spec, ":ro")
		spec = strings.TrimSuffix(spec, ":rw")
		idx := strings.LastIndex(spec, ":/")
		if idx <= 0 {
			continue
		}
		host, cpath := spec[:idx], spec[idx+1:]
		mounts[cpath] = host
	}

	for i := 0; i+1 < len(args); i++ {
		if args[i] != "-cf" {
			continue
		}
		containerPath := args[i+1]
		hostPath := containerPath
		for cpath, hpath := range mounts {
			if strings.HasPrefix(containerPath, cpath+"/") {
				hostPath = filepath.Join(hpath, strings.TrimPrefix(containerPath, cpath+"/"))
				break
			}
		}
		if err := os.MkdirAll(filepath.Dir(hostPath), 0o700); err != nil {
			return nil, err
		}
		if err := os.WriteFile(hostPath, f.payload, 0o600); err != nil {
			return nil, err
		}
		return nil, nil
	}
	return nil, nil
}

// callCount 返回调用次数。
func (f *fakeDocker) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

// newTestExporter 构造一个用假 docker 的导出器。
func newTestExporter(t *testing.T, payload []byte) (*VolumeExporter, *fakeDocker) {
	t.Helper()
	fd := &fakeDocker{payload: payload}
	root := filepath.Join(t.TempDir(), "volumes")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	return NewVolumeExporter(root, fd.run), fd
}

// readAllExport 按块把归档完整读出。
func readAllExport(t *testing.T, e *VolumeExporter, id string) []byte {
	t.Helper()
	var out bytes.Buffer
	var offset int64
	for {
		data, eof, err := e.GetChunk(context.Background(), id, offset, ChunkSize)
		if err != nil {
			t.Fatalf("读块失败(offset=%d): %v", offset, err)
		}
		if len(data) == 0 {
			if !eof {
				t.Fatalf("offset=%d 返回空块但未标 EOF", offset)
			}
			break
		}
		out.Write(data)
		offset += int64(len(data))
		if eof {
			break
		}
	}
	return out.Bytes()
}

// payLoad 生成跨越多个分块的数据（含残块）。
func payLoad(n int) []byte {
	base := []byte("0123456789abcdef")
	out := make([]byte, n)
	for i := range out {
		out[i] = base[i%len(base)]
	}
	return out
}

// TestVolumeExportRoundTrip 验证导出 → 分块读回逐字节一致。
func TestVolumeExportRoundTrip(t *testing.T) {
	payload := payLoad(700_000) // > 256 KiB 且非整块
	exp, _ := newTestExporter(t, payload)

	info, err := exp.Export(context.Background(), "demo_data", "demo")
	if err != nil {
		t.Fatalf("导出失败: %v", err)
	}
	if info.SizeBytes != int64(len(payload)) {
		t.Fatalf("大小应为 %d，实际 %d", len(payload), info.SizeBytes)
	}
	sum := sha256.Sum256(payload)
	if !equalHex(info.SHA256, hex.EncodeToString(sum[:])) {
		t.Fatalf("校验和应为 %s，实际 %s", hex.EncodeToString(sum[:]), info.SHA256)
	}
	got := readAllExport(t, exp, info.ExportID)
	if !bytes.Equal(got, payload) {
		t.Fatalf("读回内容与原归档不一致（%d vs %d 字节）", len(got), len(payload))
	}
}

// TestVolumeExportRejectsBadName 验证非法卷名在起容器之前就被拒绝。
//
// ============================ 这是路径安全的首要断言 ============================
// 卷名会进 docker 参数与归档路径。若非法名能走到 docker 那一步，
// 一个 ".." 或 "/" 就能让 tar 去打包别的东西。
func TestVolumeExportRejectsBadName(t *testing.T) {
	exp, fd := newTestExporter(t, []byte("x"))
	for _, bad := range []string{"../etc", "/abs", "a/b", "", "a b"} {
		if _, err := exp.Export(context.Background(), bad, "demo"); !errors.Is(err, ErrBadVolumeName) {
			t.Fatalf("非法卷名 %q 应返回 ErrBadVolumeName，实际 %v", bad, err)
		}
	}
	// 关键：一次 docker 都没调用。
	if fd.callCount() != 0 {
		t.Fatalf("非法卷名不应调用 docker，实际调用 %d 次", fd.callCount())
	}
}

// TestVolumeExportDockerFailureCleansUp 验证打包失败不留残目录。
func TestVolumeExportDockerFailureCleansUp(t *testing.T) {
	exp, fd := newTestExporter(t, []byte("x"))
	fd.err = errors.New("No such volume")

	before := countDirs(t, exp.root)
	if _, err := exp.Export(context.Background(), "demo_data", "demo"); err == nil {
		t.Fatal("docker 失败时应返回错误")
	}
	if after := countDirs(t, exp.root); after != before {
		t.Fatalf("失败不应留下目录：之前 %d，之后 %d", before, after)
	}
}

// TestVolumeExportFiltersDockerError 验证 docker 原文不会被回传。
//
// docker 的错误常含镜像地址、registry 认证细节、内网主机名。
// 这些会随"失败原因"流到 Hub 与浏览器。
func TestVolumeExportFiltersDockerError(t *testing.T) {
	exp, fd := newTestExporter(t, []byte("x"))
	fd.err = errors.New("Error response from daemon: No such volume: secretinternalname")

	_, err := exp.Export(context.Background(), "demo_data", "demo")
	if err == nil {
		t.Fatal("应返回错误")
	}
	msg := err.Error()
	if strings.Contains(msg, "secretinternalname") || strings.Contains(msg, "daemon") {
		t.Fatalf("错误文案不应含 docker 原文，实际 %q", msg)
	}
	// 但必须能让用户看懂该做什么。
	if !strings.Contains(msg, "不存在该数据卷") {
		t.Fatalf("应给出可操作的中文说明，实际 %q", msg)
	}
}

// TestVolumeExportNotFoundAfterClose 验证关闭后不可再读。
//
// 断言的是**登记层面**就拒绝，而不是靠文件系统兜底：
// POSIX 上 unlink 已打开的文件不会让句柄失效，读者会拿到
// "看似正常但已不在磁盘"的数据。
func TestVolumeExportNotFoundAfterClose(t *testing.T) {
	exp, _ := newTestExporter(t, []byte("payload"))
	info, err := exp.Export(context.Background(), "demo_data", "demo")
	if err != nil {
		t.Fatalf("导出失败: %v", err)
	}

	released, msg, cerr := exp.Close(context.Background(), info.ExportID)
	if cerr != nil {
		t.Fatalf("关闭失败: %v", cerr)
	}
	if !released {
		t.Fatalf("首次关闭应真的释放，实际 %s", msg)
	}
	if _, err := exp.Open(context.Background(), info.ExportID); !errors.Is(err, ErrVolumeExportNotFound) {
		t.Fatalf("关闭后 open 应返回 ErrVolumeExportNotFound，实际 %v", err)
	}
	if _, _, err := exp.GetChunk(context.Background(), info.ExportID, 0, 1024); !errors.Is(err, ErrVolumeExportNotFound) {
		t.Fatalf("关闭后读块应返回 ErrVolumeExportNotFound，实际 %v", err)
	}
	// 重复关闭不报错，但如实说明。
	released, msg, cerr = exp.Close(context.Background(), info.ExportID)
	if cerr != nil {
		t.Fatalf("重复关闭不应报错: %v", cerr)
	}
	if released {
		t.Fatal("重复关闭不应声称释放了东西")
	}
	if !strings.Contains(msg, "不存在") {
		t.Fatalf("应说明归档不存在，实际 %q", msg)
	}
}

// TestVolumeExportExpiredOnAccess 验证过期在访问时被发现。
//
// 后台清扫是尽力而为的：只靠它，过期归档会继续被读到，
// 而读出过期数据比报错危险得多。
func TestVolumeExportExpiredOnAccess(t *testing.T) {
	exp, _ := newTestExporter(t, []byte("payload"))
	info, err := exp.Export(context.Background(), "demo_data", "demo")
	if err != nil {
		t.Fatalf("导出失败: %v", err)
	}
	base := time.Now()
	exp.now = func() time.Time { return base.Add(VolumeExportTTL + time.Minute) }

	if _, err := exp.Open(context.Background(), info.ExportID); !errors.Is(err, ErrVolumeExportExpired) {
		t.Fatalf("过期后 open 应返回 ErrVolumeExportExpired，实际 %v", err)
	}
	// 已被移出登记表，之后再访问收敛为 not_found。
	if _, err := exp.Open(context.Background(), info.ExportID); !errors.Is(err, ErrVolumeExportNotFound) {
		t.Fatalf("过期清理后应返回 ErrVolumeExportNotFound，实际 %v", err)
	}
}

// TestVolumeExportSweepReclaimsDisk 验证清扫会真删文件。
func TestVolumeExportSweepReclaimsDisk(t *testing.T) {
	exp, _ := newTestExporter(t, []byte("payload"))
	info, err := exp.Export(context.Background(), "demo_data", "demo")
	if err != nil {
		t.Fatalf("导出失败: %v", err)
	}
	before := countDirs(t, exp.root)
	if n := exp.SweepExpired(); n != 0 {
		t.Fatalf("未过期时不应清理，实际 %d", n)
	}
	base := time.Now()
	exp.now = func() time.Time { return base.Add(VolumeExportTTL + time.Minute) }
	if n := exp.SweepExpired(); n != 1 {
		t.Fatalf("过期后应清理 1 个，实际 %d", n)
	}
	if after := countDirs(t, exp.root); after != before-1 {
		t.Fatalf("目录应被删除：之前 %d，之后 %d", before, after)
	}
	if _, err := exp.Open(context.Background(), info.ExportID); err == nil {
		t.Fatal("清理后 open 应失败")
	}
}

// TestVolumeExportRespectsLimit 验证数量上限。
func TestVolumeExportRespectsLimit(t *testing.T) {
	exp, _ := newTestExporter(t, []byte("x"))
	for i := 0; i < MaxVolumeExports; i++ {
		if _, err := exp.Export(context.Background(), "demo_data", "demo"); err != nil {
			t.Fatalf("第 %d 次导出应成功: %v", i+1, err)
		}
	}
	if _, err := exp.Export(context.Background(), "demo_data", "demo"); !errors.Is(err, ErrTooManyVolumeExports) {
		t.Fatalf("超出上限应返回 ErrTooManyVolumeExports，实际 %v", err)
	}
}

// TestVolumeExportCleanupOrphans 验证启动清理只删无主目录。
func TestVolumeExportCleanupOrphans(t *testing.T) {
	exp, _ := newTestExporter(t, []byte("payload"))
	info, err := exp.Export(context.Background(), "demo_data", "demo")
	if err != nil {
		t.Fatalf("导出失败: %v", err)
	}
	// 制造一个无主目录（模拟上次进程被 kill）。
	orphan := filepath.Join(exp.root, "deadbeefdeadbeefdeadbeefdeadbeef")
	if err := os.MkdirAll(orphan, 0o700); err != nil {
		t.Fatal(err)
	}

	if n := exp.CleanupOrphans(); n != 1 {
		t.Fatalf("应清理 1 个无主目录，实际 %d", n)
	}
	// 有登记的归档必须保留（否则正在用的归档会被删掉）。
	if _, err := exp.Open(context.Background(), info.ExportID); err != nil {
		t.Fatalf("有登记的归档不应被清理: %v", err)
	}
}

// TestVolumeExportCleanupOrphansSparesNeighbourDirs 是**结构性安全断言**：
// 启动清理只能删自己建的目录，绝不碰邻居。
//
// ============================ 这条测试防的是什么事故 ============================
// 卷归档的 root 是 transfers/volumes，与镜像归档共用 transfers 临时区。
// 因此同一棵目录树里同时住着：
//
//	transfers/<32位hex>/     镜像归档任务目录（imagetransfer 建的）
//	transfers/volumes/<32位hex>/  卷归档（本包建的）
//	transfers/imports/<32位hex>/  卷导入任务（本包建的）
//
// 若清理逻辑"删掉一切不认识的目录"，那么把 root 误设成 transfers/
// 就会连镜像归档一起删——而镜像与卷归档可能正在传输中。
//
// 这里造出各种"不该被删"的邻居目录，断言它们全都活着。
func TestVolumeExportCleanupOrphansSparesNeighbourDirs(t *testing.T) {
	exp, _ := newTestExporter(t, []byte("payload"))

	// 只有一个真正属于我们的无主归档目录（32 位 hex）。
	ourOrphan := filepath.Join(exp.root, "deadbeefdeadbeefdeadbeefdeadbeef")
	if err := os.MkdirAll(ourOrphan, 0o700); err != nil {
		t.Fatal(err)
	}
	// 各种邻居：名字都不是 32 位 hex，全都不能被删。
	neighbours := []string{
		"volumes",                          // 若 root 被误设成 transfers/，这就是我们自己
		"imports",                          // 导入任务目录
		"stacks",                           // 万一把 data_dir 误传进来
		"backups",                          // 备份产物
		"tmp",                              // 其他临时目录
		"NOTHEX",                           // 大写十六进制（大小写规则不同，不算我们的）
		"deadbeef",                         // 长度不够
		"deadbeefdeadbeefdeadbeefdeadbeeg", // 长度对但含非 hex 字符 'g'
	}
	neighbourPaths := make(map[string]string, len(neighbours))
	for _, n := range neighbours {
		p := filepath.Join(exp.root, n)
		if err := os.MkdirAll(p, 0o700); err != nil {
			t.Fatal(err)
		}
		// 放一个哨兵文件：目录还在但内容被删也算失败。
		if err := os.WriteFile(filepath.Join(p, "sentinel.txt"), []byte("keep me"), 0o600); err != nil {
			t.Fatal(err)
		}
		neighbourPaths[n] = p
	}

	if n := exp.CleanupOrphans(); n != 1 {
		t.Fatalf("应只清理 1 个自建的无主目录，实际删了 %d 个", n)
	}

	// 自建的无主目录必须被删掉。
	if _, err := os.Stat(ourOrphan); !os.IsNotExist(err) {
		t.Fatalf("自建的无主归档目录应被清理，实际仍存在（err=%v）", err)
	}
	// 所有邻居必须完好无损（目录在、哨兵文件在）。
	for _, n := range neighbours {
		p := neighbourPaths[n]
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("邻居目录 %q 被误删: %v", n, err)
		}
		if _, err := os.Stat(filepath.Join(p, "sentinel.txt")); err != nil {
			t.Fatalf("邻居目录 %q 的内容被误删: %v", n, err)
		}
	}
}

// TestVolumeExportConcurrentDistinctIDs 验证并发导出各得不同标识。
func TestVolumeExportConcurrentDistinctIDs(t *testing.T) {
	exp, _ := newTestExporter(t, []byte("payload"))
	var (
		wg  sync.WaitGroup
		mu  sync.Mutex
		ids []string
	)
	for i := 0; i < MaxVolumeExports; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			info, err := exp.Export(context.Background(), "demo_data", "demo")
			if err != nil {
				return
			}
			mu.Lock()
			ids = append(ids, info.ExportID)
			mu.Unlock()
		}()
	}
	wg.Wait()
	if len(ids) != MaxVolumeExports {
		t.Fatalf("并发导出应全部成功，实际 %d/%d", len(ids), MaxVolumeExports)
	}
	seen := map[string]bool{}
	for _, id := range ids {
		if seen[id] {
			t.Fatalf("导出标识重复: %q", id)
		}
		seen[id] = true
	}
}

// countDirs 统计某目录下的子目录数。
func countDirs(t *testing.T, root string) int {
	t.Helper()
	entries, err := os.ReadDir(root)
	if err != nil {
		return 0
	}
	n := 0
	for _, e := range entries {
		if e.IsDir() {
			n++
		}
	}
	return n
}
