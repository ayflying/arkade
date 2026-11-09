package helperfs

// helperfs 是存储卷文件操作的**执行端**，运行在临时 helper 容器内部。
//
// 它只认一个已经挂载好的根目录（/data），在其上做受限的 CRUD。
// 三点设计约束：
//
//  1. 全部走 os.Root（Go 1.24）：所有路径解析被限制在根目录内，
//     即使传入 ../.. 也无法逃出。/data 是卷，卷外是容器临时文件系统，
//     逃出卷就意味着能改 helper 自己的二进制。
//  2. 不做 shell、不调 python：只有 Go 标准库。卷里可能有用户任意内容，
//     把文件名拼进命令行是注入漏洞的经典来源。
//  3. 拒绝符号链接与多硬链接：卷里的 symlink 可能指向 /etc 或宿主机可见路径，
//     跟随它就等于绕过上面的根限制。

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"runtime"
	"strings"
)

const (
	// MaxListEntries 是 list 的最大返回条数。超过就报错而不是静默截断：
	// 静默截断会让用户以为目录里只有这些，从而误删。
	MaxListEntries = 2000
	// MaxFileBytes 是单文件读写上限。
	MaxFileBytes = 4 << 20
)

// 操作类型。
const (
	OpList   = "list"
	OpRead   = "read"
	OpWrite  = "write"
	OpMkdir  = "mkdir"
	OpRename = "rename"
	OpDelete = "delete"
)

var (
	ErrBadPath             = errors.New("路径不合法")
	ErrTooMany             = errors.New("目录项过多")
	ErrFileTooLarge        = errors.New("文件超过大小上限")
	ErrSymlink             = errors.New("拒绝操作符号链接")
	ErrHardlink            = errors.New("拒绝操作多硬链接文件")
	ErrNotEmpty            = errors.New("目录非空，拒绝删除")
	ErrExists              = errors.New("目标已存在")
	ErrChecksum            = errors.New("校验和不匹配")
	ErrNotRegular          = errors.New("不是普通文件")
	ErrNoAtomicRename      = errors.New("该文件系统不支持原子重命名")
	ErrUnsupportedPlatform = errors.New("该平台不支持卷文件操作")
)

// Entry 是一条目录项。
type Entry struct {
	Name       string
	Kind       string // directory / file / symlink / unsupported
	Size       int64
	ModifiedAt int64
	Mode       string
}

// Result 是操作的统一返回。
type Result struct {
	Path       string
	Entries    []Entry
	DataBase64 string
	SHA256     string
	Size       int64
	Message    string
}

// Request 是一次操作请求。
type Request struct {
	Operation      string
	Path           string
	Destination    string
	DataBase64     string
	ExpectedSHA256 string
	CreateOnly     bool
}

// Do 在 root 上执行一次操作。
func Do(root *os.Root, req Request) (Result, error) {
	// 首批只在 Linux 上提供：硬链接检查依赖 Stat_t.Nlink，
	// 原子改名依赖 renameat2(RENAME_NOREPLACE)，逐级打开依赖 openat。
	// 其它平台上这些保护不可用，与其降级成"看起来能跑但保护缺失"，
	// 不如明确 unsupported（fail closed）。
	if runtime.GOOS != "linux" {
		return Result{}, ErrUnsupportedPlatform
	}
	switch req.Operation {
	case OpList:
		return doList(root, req)
	case OpRead:
		return doRead(root, req)
	case OpWrite:
		return doWrite(root, req)
	case OpMkdir:
		return doMkdir(root, req)
	case OpRename:
		return doRename(root, req)
	case OpDelete:
		return doDelete(root, req)
	default:
		return Result{}, fmt.Errorf("不支持的操作: %s", req.Operation)
	}
}

