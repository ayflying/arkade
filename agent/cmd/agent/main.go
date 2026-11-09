// Command agent 是 arkade-agent 的进程入口。
//
// 这个文件刻意保持极短：只负责把 os.Args 传给 cli.Main 并把退出码交给操作系统。
// 所有逻辑（子命令分派、配置、连接、处理器）都在 internal/ 下的包中，
// 这样 main 包不需要可测性妥协，测试也不必跨包调用 main。
package main

import (
	"os"

	"arkade/agent/internal/cli"
)

func main() {
	// 直接透传退出码：脚本与 systemd 依赖 0/1/2 的区分（见 cli 包的约定）。
	os.Exit(cli.Main(os.Args[1:], os.Stdout, os.Stderr))
}
