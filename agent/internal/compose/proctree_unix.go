//go:build !windows

package compose

// proctree_unix.go：Linux/macOS 上按进程组终止 compose 子进程树。
//
// ============================ 为什么必须杀进程组 ============================
//
// exec.CommandContext 默认只 kill 直接子进程。真实场景里 compose 会 fork
// 子进程（拉取、解包），它们继承同一批管道描述符；只杀父进程时，
// 这些"孙进程"还活着并占着管道，cmd.Wait() 会一直等 copy 结束——
// 表现为任务永远 running，直到孙进程自己退出（可能几分钟）。
//
// 让子进程成为新进程组的组长，然后 kill(-pgid) 一次带走整棵树。

import (
	"os/exec"
	"syscall"
)

// sysProcAttrSetPgid 让子进程自成一个进程组。
func sysProcAttrSetPgid() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setpgid: true}
}

// killProcessGroup 杀掉整个进程组。
//
// 用负 pid 是 POSIX 的语义：信号发给该进程组的所有成员。
// 进程可能已退出（ESRCH），此时忽略错误即可。
func killProcessGroup(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	pgid, err := syscall.Getpgid(cmd.Process.Pid)
	if err != nil {
		// 拿不到组号时退回单进程 kill，至少不会留下主进程。
		return cmd.Process.Kill()
	}
	// 先 TERM 让 compose 有机会清理，再兜底 KILL 不会在这里做——
	// exec 的 Wait 会很快回收，残留的孙进程已失去管道，不影响退出。
	return syscall.Kill(-pgid, syscall.SIGKILL)
}