// cleanPath 校验并规范化卷内相对路径。
//
// 拒绝：绝对路径、空段、"."、".."、NUL、反斜杠（在部分引擎/平台上会变成分隔符）。
// 根路径（""）只允许 list 使用，由各操作自行判断。
func cleanPath(p string) (string, error) {
	if strings.ContainsRune(p, '\x00') {
		return "", ErrBadPath
	}
	if strings.Contains(p, "\\") {
		return "", ErrBadPath
	}
	if strings.HasPrefix(p, "/") {
		return "", ErrBadPath
	}
	if p == "" || p == "." {
		return "", nil
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return "", ErrBadPath
		}
	}
	cleaned := path.Clean(p)
	if cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return "", ErrBadPath
	}
	return cleaned, nil
}

// checkNoSymlink 逐段检查路径上不存在符号链接。
//
// os.Root 本身已阻止逃出根目录，但 symlink 仍可指向卷内其它位置，
// 使用户"读一个看起来无害的名字"实际读到别处。逐段 Lstat 明确拒绝。
func checkNoSymlink(root *os.Root, rel string) error {
	if rel == "" {
		return nil
	}
	segments := strings.Split(rel, "/")
	cur := ""
	for _, seg := range segments {
		if cur == "" {
			cur = seg
		} else {
			cur = cur + "/" + seg
		}
		info, err := root.Lstat(cur)
		if err != nil {
			// 不存在是正常的（写新文件）。其余错误原样上抛。
			if errors.Is(err, os.ErrNotExist) {
				return nil
			}
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return ErrSymlink
		}
	}
	return nil
}

// nlinkOK 判断硬链接数是否允许操作。
//
// nlink == 0 表示"取不到"（非 Linux 平台），必须按不安全处理——
// 见 nlink_other.go 的说明。
func nlinkOK(n uint64) bool { return n == 1 }

// kindOf 把文件模式映射成协议里的 kind。
func kindOf(m os.FileMode) string {
	switch {
	case m.IsDir():
		return "directory"
	case m.IsRegular():
		return "file"
	case m&os.ModeSymlink != 0:
		return "symlink"
	default:
		// 设备、FIFO、socket 等：可显示，但绝不打开。
		return "unsupported"
	}
}

func doList(root *os.Root, req Request) (Result, error) {
	rel, err := cleanPath(req.Path)
	if err != nil {
		return Result{}, err
	}
	// 用 openDirNoFollow 逐级 openat(O_NOFOLLOW|O_DIRECTORY)：
	// root.Open 会**跟随**卷内符号链接（只要不出 root），
	// 于是"list 一个指向卷内别处的链接目录"会被静默放行，
	// 与"拒绝符号链接"的契约自相矛盾。这里必须同样拒绝。
	dir, err := openDirNoFollow(root, rel)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			// 保持 wire 层的 not_found 类别。
			return Result{}, os.ErrNotExist
		}
		return Result{}, err
	}
	defer func() { _ = dir.Close() }()

	entries, err := dir.ReadDir(MaxListEntries + 1)
	if err != nil && !errors.Is(err, io.EOF) {
		return Result{}, err
	}
	if len(entries) > MaxListEntries {
		// 明确报错而不是截断：截断会让人误判目录内容。
		return Result{}, ErrTooMany
	}

	out := make([]Entry, 0, len(entries))
	for _, e := range entries {
		info, ierr := e.Info()
		if ierr != nil {
			// 单个条目读不到（例如被并发删除）不应让整个列表失败。
			out = append(out, Entry{Name: e.Name(), Kind: "unsupported"})
			continue
		}
		out = append(out, Entry{
			Name:       e.Name(),
			Kind:       kindOf(info.Mode()),
			Size:       info.Size(),
			ModifiedAt: info.ModTime().Unix(),
			Mode:       info.Mode().Perm().String(),
		})
	}
	return Result{Path: rel, Entries: out}, nil
}

