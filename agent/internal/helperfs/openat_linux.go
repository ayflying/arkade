//go:build linux

package helperfs

// openat_linux.go：卷内安全打开。
//
// ============================ 为什么要逐级 openat ============================
//
// root.Open("a/b/c") 内部按路径解析。即使 os.Root 保证不逃出根目录，
// 解析过程中仍会**跟随符号链接**（只要最终不出 root）。
// 对一个"打开父目录再改名"的操作，这意味着拿到的目录 fd
// 可能不是用户以为的那个。
//
// 逐级 openat 后每段都用 O_NOFOLLOW|O_DIRECTORY：
//   - O_NOFOLLOW：最后一段是符号链接就 ELOOP，不跟随；
//   - O_DIRECTORY：不是目录就 ENOTDIR，顺带挡住 FIFO/设备。
//
// ============================ 为什么文件要 O_NONBLOCK ============================
//
// 打开 FIFO 时，若没有写端在场，open 会**永久阻塞**。
// 恶意或意外地把 "config.json" 换成同名 FIFO，就能让 helper 卡死
// （虽然有 60s 超时兜底，但那期间容器一直占着资源，操作也拿不到明确错误）。
//
// O_NONBLOCK 让 FIFO 的 open 立即返回而不是等写端；
// 之后 fstat 发现不是普通文件就立刻拒绝。
// 对普通文件，O_NONBLOCK 在 Linux 上无任何副作用（不改变读写阻塞语义）。
//
// ============================ 残余边界（如实说明） ============================
//
// 这**不是**与恶意业务进程对抗的竞态证明。业务进程对卷有完全写权限，
// 若它在我们操作期间移动目录甚至把我们已打开的 fd 换走，
// 仍有理论窗口——因为我们只有 POSIX 提供的能力。
//
// 能保证的是：
//  1. 所有路径解析不出 root（root containment）；
//  2. 每一段都不跟随符号链接（no-follow）；
//  3. nlink/大小/内容 hash 都在**同一个 fd** 上校验，
//     校验对象与操作对象是同一个 inode；
//  4. 打开非普通文件不会阻塞。
//
// 不能保证"外部进程操作期间完全不改动卷"。
// 因此 expected_sha256 是**尽力而为的冲突检测**，不是跨进程原子 CAS。

import (
	"errors"
	"os"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

// openDirNoFollow 在 root 内逐级打开目录 rel（""=根），返回目录 fd。
func openDirNoFollow(root *os.Root, rel string) (*os.File, error) {
	rel = trimSlash(rel)
	if rel == "" || rel == "." {
		// 根目录本身：它不可能是链接。
		return root.Open(".")
	}

	cur, err := root.Open(".")
	if err != nil {
		return nil, err
	}

	for _, seg := range strings.Split(rel, "/") {
		if seg == "" || seg == "." || seg == ".." {
			_ = cur.Close()
			return nil, ErrBadPath
		}
		fd, oerr := unix.Openat(
			int(cur.Fd()), seg,
			unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_DIRECTORY|unix.O_CLOEXEC,
			0,
		)
		_ = cur.Close()
		if oerr != nil {
			if errors.Is(oerr, syscall.ELOOP) {
				return nil, ErrSymlink
			}
			return nil, oerr
		}
		cur = os.NewFile(uintptr(fd), seg)
		if cur == nil {
			_ = unix.Close(fd)
			return nil, errors.New("无法包装目录描述符")
		}
	}
	return cur, nil
}

// safeOpenFile 在 root 内安全打开一个**普通文件**。
//
// flags 只应是 O_RDONLY 或 O_RDWR；创建/截断类标志一律拒绝——
// 创建必须走 create_only 路径（O_EXCL），截断必须在校验通过后
// 用同一个 fd 的 Truncate 完成（见 doWrite 的说明）。
//
// 返回值已确认：最后一段不是符号链接、是普通文件、nlink == 1。
func safeOpenFile(root *os.Root, rel string, flags int) (*os.File, os.FileInfo, error) {
	if flags&(os.O_CREATE|os.O_TRUNC|os.O_EXCL|os.O_APPEND) != 0 {
		// 防御性检查：这些标志会绕过"先校验后写"的流程。
		return nil, nil, errors.New("内部错误：不允许的打开标志")
	}
	dirRel, base := splitRel(rel)
	dir, err := openDirNoFollow(root, dirRel)
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = dir.Close() }()

	// O_NONBLOCK 的目的见文件头：避免 FIFO 的 open 阻塞。
	// O_NOFOLLOW 避免最后一段是符号链接时被跟随。
	fd, oerr := unix.Openat(
		int(dir.Fd()), base,
		flags|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC,
		0,
	)
	if oerr != nil {
		if errors.Is(oerr, syscall.ELOOP) {
			return nil, nil, ErrSymlink
		}
		return nil, nil, oerr
	}
	f := os.NewFile(uintptr(fd), base)
	if f == nil {
		_ = unix.Close(fd)
		return nil, nil, errors.New("无法包装文件描述符")
	}

	// fstat 校验必须在**同一个 fd** 上做：
	// 用路径再 stat 会引入"检查后被替换"的窗口。
	info, serr := f.Stat()
	if serr != nil {
		_ = f.Close()
		return nil, nil, serr
	}
	if !info.Mode().IsRegular() {
		_ = f.Close()
		return nil, nil, ErrNotRegular
	}
	if !nlinkOK(nlinkOf(info)) {
		_ = f.Close()
		return nil, nil, ErrHardlink
	}
	return f, info, nil
}

// safeCreateFile 在 root 内新建普通文件（O_EXCL，目标必须不存在）。
//
// 同样逐级 openat + O_NOFOLLOW，父目录不含符号链接。
func safeCreateFile(root *os.Root, rel string) (*os.File, error) {
	dirRel, base := splitRel(rel)
	if base == "" {
		return nil, ErrBadPath
	}
	dir, err := openDirNoFollow(root, dirRel)
	if err != nil {
		return nil, err
	}
	defer func() { _ = dir.Close() }()

	fd, oerr := unix.Openat(
		int(dir.Fd()), base,
		unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC,
		0o644,
	)
	if oerr != nil {
		if errors.Is(oerr, syscall.EEXIST) {
			return nil, ErrExists
		}
		if errors.Is(oerr, syscall.ELOOP) {
			return nil, ErrSymlink
		}
		return nil, oerr
	}
	f := os.NewFile(uintptr(fd), base)
	if f == nil {
		_ = unix.Close(fd)
		return nil, errors.New("无法包装文件描述符")
	}
	return f, nil
}

// splitRel 把相对路径拆成目录部分与文件名部分。
func splitRel(rel string) (dir, base string) {
	rel = trimSlash(rel)
	i := strings.LastIndexByte(rel, '/')
	if i < 0 {
		return "", rel
	}
	return rel[:i], rel[i+1:]
}

// trimSlash 去掉结尾斜杠。
func trimSlash(s string) string {
	for len(s) > 0 && s[len(s)-1] == '/' {
		s = s[:len(s)-1]
	}
	return s
}
