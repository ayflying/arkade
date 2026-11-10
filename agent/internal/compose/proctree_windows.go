//go:build windows

package compose

// proctree_windows.go：Windows 上没有进程组，退回单进程终止。
//
// Agent 生产环境是 Linux（compose 依赖 docker.sock），
// 这里只需保证编译通过且行为不退化为"完全不取消"。

import (
	"os/exec"
	"syscall"
)

// sysProcAttrSetPgid 在 Windows 上不需要额外属性。
func sysProcAttrSetPgid() *syscall.SysProcAttr {
	return nil
}

// killProcessGroup 终止直接子进程。
func killProcessGroup(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	return cmd.Process.Kill()
}