func doRead(root *os.Root, req Request) (Result, error) {
	rel, err := cleanPath(req.Path)
	if err != nil {
		return Result{}, err
	}
	if rel == "" {
		return Result{}, ErrNotRegular
	}
	if err := checkNoSymlink(root, rel); err != nil {
		return Result{}, err
	}
	// 用 safeOpenFile 打开：逐级 openat(O_NOFOLLOW) + O_NONBLOCK，
	// 防止"文件被换成 FIFO/设备/符号链接"导致阻塞或读到别处。
	// 返回的 info 来自同一个 fd 的 fstat，是后续判断的唯一依据。
	f, info, err := safeOpenFile(root, rel, os.O_RDONLY)
	if err != nil {
		return Result{}, err
	}
	defer func() { _ = f.Close() }()

	if info.Size() > MaxFileBytes {
		return Result{}, ErrFileTooLarge
	}

	// 读时再次确认大小：文件可能在 fstat 之后仍被追加写入。
	data, err := io.ReadAll(io.LimitReader(f, MaxFileBytes+1))
	if err != nil {
		return Result{}, err
	}
	if len(data) > MaxFileBytes {
		return Result{}, ErrFileTooLarge
	}
	sum := sha256.Sum256(data)
	return Result{
		Path:       rel,
		DataBase64: base64.StdEncoding.EncodeToString(data),
		SHA256:     hex.EncodeToString(sum[:]),
		Size:       int64(len(data)),
	}, nil
}

func doWrite(root *os.Root, req Request) (Result, error) {
	rel, err := cleanPath(req.Path)
	if err != nil {
		return Result{}, err
	}
	if rel == "" {
		return Result{}, ErrBadPath
	}
	data, err := base64.StdEncoding.DecodeString(req.DataBase64)
	if err != nil {
		return Result{}, errors.New("内容不是合法的 base64")
	}
	if len(data) > MaxFileBytes {
		return Result{}, ErrFileTooLarge
	}
	if err := checkNoSymlink(root, rel); err != nil {
		return Result{}, err
	}

	sum := sha256.Sum256(data)
	hexSum := hex.EncodeToString(sum[:])

	// 契约：create_only=true 才新建；否则必须带 expected_sha256。
	// 这条与 Hub 侧一致，不能更宽松——否则绕过 Hub 直连 RPC 就能
	// 用"无 hash 的 write"覆盖文件，把校验保护整个跳过。
	if !req.CreateOnly && req.ExpectedSHA256 == "" {
		return Result{}, errors.New("覆盖已有文件必须提供 expected_sha256")
	}

	if req.CreateOnly {
		// 新建：O_EXCL 由内核原子拒绝"已存在"，无检查-创建窗口。
		f, oerr := safeCreateFile(root, rel)
		if oerr != nil {
			return Result{}, oerr
		}
		return writeAll(f, data, rel, hexSum)
	}

	// 覆盖已有文件：**不复用 O_TRUNC**。
	//
	// O_TRUNC 在打开瞬间清空文件，而校验发生在打开之后——
	// 一旦校验不通过，原内容已被抹掉，这是不可接受的破坏。
	// 正确顺序：以读写方式打开（不清空）→ 同一个 fd 上 fstat 校验 →
	// 读回内容算 hash 比对 → 全部通过才 Truncate(0) 并写入。
	f, info, oerr := safeOpenFile(root, rel, os.O_RDWR)
	if oerr != nil {
		// 文件已消失 → 报 notfound，**绝不**退回创建：
		// 调用方的前提是"文件存在且内容是我读到的那份"，若它已消失，
		// 悄悄建新文件会把"有人删了它"这件事掩盖掉。
		return Result{}, oerr
	}
	defer func() { _ = f.Close() }()

	if info.Size() > MaxFileBytes {
		return Result{}, ErrFileTooLarge
	}

	// 用同一个 fd 读回内容算 hash。
	h := sha256.New()
	if _, cerr := io.Copy(h, io.LimitReader(f, MaxFileBytes+1)); cerr != nil {
		return Result{}, cerr
	}
	if !strings.EqualFold(hex.EncodeToString(h.Sum(nil)), req.ExpectedSHA256) {
		return Result{}, ErrChecksum
	}

	// 校验通过后才清空并写入。
	if terr := f.Truncate(0); terr != nil {
		return Result{}, terr
	}
	if _, serr := f.Seek(0, io.SeekStart); serr != nil {
		return Result{}, serr
	}
	if _, werr := f.Write(data); werr != nil {
		return Result{}, werr
	}
	if syerr := f.Sync(); syerr != nil {
		return Result{}, syerr
	}
	return Result{Path: rel, SHA256: hexSum, Size: int64(len(data))}, nil
}

