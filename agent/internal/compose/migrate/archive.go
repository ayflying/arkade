package migrate

// archive.go：卷归档的导出、读取、释放。
//
// ============================ 与镜像归档的异同 ============================
//
// 相同点（刻意保持的）：ExportID 形态、分块协议（256 KiB）、
// 校验和语义、过期清理、access-time 校验。Hub 侧因此能用同一套复制代码。
//
// 不同点：
//
//	镜像归档  导出后**持有**直到显式 close（迁移要先导出、再规划、最后拉取）
//	卷归档    同样是持有式，但 TTL 更短、数量上限更小
//
// ============================ 为什么卷用临时容器打 tar ============================
//
// 与 backup.go 完全相同的理由，且复用同一条已验证的路径：
//   - 直接读 /var/lib/docker/volumes 需要知道 Docker 的 root-dir（可能被改过）；
//   - 卷里的文件属主/权限/硬链接只有在容器内打 tar 才保得住；
//   - Agent 本身跑在容器里，只有 docker.sock，没有宿主机文件系统。
//
// ============================ 为什么先落盘再分块读，而不是流式 ============================
// 流式（tar 输出直接接到分块读取）能省一次落盘，但要求"读的进度"与
// "tar 的进度"实时同步——而分块读取可能因 Hub 超时中断、稍后从任意 offset 重试。
// 流式下重试意味着要从头重跑 tar；落盘后则可以随时按 offset 续读。
//
// 卷可能有几十 GB，重跑一次的代价远大于一次落盘。因此选择落盘。

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// exportIDLen 是卷归档 id 的长度。
//
// 与生成处保持一致：randomHex(16) 取 16 字节 → 32 个十六进制字符。
// 单独抽成常量是因为它同时被"生成"和"清理时的名字校验"用到，
// 两处必须一致；写死两次迟早会改漏一处，而改漏的后果是
// 启动清理把正在用的归档目录当成残留删掉。
const exportIDLen = 32

// 卷归档的限额与保留时长。
const (
	// VolumeExportTTL 是卷归档的保留时长。
	//
	// 比镜像归档（2 小时）短：卷归档是"导出后马上传走"的用途，
	// 不存在"先导出、隔很久再拿"的场景。而卷可能几十 GB，
	// 长 TTL 会让一台机器的磁盘长期被占满。
	VolumeExportTTL = 30 * time.Minute
	// MaxVolumeExports 是同时持有的卷归档数量上限。
	//
	// 比镜像（8）更保守：单个卷可能比单个镜像大一个量级，
	// 数量稍多就会顶到磁盘配额。
	MaxVolumeExports = 4
	// ChunkSize 是分块读取的块大小，与镜像归档保持一致。
	ChunkSize = 256 << 10
	// MaxVolumeArchiveBytes 是单个卷归档的大小上限。
	MaxVolumeArchiveBytes int64 = 128 << 30
)

// 卷归档的领域错误。
var (
	// ErrVolumeExportNotFound 归档不存在（从未导出 / 已关闭 / 已过期）。
	ErrVolumeExportNotFound = errors.New("卷归档不存在或已释放")
	// ErrVolumeExportExpired 归档已过期。
	ErrVolumeExportExpired = errors.New("卷归档已过期")
	// ErrTooManyVolumeExports 同时持有的卷归档数量达上限。
	ErrTooManyVolumeExports = errors.New("同时持有的卷归档数量已达上限")
	// ErrVolumeArchiveTooLarge 卷归档超过大小上限。
	ErrVolumeArchiveTooLarge = errors.New("卷归档超过大小上限")
	// ErrBadVolumeName 卷名非法。
	ErrBadVolumeName = errors.New("数据卷名称不合法")
	// ErrBadOffset 分块偏移不合法。
	ErrBadOffset = errors.New("分块偏移不合法")
)

// volumeArchive 是一份持有中的卷归档。
type volumeArchive struct {
	id      string
	volume  string
	size    int64
	sha256  string
	expires time.Time
	// readers 挡住"正在被读时删除文件"，与镜像归档同一个理由。
	readers int32
	gone    bool
}

