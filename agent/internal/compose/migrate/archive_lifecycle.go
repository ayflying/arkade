package migrate

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

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
	if !validExportID(id) {
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
