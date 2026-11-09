//go:build !linux

package helperfs

import (
	"os"
)

// nlinkOf 在非 Linux 平台**不可用**，返回 0（而不是"看起来正常"的 1）。
//
// 返回 1 会让调用方以为"没有硬链接"从而放行写入，这是**危险的默认值**：
// 在容器外的开发机上测试时一切正常，一旦真跑在有硬链接的卷上就会绕过检查。
//
// 因此这里返回 0 并配合检查逻辑：nlink == 0 视为"无法确认"，
// 由调用点统一按 ErrHardlink 拒绝（见 fs.go 的 nlinkOK）。
// 首批 helper 只在 Linux 容器内运行，非 Linux 仅用于开发机编译。
func nlinkOf(os.FileInfo) uint64 { return 0 }
