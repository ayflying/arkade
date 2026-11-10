package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"arkade/agent/internal/config"
	"arkade/agent/internal/handler"
	"arkade/agent/internal/runtime"
)

// cmdRun 实现 `arkade-agent run`：服务模式，连回 Hub。
//
// 生命周期：
//
//	读配置 → 校验 → 建运行时 → 建注册表 → 建连接器 → 阻塞运行
//	收到 SIGINT/SIGTERM → 取消 context → 正常退出（systemd 依赖这个行为）
func cmdRun(args []string, stderr io.Writer) int {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	fs.SetOutput(stderr)
	cfgPath := fs.String("c", "", "配置文件路径（必填）")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}

	if *cfgPath == "" {
		fmt.Fprintln(stderr, "错误: run 子命令必须用 -c 指定配置文件，例如: arkade-agent run -c /etc/arkade/agent.toml")
		return exitUsage
	}

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		fmt.Fprintf(stderr, "错误: %v\n", err)
		return exitFail
	}
	// 服务模式才需要 hub_url/token；本地命令刻意不做这项校验（见 config.Validate 注释）。
	if err := cfg.Validate(); err != nil {
		fmt.Fprintf(stderr, "错误: %v\n", err)
		return exitFail
	}

	log, err := newServiceLogger(cfg)
	if err != nil {
		fmt.Fprintf(stderr, "错误: %v\n", err)
		return exitFail
	}

	// 安全提醒：如果凭据是从配置文件里读到（而不是环境变量），说明令牌已经落盘，
	// 它会随配置文件一起被备份、复制、下发。这里不拒绝启动（那会让老部署突然起不来），
	// 但必须显式警告，否则这个坏习惯会一直悄悄存在。
	if config.TokenFromFile(*cfgPath) {
		log.Warn("安全提醒：检测到配置文件里写有 token，建议改为环境变量 ARKADE_AGENT_TOKEN 注入。" +
			"配置文件容易被备份或复制，明文令牌会随之扩散。")
	}

	rt, probeErr := createRuntime(cfg)
	if probeErr != nil {
		// 服务模式下不因为运行时探测失败就退出：Agent 仍应连上 Hub 并上报
		// "这台机器引擎不可用"，否则运维只能上机器看日志才知道原因。
		log.Warn("运行时探测失败，将以「无运行时」状态继续运行", "error", probeErr)
		rt = runtime.NewNone("")
	}
	info, _ := rt.Info(context.Background())
	if info.Kind == runtime.KindNone {
		log.Warn("本机未检测到 Docker / Podman，容器相关命令将返回 unsupported")
	} else {
		log.Info("运行时探测完成",
			"类型", string(info.Kind), "版本", info.Version,
			"端点", info.Endpoint, "rootless", info.Rootless)
	}

	// ============================ 生命周期上下文必须先就绪 ============================
	// systemd stop 发 SIGTERM，Ctrl+C 发 SIGINT，两者都走优雅退出。
	//
	// 它必须**先于**任何长任务子系统创建：编排镜像更新任务挂在它上面，
	// 若反过来（先建任务表、后建 ctx），任务表拿到的是 nil/Background，
	// 优雅退出时就不会取消正在跑的 compose 子进程，留下孤儿进程继续拉镜像。
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// 注册表在服务模式与本地模式里都是同一份构造方式，见 localSetup。
	reg := handler.NewRegistry()
	imageMgr := buildImageTransferManager(cfg, rt)
	composeMgr := buildComposeManager(cfg, rt)
	// 把 Agent 生命周期交给 compose：镜像更新任务据此随退出一起取消。
	composeMgr.RootCtx = ctx
	handler.RegisterDefaultsWithTransfer(reg, rt, composeMgr, imageMgr, log)
	// 取消全部进行中的镜像更新任务（含其 compose 子进程）。
	// 注册在 Registry 上而不是单独 defer：这样任何持有 Registry 的
	// 部署路径（服务模式 / 本地模式）都会经过它，不会漏接线。
	defer reg.Close()

	agent, err := buildLinkAgent(cfg, log, rt, reg)
	if err != nil {
		fmt.Fprintf(stderr, "错误: 构造连接器失败: %v\n", err)
		return exitFail
	}

	// 后台清扫传输临时区：没有它，Hub 崩溃或浏览器关闭后留下的几 GB 归档
	// 会永久占着配额，后续传输全被拒且用户看不出原因。
	if imageMgr != nil {
		go imageMgr.RunSweeper(ctx)
		// 优雅退出时清掉本进程留下的临时归档（见 Close 的说明）：
		// 不清理的话它们要等到"下次启动"才会被 cleanupOrphans 收走，
		// 而下次启动可能是几天后。
		defer imageMgr.Close()
	}

	log.Info("Agent 启动", "版本", Version, "hub", cfg.HubURL, "数据目录", cfg.DataDir)
	if err := agent.Run(ctx); err != nil {
		log.Error("连接循环异常退出", "error", err)
		return exitFail
	}
	log.Info("Agent 已停止")
	return exitOK
}
