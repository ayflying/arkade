package imagetransfer

// storage.go 管理传输临时区（transfers）的目录与文件。
//
// ============================ 安全边界 ============================
//
// 传输临时区里放的是**镜像归档**：它们可能含私有配置、密钥文件、
// 甚至完整的应用代码。因此：
//
//  1. **只用专用子目录** <data_dir>/transfers/<task_id>/，不与
//     stacks/、backups/ 混放。混放的危险是清理逻辑稍有不慎就会删到
//     用户的编排与备份——那是不可恢复的数据丢失。
//
//  2. **权限 0600**（文件）与 0700（目录）。归档不是可公开内容。
//
//  3. **防 symlink**：清理与写入前都用 Lstat 检查，绝不跟随符号链接。
//     若攻击者能在 data_dir 里放一个指向 / 的符号链接，
//     一次"清理过期传输"就可能变成"删除系统目录"。
//
//  4. **启动时只清理 transfers 目录**，且只清理
//     "没有任何活动任务对应"的子目录。

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	// transfersDirName 是 data_dir 下的专用子目录名。
	transfersDirName = "transfers"
	// archiveFileName 是归档文件的固定名字。
	archiveFileName = "archive.tar"
	// dirMode / archiveMode 是权限位。
	dirMode     os.FileMode = 0o700
	archiveMode os.FileMode = 0o600
)

// TransfersDirName 返回 data_dir 下传输临时区的目录名。
//
// 暴露它是因为**编排迁移的卷归档要落进同一个临时区**：
// 两类归档共享 TTL、数量上限与"启动时清理无主残留"这套规则，
// 各开一个目录就会出现两套清理逻辑，漏一套就永久占盘。
func TransfersDirName() string { return transfersDirName }

// storage 负责物理文件操作。
type storage struct {
	// root 是 transfers 目录的绝对路径。
	root string
}

// newStorage 确保 transfers 目录存在并返回句柄。
//
// ============================ 为什么先 Lstat 再 MkdirAll ============================
//
// 这一点非常重要，不能图省事直接 MkdirAll：
//
//	如果 <data_dir>/transfers 本身是一个**符号链接**（指向 / 或 /var 等），
//	MkdirAll 会因为"目标已存在"而成功返回，我们随后就会：
//	  - 把归档写进链接指向的真实目录；
//	  - 更糟的是启动时 cleanupOrphans 会去**删链接目标里的目录**——
//	    一次"清理残留传输"就变成了删除系统目录。
//
// data_dir 来自可信配置（由运维指定），但 transfers 这个名字之下的
// 东西是**可被替换的**（同机器其它进程、错误的备份恢复、攻击者）。
// 因此这里把 transfers 当作不可信输入处理：
//
//  1. Lstat（不跟随链接）看它到底是什么；
//  2. 是符号链接 → 直接拒绝，不做任何写入/清理；
//  3. 存在但不是目录 → 拒绝；
//  4. 只有"确实是一个普通目录"或"不存在"才继续。
func newStorage(dataDir string) (*storage, error) {
	if strings.TrimSpace(dataDir) == "" {
		return nil, errors.New("数据目录为空，无法建立传输临时区")
	}
	root := filepath.Join(dataDir, transfersDirName)

	info, err := os.Lstat(root)
	switch {
	case err == nil:
		if info.Mode()&os.ModeSymlink != 0 {
			// 不跟随、不删除、不写入。给出可操作的中文说明。
			return nil, fmt.Errorf("传输临时目录 %s 是符号链接，出于安全考虑拒绝使用（请先手工确认并移除它）", transfersDirName)
		}
		if !info.IsDir() {
			return nil, fmt.Errorf("传输临时目录 %s 已存在但不是目录，拒绝使用", transfersDirName)
		}
	case os.IsNotExist(err):
		// 不存在才创建。
		if mkErr := os.MkdirAll(root, dirMode); mkErr != nil {
			return nil, fmt.Errorf("创建传输临时目录失败: %w", mkErr)
		}
	default:
		return nil, fmt.Errorf("检查传输临时目录失败: %w", err)
	}

	// 创建后再确认一次是目录（防止创建过程中被替换）。
	// 这里仍然用 Lstat：若此刻变成了链接，同样拒绝。
	info2, err2 := os.Lstat(root)
	if err2 != nil {
		return nil, fmt.Errorf("检查传输临时目录失败: %w", err2)
	}
	if info2.Mode()&os.ModeSymlink != 0 || !info2.IsDir() {
		return nil, fmt.Errorf("传输临时目录 %s 状态异常，拒绝使用", transfersDirName)
	}

	// 已存在时会继承旧权限，显式收紧一次（MkdirAll 不会改已有目录的模式）。
	if chErr := os.Chmod(root, dirMode); chErr != nil {
		return nil, fmt.Errorf("设置传输临时目录权限失败: %w", chErr)
	}
	return &storage{root: root}, nil
}