// VolumeExportInfo 是一份卷归档的对外只读信息。
type VolumeExportInfo struct {
	ExportID  string
	Volume    string
	SizeBytes int64
	SHA256    string
	ExpiresAt time.Time
}

// VolumeExporter 管理卷归档。
//
// 与 imagetransfer.Manager 分开一个类型：卷归档与镜像归档的
// 触发方式、限额、TTL 都不同，混在一张表里会让两套规则互相影响。
type VolumeExporter struct {
	// root 是归档存放目录（transfers 下的子目录）。
	root string
	// docker 是执行 docker 命令的函数，可被测试替换。
	docker func(ctx context.Context, args ...string) ([]byte, error)
	// now 可被测试替换。
	now func() time.Time

	mu       sync.Mutex
	archives map[string]*volumeArchive
}

// NewVolumeExporter 构造卷归档管理器。
//
// root 必须是一个**专用目录**（不与 stacks/、backups/ 混放），
// 由调用方保证它已创建且权限正确。
func NewVolumeExporter(root string, docker func(ctx context.Context, args ...string) ([]byte, error)) *VolumeExporter {
	if docker == nil {
		docker = defaultDocker
	}
	e := &VolumeExporter{
		root:     root,
		docker:   docker,
		now:      time.Now,
		archives: make(map[string]*volumeArchive),
	}
	return e
}

// defaultDocker 是默认的 docker 命令执行器。
func defaultDocker(ctx context.Context, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "docker", args...)
	return cmd.CombinedOutput()
}