// writeAll 把内容写入一个新打开的文件并同步关闭。
func writeAll(f *os.File, data []byte, rel, hexSum string) (Result, error) {
	if _, werr := f.Write(data); werr != nil {
		_ = f.Close()
		return Result{}, werr
	}
	if serr := f.Sync(); serr != nil {
		_ = f.Close()
		return Result{}, serr
	}
	if cerr := f.Close(); cerr != nil {
		return Result{}, cerr
	}
	return Result{Path: rel, SHA256: hexSum, Size: int64(len(data))}, nil
}

func doMkdir(root *os.Root, req Request) (Result, error) {
	rel, err := cleanPath(req.Path)
	if err != nil {
		return Result{}, err
	}
	if rel == "" {
		return Result{}, ErrBadPath
	}
	// 父目录不能是 symlink。
	if err := checkNoSymlink(root, rel); err != nil {
		return Result{}, err
	}
	if _, serr := root.Lstat(rel); serr == nil {
		return Result{}, ErrExists
	}
	if err := root.Mkdir(rel, 0o755); err != nil {
		return Result{}, err
	}
	return Result{Path: rel, Message: "目录已创建"}, nil
}

func doRename(root *os.Root, req Request) (Result, error) {
	src, err := cleanPath(req.Path)
	if err != nil {
		return Result{}, err
	}
	dst, err := cleanPath(req.Destination)
	if err != nil {
		return Result{}, err
	}
	if src == "" || dst == "" {
		return Result{}, errors.New("不允许改名根目录")
	}
	if err := checkNoSymlink(root, src); err != nil {
		return Result{}, err
	}
	if err := checkNoSymlink(root, dst); err != nil {
		return Result{}, err
	}
	// 目标必须不存在：rename 覆盖会静默丢数据。
	if _, serr := root.Lstat(dst); serr == nil {
		return Result{}, ErrExists
	}
	if err := renameNoReplace(root, src, dst); err != nil {
		return Result{}, err
	}
	return Result{Path: dst, Message: "已重命名"}, nil
}

func doDelete(root *os.Root, req Request) (Result, error) {
	rel, err := cleanPath(req.Path)
	if err != nil {
		return Result{}, err
	}
	if rel == "" {
		return Result{}, errors.New("不允许删除根目录")
	}
	if err := checkNoSymlink(root, rel); err != nil {
		return Result{}, err
	}
	info, err := root.Lstat(rel)
	if err != nil {
		return Result{}, err
	}
	if info.IsDir() {
		// 只允许空目录，绝不递归：递归删除在卷里等价于"删库"。
		//
		// 用 openDirNoFollow 而不是 root.Open：后者会跟随符号链接，
		// 且更关键的是 O_DIRECTORY 能挡住"删之前被换成 FIFO/设备"的情况
		// ——打开 FIFO 会阻塞，容器只能靠超时兜底。
		dir, rerr := openDirNoFollow(root, rel)
		if rerr != nil {
			return Result{}, rerr
		}
		names, derr := dir.ReadDir(1)
		_ = dir.Close()
		if derr != nil && !errors.Is(derr, io.EOF) {
			return Result{}, derr
		}
		if len(names) > 0 {
			return Result{}, ErrNotEmpty
		}
		if rmerr := root.Remove(rel); rmerr != nil {
			return Result{}, rmerr
		}
		return Result{Path: rel, Message: "空目录已删除"}, nil
	}
	if !info.Mode().IsRegular() && info.Mode()&os.ModeSymlink == 0 {
		return Result{}, ErrNotRegular
	}
	if !nlinkOK(nlinkOf(info)) {
		return Result{}, ErrHardlink
	}
	if rmerr := root.Remove(rel); rmerr != nil {
		return Result{}, rmerr
	}
	return Result{Path: rel, Message: "文件已删除"}, nil
}