// taskDir 返回某个任务的目录路径。
//
// 路径只由 transfers 根 + 已校验的 task_id 组成，并**再做一次越界检查**：
// 即使将来 id 生成规则被改松，这里还能兜住。
func (s *storage) taskDir(taskID string) (string, error) {
	if !validTaskID(taskID) {
		return "", ErrTaskNotFound
	}
	dir := filepath.Join(s.root, taskID)
	// 防越界：拼出来的路径必须仍在 root 之下。
	rel, err := filepath.Rel(s.root, dir)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", ErrTaskNotFound
	}
	return dir, nil
}

// createArchive 为任务创建归档文件，返回路径与句柄（0600）。
//
// 用 O_EXCL：若同名文件已存在说明有异常（id 撞车或残留），
// 此时**拒绝**而不是覆盖——覆盖会静默毁掉一个可能正在被读的归档。
func (s *storage) createArchive(taskID string) (string, *os.File, error) {
	dir, err := s.dirFor(taskID)
	if err != nil {
		return "", nil, err
	}
	path := filepath.Join(dir, archiveFileName)

	// 归档路径本身也必须是"不存在"或"普通文件"，不能是链接。
	// 若已存在一个指向别处的链接，O_EXCL 会失败（这没问题），
	// 但在有些平台上 O_CREATE|O_EXCL 对已存在的链接的报错不直观，
	// 这里显式检查一次，给出明确原因。
	if info, lerr := os.Lstat(path); lerr == nil && info.Mode()&os.ModeSymlink != 0 {
		return "", nil, errors.New("归档路径是符号链接，已拒绝使用")
	}

	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, archiveMode)
	if err != nil {
		return "", nil, fmt.Errorf("创建归档文件失败: %w", err)
	}
	return path, f, nil
}

// dirFor 创建（必要时）任务目录，并拒绝任何形式的链接替换。
//
// 与 newStorage 同样的理由：transfers 下的名字虽然由我们生成，
// 但目录**内容**可被同机器其它进程替换。若某个 task 目录被换成
// 指向 /etc 的符号链接，一次写入就往系统里落了个归档文件，
// 而 cleanupOrphans 更会去删链接目标——所以每建一次都要 Lstat。
func (s *storage) dirFor(taskID string) (string, error) {
	dir, err := s.taskDir(taskID)
	if err != nil {
		return "", err
	}

	info, lerr := os.Lstat(dir)
	switch {
	case lerr == nil:
		if info.Mode()&os.ModeSymlink != 0 {
			return "", errors.New("任务目录是符号链接，已拒绝使用")
		}
		if !info.IsDir() {
			return "", errors.New("任务目录已存在但不是目录，已拒绝使用")
		}
		return dir, nil
	case os.IsNotExist(lerr):
		if mkErr := os.MkdirAll(dir, dirMode); mkErr != nil {
			return "", fmt.Errorf("创建任务目录失败: %w", mkErr)
		}
		// 建完复核：创建与使用之间被替换成链接的情况也要挡住。
		after, aerr := os.Lstat(dir)
		if aerr != nil {
			return "", fmt.Errorf("检查任务目录失败: %w", aerr)
		}
		if after.Mode()&os.ModeSymlink != 0 || !after.IsDir() {
			return "", errors.New("任务目录状态异常，已拒绝使用")
		}
		return dir, nil
	default:
		return "", fmt.Errorf("检查任务目录失败: %w", lerr)
	}
}

// openArchive 以只读方式打开归档，返回句柄与大小。
func (s *storage) openArchive(taskID string) (*os.File, int64, error) {
	dir, err := s.taskDir(taskID)
	if err != nil {
		return nil, 0, err
	}
	path := filepath.Join(dir, archiveFileName)
	// 防 symlink：Lstat 不跟随链接，发现是链接直接拒绝。
	info, err := os.Lstat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, 0, ErrTaskNotFound
		}
		return nil, 0, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil, 0, errors.New("归档路径是符号链接，已拒绝读取")
	}
	if !info.Mode().IsRegular() {
		return nil, 0, errors.New("归档路径不是普通文件，已拒绝读取")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, 0, err
	}
	return f, info.Size(), nil
}