// validVolumeName 校验卷名。
//
// ============================ 这是路径安全的第一道线 ============================
// 卷名会出现在两处危险位置：docker 命令行参数、归档文件名。
// Docker 的卷名规则是 [a-zA-Z0-9][a-zA-Z0-9_.-]*，
// 因此这里严格按它校验——任何含 / 或 .. 的输入在这里就被拒，
// 不会走到"拼路径"或"传给 docker"那一步。
func validVolumeName(name string) bool {
	if len(name) == 0 || len(name) > 255 {
		return false
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		ok := (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') ||
			(c >= '0' && c <= '9') || c == '_' || c == '.' || c == '-'
		if !ok {
			return false
		}
	}
	return true
}

// newExportID 生成 32 位小写十六进制标识（与镜像归档同一形态）。
func newExportID() string {
	// exportIDLen/2 个字节 → exportIDLen 个十六进制字符。
	// 用常量而不是硬编码 16，保证生成与校验永远同源。
	return randomHex(exportIDLen / 2)
}

// projectVolume 把卷打成 tar 并落盘，返回归档信息。
//
// 调用方必须已经确认 volume 是**该项目的**卷（由 ResolvePlan 得出），
// 这里不再做归属校验：归属判断需要 compose 配置，那是调用方的事。
func (e *VolumeExporter) Export(ctx context.Context, volume, project string) (VolumeExportInfo, error) {
	if !validVolumeName(volume) {
		return VolumeExportInfo{}, fmt.Errorf("%w: %q", ErrBadVolumeName, volume)
	}

	// 数量上限在落盘**之前**判断：否则会先写满磁盘再报"超限"。
	e.mu.Lock()
	live := 0
	for _, a := range e.archives {
		if !a.gone {
			live++
		}
	}
	if live >= MaxVolumeExports {
		e.mu.Unlock()
		return VolumeExportInfo{}, ErrTooManyVolumeExports
	}
	id := newExportID()
	e.mu.Unlock()

	// 归档落在专用子目录：<root>/<exportID>/volume.tar
	// 用 exportID 而不是卷名做目录名：卷名可能重复（不同项目的同名卷），
	// 而 exportID 唯一，重名会互相覆盖。
	dir, err := e.archiveDir(id)
	if err != nil {
		return VolumeExportInfo{}, err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return VolumeExportInfo{}, fmt.Errorf("创建归档目录失败: %w", err)
	}
	tarPath := filepath.Join(dir, "volume.tar")

	// 用临时容器的 tar：与 backup.go 同一条路径。
	// 挂载为 :ro——导出绝不能修改源数据。
	if _, derr := e.docker(ctx, "run", "--rm",
		"-v", volume+":/from:ro",
		"-v", dir+":/backup",
		"alpine:3.20",
		"tar", "-cf", "/backup/volume.tar", "-C", "/from", "."); derr != nil {
		_ = os.RemoveAll(dir)
		return VolumeExportInfo{}, filterDockerError(derr)
	}

	sha, size, herr := hashFile(tarPath)
	if herr != nil {
		_ = os.RemoveAll(dir)
		return VolumeExportInfo{}, herr
	}
	if size > MaxVolumeArchiveBytes {
		_ = os.RemoveAll(dir)
		return VolumeExportInfo{}, ErrVolumeArchiveTooLarge
	}

	e.mu.Lock()
	defer e.mu.Unlock()
	a := &volumeArchive{
		id: id, volume: volume, size: size, sha256: sha,
		expires: e.now().Add(VolumeExportTTL),
	}
	e.archives[id] = a
	return a.info(), nil
}

// Open 确认归档仍可用。
func (e *VolumeExporter) Open(ctx context.Context, exportID string) (VolumeExportInfo, error) {
	a, err := e.lookup(exportID)
	if err != nil {
		return VolumeExportInfo{}, err
	}
	defer e.endRead(a)

	path, err := e.archivePath(exportID)
	if err != nil {
		return VolumeExportInfo{}, err
	}
	fi, serr := os.Lstat(path)
	if serr != nil {
		// 登记还在但文件没了：报"不存在"，调用方的正确反应是重新导出。
		return VolumeExportInfo{}, ErrVolumeExportNotFound
	}
	// 大小必须与登记一致：被截断的归档若被当成完整归档传给目标端，
	// 会在解包时失败——而那时已经花掉了整个传输时间。
	if fi.Size() != a.size {
		return VolumeExportInfo{}, fmt.Errorf("卷归档大小与登记不一致（登记 %d，实际 %d）", a.size, fi.Size())
	}
	return a.info(), nil
}

// GetChunk 读取一块字节。
func (e *VolumeExporter) GetChunk(ctx context.Context, exportID string, offset, maxBytes int64) ([]byte, bool, error) {
	if offset < 0 {
		return nil, false, ErrBadOffset
	}
	a, err := e.lookup(exportID)
	if err != nil {
		return nil, false, err
	}
	defer e.endRead(a)

	if maxBytes <= 0 || maxBytes > ChunkSize {
		maxBytes = ChunkSize
	}
	path, err := e.archivePath(exportID)
	if err != nil {
		return nil, false, err
	}
	f, oerr := os.Open(path)
	if oerr != nil {
		return nil, false, ErrVolumeExportNotFound
	}
	defer func() { _ = f.Close() }()

	if offset >= a.size {
		return nil, true, nil
	}
	buf := make([]byte, maxBytes)
	n, rerr := f.ReadAt(buf, offset)
	if rerr != nil && !errors.Is(rerr, io.EOF) {
		return nil, false, rerr
	}
	eof := offset+int64(n) >= a.size
	return buf[:n], eof, nil
}

// Close 释放归档。
//
// released=false 且 err=nil 表示本来就不存在，**不是错误**。
func (e *VolumeExporter) Close(ctx context.Context, exportID string) (bool, string, error) {
	e.mu.Lock()
	a, ok := e.archives[exportID]
	if ok {
		a.gone = true
		delete(e.archives, exportID)
	}
	e.mu.Unlock()

	if !ok {
		return false, "卷归档不存在（可能已释放或已过期）", nil
	}
	// 等正在读的调用结束再删：否则读者会拿到一个已 unlink 但可读的句柄，
	// 读到看似正常却已不存在的字节。
	if !e.waitReadersIdle(a) {
		return false, "卷归档仍被读取占用，请稍后重试", fmt.Errorf("卷归档仍被读取")
	}
	dir, err := e.archiveDir(exportID)
	if err != nil {
		return false, "", err
	}
	if err := os.RemoveAll(dir); err != nil {
		return false, "删除卷归档失败", err
	}
	return true, "已释放", nil
}

// SweepExpired 清理过期归档，返回清理数量。
func (e *VolumeExporter) SweepExpired() int {
	now := e.now()
	var expired []*volumeArchive
	e.mu.Lock()
	for id, a := range e.archives {
		if now.After(a.expires) {
			a.gone = true
			delete(e.archives, id)
			expired = append(expired, a)
		}
	}
	e.mu.Unlock()

	removed := 0
	for _, a := range expired {
		if !e.waitReadersIdle(a) {
			continue
		}
		if dir, err := e.archiveDir(a.id); err == nil {
			if err := os.RemoveAll(dir); err == nil {
				removed++
			}
		}
	}
	return removed
}

// CleanupOrphans 删除 root 下所有"没有登记"的子目录。
//
// 用于启动清理：进程被 kill 时 defer 不会执行，残留只能在这里收拾。
// 进程重启后登记表是空的，磁盘上的归档**无法证明仍然有效**，
// 因此一律删除——宁可让迁移重新导出，也不能让调用方拿到
// 一份"没有登记、无从校验"的卷归档。
// CleanupOrphans 清理当前登记表里没有的归档目录。
//
// ============================ 为什么不盲删 root 下的所有目录 ============================
// 早期版本直接遍历 root 并删掉所有不在登记表里的目录。它当时是对的，
// 但正确性依赖一个**没有强制、也没有说明**的隐式前提：root 是独占目录。
//
// 现实是我们把 root 放在 transfers/ 之下（transfers/volumes），
// 与镜像归档共用同一个临时区。同级的 transfers/imports 就是导入任务的
// 目录，而 imagetransfer 的 cleanupOrphans 恰好也在扫 transfers/。
// 两边都在"删自己不认识的目录"——目前靠各自的名字规则**碰巧**错开：
//   - imagetransfer 只删 32 位小写 hex；
//   - 我们删一切。
//
// 只要有人把某一侧的目录名改成看起来像另一侧，或者把 root 上提一层
// （改成 transfers 本身），一次启动清理就会删掉对面正在用的归档。
//
// 因此这里改成**白名单式**：只删符合卷归档 id 规则的目录。
// 这让"清理"永远只作用于自己建的东西，与邻居的命名规则无关。
func (e *VolumeExporter) CleanupOrphans() int {
	active := make(map[string]struct{})
	e.mu.Lock()
	for id := range e.archives {
		active[id] = struct{}{}
	}
	e.mu.Unlock()

	entries, err := os.ReadDir(e.root)
	if err != nil {
		return 0
	}
	removed := 0
	for _, ent := range entries {
		// 名字不符合归档 id 规则：不是我们建的，**不动它**。
		//
		// 这条判断同时挡住了"root 被上提一层"的事故：
		// 若 root 误设成 transfers/，imports 与其它非 hex 名字都会被跳过。
		if !validExportID(ent.Name()) {
			continue
		}
		if !ent.IsDir() {
			// 符号链接或普通文件：不是我们建的目录，删链接本身即可，
			// 绝不进入它指向的位置。
			if ent.Type()&os.ModeSymlink != 0 {
				if os.Remove(filepath.Join(e.root, ent.Name())) == nil {
					removed++
				}
			}
			continue
		}
		if _, ok := active[ent.Name()]; ok {
			continue
		}
		if err := os.RemoveAll(filepath.Join(e.root, ent.Name())); err == nil {
			removed++
		}
	}
	return removed
}

// validExportID 判断目录名是否是本包生成的卷归档 id。
//
// 与 imagetransfer 的 validTaskID 用同一种形状（32 位小写 hex），
// 但不是同一条规则：两个包各自的 id 空间独立，
// 谁将来改格式都不该影响对方。
func validExportID(id string) bool {
	if len(id) != exportIDLen {
		return false
	}
	for i := 0; i < len(id); i++ {
		c := id[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// lookup 取出可读归档并登记一次读（必须与 endRead 配对）。
//
// 过期判定在访问时做，而不只靠后台清扫：后台是尽力而为的，
// 若只靠它，过期归档会继续被读到——**读出过期数据比报错危险得多**。
func (e *VolumeExporter) lookup(id string) (*volumeArchive, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	a, ok := e.archives[id]
	if !ok {
		return nil, ErrVolumeExportNotFound
	}
	if a.gone {
		return nil, ErrVolumeExportNotFound
	}
	if e.now().After(a.expires) {
		a.gone = true
		delete(e.archives, id)
		return nil, ErrVolumeExportExpired
	}
	a.readers++
	return a, nil
}

func (e *VolumeExporter) endRead(a *volumeArchive) {
	e.mu.Lock()
	if a.readers > 0 {
		a.readers--
	}
	e.mu.Unlock()
}

// waitReadersIdle 等该归档在飞读结束，返回是否等到。
func (e *VolumeExporter) waitReadersIdle(a *volumeArchive) bool {
	const (
		step    = 2 * time.Millisecond
		maxWait = 2 * time.Second
	)
	var waited time.Duration
	for {
		e.mu.Lock()
		n := a.readers
		e.mu.Unlock()
		if n == 0 {
			return true
		}
		if waited >= maxWait {
			return false
		}
		time.Sleep(step)
		waited += step
	}
}

// archiveDir 返回某归档的目录（含路径逃逸防护）。
func (e *VolumeExporter) archiveDir(id string) (string, error) {
	if !validVolumeName(id) {
		return "", fmt.Errorf("%w: %q", ErrBadVolumeName, id)
	}
	dir := filepath.Join(e.root, id)
	rel, err := filepath.Rel(e.root, dir)
	if err != nil || strings.HasPrefix(rel, "..") {
		return "", fmt.Errorf("归档路径逃出根目录")
	}
	return dir, nil
}

// archivePath 返回某归档的 tar 文件路径。
func (e *VolumeExporter) archivePath(id string) (string, error) {
	dir, err := e.archiveDir(id)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "volume.tar"), nil
}

// info 生成对外信息。
func (a *volumeArchive) info() VolumeExportInfo {
	return VolumeExportInfo{
		ExportID: a.id, Volume: a.volume, SizeBytes: a.size,
		SHA256: a.sha256, ExpiresAt: a.expires,
	}
}

// hashFile 计算文件的大小与 sha256（一块一块读，不整文件进内存）。
func hashFile(path string) (string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, fmt.Errorf("打开卷归档失败: %w", err)
	}
	defer func() { _ = f.Close() }()

	h := sha256.New()
	n, cerr := io.Copy(h, f)
	if cerr != nil {
		return "", 0, fmt.Errorf("读取卷归档失败: %w", cerr)
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

// filterDockerError 把 docker 的原始输出转成中文说明。
//
// ============================ 必须过滤 ============================
// docker 的错误文本常含镜像地址、registry 认证细节、内网主机名。
// 这些信息会随"任务失败原因"流到 Hub、浏览器与日志里。
// 因此这里只保留**可操作**的部分，其余一律替换成固定文案。
func filterDockerError(err error) error {
	msg := err.Error()
	switch {
	case strings.Contains(msg, "No such volume"):
		return fmt.Errorf("源服务器上不存在该数据卷")
	case strings.Contains(msg, "permission denied"):
		return fmt.Errorf("无权限读取该数据卷（请检查 Agent 的 docker 权限）")
	case strings.Contains(msg, "no space left"):
		return fmt.Errorf("磁盘空间不足，无法写出卷归档")
	case strings.Contains(msg, "pull access denied"), strings.Contains(msg, "not found"):
		// 打包用的 alpine 镜像拉不到（离线环境）。
		return fmt.Errorf("无法获取打包用的临时镜像，请确认服务器可访问镜像源")
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return fmt.Errorf("打包数据卷超时或被取消")
	}
	return fmt.Errorf("打包数据卷失败（请查看服务器上的 docker 日志）")
}
