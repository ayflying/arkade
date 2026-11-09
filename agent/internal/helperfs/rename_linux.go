//go:build linux

package helperfs

// rename_linux.go：用 renameat2 做**不覆盖**的改名。
//
// 为什么不用 os.Root.Rename：
//   本仓库 go.mod 声明 go 1.24，而 Root.Rename 是 Go 1.25 才加的 API。
//   开发机上装了更新的 Go 会编译通过，但 Docker 里用 1.24 构建会直接失败
//   ——这种"本地能过、镜像里挂"的问题必须在源头避免。
//
// 为什么不用 syscall.Rename 拼宿主机路径：
//   os.Root 的全部价值就是让路径解析不逃出根目录。一旦把 Root.Name()
//   与相对路径拼成宿主机绝对路径再调 rename，中间任何一段目录被替换成
//   符号链接，就会把文件改名到根之外——正好绕过了我们在别处做的所有检查。
//
// 正确做法：从 Root 上"打开"两个父目录，拿到**目录 fd**，
// 再用 renameat2 的 fd 相对形式改名。内核按 fd 解析，不重新遍历路径，
// 因此不存在"检查后被替换"的竞态窗口。
//
// RENAME_NOREPLACE 让"目标已存在"由内核原子拒绝，
// 而不是先 Lstat 再 Rename（后者有时间窗口，可能覆盖掉并发创建的文件）。

import (
	"errors"
	"os"
	"path"
	"syscall"

	"golang.org/x/sys/unix"
)

// renameNoReplace 在 root 内把 oldRel 改名为 newRel，目标必须不存在。
func renameNoReplace(root *os.Root, oldRel, newRel string) error {
	oldDirRel, oldBase := path.Split(oldRel)
	newDirRel, newBase := path.Split(newRel)
	oldDirRel = trimSlash(oldDirRel)
	newDirRel = trimSlash(newDirRel)

	if oldBase == "" || newBase == "" {
		return ErrBadPath
	}
	if oldBase == ".." || newBase == ".." || oldBase == "." || newBase == "." {
		return ErrBadPath
	}

	// 打开父目录：逐级 openat(O_NOFOLLOW)，不跟随任何符号链接段。
	oldDir, err := openDirNoFollow(root, oldDirRel)
	if err != nil {
		return err
	}
	defer func() { _ = oldDir.Close() }()

	newDir := oldDir
	if newDirRel != oldDirRel {
		newDir, err = openDirNoFollow(root, newDirRel)
		if err != nil {
			return err
		}
		defer func() { _ = newDir.Close() }()
	}

	err = unix.Renameat2(
		int(oldDir.Fd()), oldBase,
		int(newDir.Fd()), newBase,
		unix.RENAME_NOREPLACE,
	)
	if err == nil {
		return nil
	}
	// 内核/文件系统不支持 RENAME_NOREPLACE 时**直接明确不支持**。
	//
	// 这里绝不能退回"先 Lstat 再 Renameat"：
	// 那个窗口里并发创建的目标文件会被静默覆盖，正是本函数要防的事。
	// 与其给一个"看起来能用、偶尔吞数据"的实现，不如明确报错，
	// 让用户换文件系统（overlay2/ext4/xfs 都支持）。
	if errors.Is(err, syscall.ENOSYS) || errors.Is(err, syscall.EINVAL) ||
		errors.Is(err, syscall.EOPNOTSUPP) {
		return ErrNoAtomicRename
	}
	if errors.Is(err, syscall.EEXIST) {
		return ErrExists
	}
	return err
}