// removeTask 删除某个任务的整个目录。
//
// 逐项校验后用 RemoveAll：先确认目录本身不是符号链接，
// 避免"删掉链接指向的真实目录"。
func (s *storage) removeTask(taskID string) error {
	dir, err := s.taskDir(taskID)
	if err != nil {
		return err
	}
	info, statErr := os.Lstat(dir)
	if statErr != nil {
		if os.IsNotExist(statErr) {
			return nil
		}
		return statErr
	}
	if info.Mode()&os.ModeSymlink != 0 {
		// 只删链接本身，绝不进入它。
		return os.Remove(dir)
	}
	return os.RemoveAll(dir)
}

// usage 返回当前 transfers 目录下所有归档的总字节数。
//
// 用于总量配额判断。只统计 **Lstat 得到的普通文件**：
// 跳过符号链接与子目录里的异常项，避免被一个链接指向的巨型文件
// 把配额算歪（那会让配额判断变得既能被绕过、又会误拒）。
func (s *storage) usage() int64 {
	var total int64
	entries, err := os.ReadDir(s.root)
	if err != nil {
		return 0
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue // 根下不该有文件，忽略
		}
		dir := filepath.Join(s.root, e.Name())
		info, lerr := os.Lstat(dir)
		if lerr != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			continue
		}
		path := filepath.Join(dir, archiveFileName)
		fi, ferr := os.Lstat(path)
		if ferr != nil || fi.Mode()&os.ModeSymlink != 0 || !fi.Mode().IsRegular() {
			continue
		}
		total += fi.Size()
	}
	return total
}

// cleanupOrphans 清理 transfers 目录下"没有活动任务对应"的子目录。
//
// 只在**启动时**与 Close 时调用。清理不依赖记忆中的任务表——
// 进程重启后那份表是空的，但磁盘上的残留还在
// （进程被 kill 时 defer 不会执行）。
//
// ============================ 安全：绝不跟随链接 ============================
//
// 这是本项目里最危险的一段代码：它在删目录。若允许跟随符号链接，
// 一个 <transfers>/<32位hex> → /  的链接就能让"清理残留传输"
// 变成"删除根目录下的所有东西"。
//
// 因此规则非常保守：
//   - 名字必须先通过 validTaskID（不符合的直接跳过，绝不删）；
//   - 必须是 **Lstat** 得到的普通目录（ModeDir），链接一律只删链接本身；
//   - 普通文件也不删（那不是我们建的，可能是用户手工放的东西）；
//   - 任何一步出错都跳过该项并继续，不让一个坏目录阻止整体启动。
func (s *storage) cleanupOrphans(active map[string]bool) int {
	entries, err := os.ReadDir(s.root)
	if err != nil {
		return 0
	}
	removed := 0
	for _, e := range entries {
		name := e.Name()
		if active[name] {
			continue
		}
		// 名字不符合任务 id 规则：不是我们建的，**不动它**。
		if !validTaskID(name) {
			continue
		}
		dir := filepath.Join(s.root, name)
		info, lerr := os.Lstat(dir)
		if lerr != nil {
			continue
		}
		switch {
		case info.Mode()&os.ModeSymlink != 0:
			// 只删链接本身，绝不进入它指向的位置。
			if os.Remove(dir) == nil {
				removed++
			}
		case info.IsDir():
			if os.RemoveAll(dir) == nil {
				removed++
			}
		default:
			// 普通文件：不是我们建的（我们只建目录），跳过。
		}
	}
	return removed
}

// validTaskID 校验任务 id 形态。
//
// 只允许小写十六进制：id 由 newTaskID 生成，格式固定。
// 这条校验同时是路径安全的第一道防线（禁止分隔符、禁止 ".."）。
func validTaskID(id string) bool {
	if len(id) != 32 {
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

// newTaskID 生成任务 id（16 字节随机十六进制）。
//
// 用随机而不是自增：Agent 重启后自增会撞车，而撞车会让
// "清理残留目录"误删新任务的目录。
func newTaskID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// 随机源不可用时退化为纳秒时间戳：宁可 id 可预测，
		// 也不能让整个传输功能因为拿不到随机数就不可用。
		return fmt.Sprintf("%032x", uint64(time.Now().UnixNano()))
	}
	return hex.EncodeToString(b[:])
}

// copyExactly 从 r 精确复制 n 字节到 w，返回实际复制量。
//
// 用于把上传的分块写入归档；同时可作为流式哈希的入口
// （调用方包一层 io.TeeReader 即可边写边算，不需要二次读盘）。
func copyExactly(w io.Writer, r io.Reader, n int64) (int64, error) {
	return io.CopyN(w, r, n)
}
