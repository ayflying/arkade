package embedded

import "arkade/agent/internal/helperfs"

// RunHelper 分发注入 helper 容器的隐藏子命令。
// Hub main 必须在配置解析、数据库初始化和服务启动之前调用：
//
//	if handled, code := embedded.RunHelper(os.Args[1:]); handled { os.Exit(code) }
//
// helper 使用 os.Executable() 注入当前 Hub 二进制，因此不能省略此入口。
func RunHelper(args []string) (handled bool, exitCode int) {
	if len(args) == 0 {
		return false, 0
	}
	switch args[0] {
	case "volume-file-helper":
		return true, helperfs.Run()
	case "compose-source-helper":
		return true, helperfs.RunComposeSource()
	default:
		return false, 0
	}
}
