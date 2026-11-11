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
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
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
	pending  int
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
	if len(name) == 0 || len(name) > 255 || !((name[0] >= 'a' && name[0] <= 'z') || (name[0] >= 'A' && name[0] <= 'Z') || (name[0] >= '0' && name[0] <= '9')) {
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

	if out, err := e.docker(ctx, "volume", "inspect", volume); err != nil {
		return VolumeExportInfo{}, filterDockerError(fmt.Errorf("%w: %s", err, out))
	}
	// 数量上限在落盘**之前**判断：否则会先写满磁盘再报"超限"。
	e.mu.Lock()
	live := e.pending
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
	e.pending++
	e.mu.Unlock()
	defer func() { e.mu.Lock(); e.pending--; e.mu.Unlock() }()

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
	if _, derr := runArchiveHelper(ctx, e.docker,
		"-v", volume+":/from:ro",
		"-v", dir+":/backup",
		"alpine:3.20",
		"tar", "-cf", "/backup/volume.tar", "-C", "/from", "."); derr != nil {
		if errors.Is(derr, ErrHelperCleanup) {
			return VolumeExportInfo{}, derr
		}
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
