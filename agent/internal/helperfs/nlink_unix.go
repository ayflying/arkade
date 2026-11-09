//go:build unix

package helperfs

import (
	"os"
	"syscall"
)

// nlinkOf 返回文件系统的硬链接数。
//
// 多硬链接是**逃逸通道**：卷内的一个文件可能有另一个名字在卷外。
// 写入它等于改到了卷外内容，删除它也可能影响别处正在使用的数据。
// 因此 nlink != 1 一律拒写、拒删、拒读。
//
// 取不到时返回 0（不是 1）：0 会被 nlinkOK 判为"不允许"，
// 也就是 fail closed。返回 1 会让"取不到"变成"看起来没有硬链接"，
// 那是 fail open，正是要避免的方向。
func nlinkOf(info os.FileInfo) uint64 {
	if st, ok := info.Sys().(*syscall.Stat_t); ok {
		return uint64(st.Nlink)
	}
	return 0
}
