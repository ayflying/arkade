// Package cli 实现 arkade-agent 的命令行入口（决策 D-05 的首期离线管理入口）。
//
// 结构约定：
//   - 一个二进制、多子命令：run 走服务模式（连 Hub），其余子命令走本地模式；
//   - 本地子命令**不经过 Hub**，直接构造信封交给 handler.Registry ——
//     与 Hub 下行请求共用同一张注册表，保证离线与在线的行为逐字节一致；
//   - 本包只负责"参数 → 信封 → 表格/文本输出"，任何业务判断都在 handler 里。
package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"time"

	"arkade/agent/internal/config"
	"arkade/agent/internal/handler"
	"arkade/agent/internal/link"
	"arkade/agent/internal/logging"
	"arkade/agent/internal/runtime"
	"arkade/protocol/envelope"
)

// Version 由构建时通过 -ldflags 注入；源码里留一个默认值便于本地调试。
var Version = "0.1.0-dev"

// 退出码约定。
//
// 与 shell 惯例保持一致：0 成功、1 运行期失败（如运行时连不上）、2 用法错误
// （参数写错）。脚本据此分流，不要把所有失败都塞成 1。
const (
	exitOK    = 0
	exitFail  = 1
	exitUsage = 2
)

// usage 是顶层帮助文本。
const usage = `arkade-agent —— Arkade 被管服务器上的代理程序

用法：
  arkade-agent run   -c <config.toml>     以服务模式运行（主动连回 Hub）
  arkade-agent ps    [-c <config.toml>]   本地列出容器（不连 Hub）
  arkade-agent start <id|name>            本地启动容器
  arkade-agent stop  <id|name> [--force]  本地停止容器
  arkade-agent logs  <id|name> [-n 100]   本地查看容器日志
  arkade-agent info                       本地查看运行时信息
  arkade-agent version                    打印版本

说明：
  本地子命令完全离线可用（决策 D-05），直接操作本机运行时；
  -c 省略时使用默认值（数据目录 /opt/arkade/agent，运行时自动探测）。
`

// Main 是 CLI 的唯一入口，返回进程退出码。
//
// 把 io.Writer 作为参数注入（而不是直接 os.Stdout），是为了让输出格式能被单测断言。
func Main(args []string, stdout, stderr io.Writer) int {
	// 顶层无子命令时打印用法并以用法错误退出，避免"静默什么都不做"。
	if len(args) == 0 {
		fmt.Fprint(stdout, usage)
		return exitUsage
	}

	cmd, rest := args[0], args[1:]
	switch cmd {
	case "run":
		return cmdRun(rest, stderr)
	case "ps", "list", "ls":
		return cmdPS(rest, stdout, stderr)
	case "start":
		return cmdContainerOp(rest, stdout, stderr, true)
	case "stop":
		return cmdContainerOp(rest, stdout, stderr, false)
	case "logs":
		return cmdLogs(rest, stdout, stderr)
	case "info":
		return cmdInfo(rest, stdout, stderr)
	case "version", "-v", "--version":
		fmt.Fprintf(stdout, "arkade-agent %s (协议版本 %d)\n", Version, envelope.Version)
		return exitOK
	case "help", "-h", "--help":
		fmt.Fprint(stdout, usage)
		return exitOK
	default:
		fmt.Fprintf(stderr, "未知子命令: %s\n\n%s", cmd, usage)
		return exitUsage
	}
}

// localSetup 是本地子命令共用的初始化结果。
type localSetup struct {
	rt       runtime.Runtime
	registry *handler.Registry
	cfg      config.Config
}

// prepareLocal 为本地子命令准备运行时与注册表。
//
// 三个子命令（ps/start/stop/logs）只有参数不同，初始化完全一致，
// 因此统一在这里做一次——复制三份初始化代码是后面必然出问题的地方。
func prepareLocal(args []string, stderr io.Writer, extraFlag func(*flag.FlagSet)) (*localSetup, *parsedArgs, int, bool) {
	fs := flag.NewFlagSet("local", flag.ContinueOnError)
	fs.SetOutput(stderr)
	cfgPath := fs.String("c", "", "配置文件路径（可选，本地命令不连 Hub）")
	if extraFlag != nil {
		extraFlag(fs)
	}
	pa, err := parseArgs(fs, args)
	if err != nil {
		return nil, nil, exitUsage, false
	}

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		fmt.Fprintf(stderr, "错误: %v\n", err)
		return nil, nil, exitFail, false
	}

	rt, probeErr := createRuntime(cfg)
	if probeErr != nil {
		// 显式配置的端点探测失败属于配置问题，直接报错停止：
		// 否则用户会看到"没有容器"而不是"你配的套接字连不上"。
		fmt.Fprintf(stderr, "错误: %v\n", probeErr)
		return nil, nil, exitFail, false
	}

	reg := handler.NewRegistry()
	// 本地一次性命令（container ls 之类）不需要服务日志，
	// 传 nil 由 RegisterDefaults 用静默日志器兜底。
	//
	// ============================ 镜像传输在本地 CLI 里不注册 ============================
	//
	// 镜像传输是**异步任务**模型：pull/save 立即返回 task_id，
	// 真正的执行在后台 goroutine 里，进度靠 image.task.status 轮询。
	//
	// 本地 CLI 是短命进程：命令执行完就退出。一旦在这里注册，
	// 会出现两个都不正确的选择：
	//
	//   - 进程退出时不管后台任务 → 留下**无主任务**：
	//     归档文件躺在 transfers 目录里没人认领，也没有任何东西
	//     能再查询那个 task_id（它只存在于已退出的进程内存里）。
	//     用户看到"已开始"，然后什么都不会发生。
	//
	//   - 进程等待任务完成 → CLI 从"一条命令"变成"一个后台服务"，
	//     必须自己实现轮询、超时、信号处理，等于把 Hub 的职责
	//     复制一份到本地命令里，行为必然与在线模式逐渐分叉。
	//
	// 因此本地 CLI **明确不提供**镜像传输（Hub 得到 unsupported，
	// 用户改用常驻 Agent）。这不是"还没做"，而是刻意的边界：
	// 异步任务需要一个比单条命令活得更久的宿主，而常驻 Agent 正是那个宿主。
	//
	// 需要本机离线导出/导入时，直接用 docker save / docker load——
	// 那本来就是这些引擎自带的能力，不需要我们中转一层。
	handler.RegisterDefaultsWithTransfer(reg, rt,
		buildComposeManager(cfg, rt), nil, nil)
	return &localSetup{rt: rt, registry: reg, cfg: cfg}, pa, exitOK, true
}

// createRuntime 按配置创建运行时；PreferPodman 由 runtime_kind 推导。
func createRuntime(cfg config.Config) (runtime.Runtime, error) {
	return runtime.New(runtime.Config{
		Socket:       cfg.Socket,
		PreferPodman: cfg.PreferPodman || cfg.RuntimeKind == "podman",
	})
}

// callLocal 通过注册表执行一次本地请求。
//
// 注意这里构造的信封与 Hub 下发的完全同构（同样的 v/type/method/payload），
// 这是"在线与离线共用一套实现"的落地方式——不存在第二条执行路径。
func callLocal(ctx context.Context, reg *handler.Registry, method string, payload any) (*envelope.Envelope, error) {
	req, err := envelope.NewRequest(newLocalID(), method, "agent:local", payload)
	if err != nil {
		return nil, err
	}
	resp := reg.Dispatch(ctx, req)
	if resp == nil {
		return nil, fmt.Errorf("本地调用 %s 未返回结果", method)
	}
	if resp.Error != nil {
		return resp, resp.Error
	}
	return resp, nil
}

// localTimeout 是本地命令的执行超时；停止容器可能较慢，给足 60 秒。
const localTimeout = 60 * time.Second

// newLocalID 生成本地请求 ID（时间戳足够，本地调用不跨进程）。
func newLocalID() string {
	return fmt.Sprintf("local-%d", time.Now().UnixNano())
}

// newServiceLogger 构造服务模式日志器。
//
// 输出到 stderr：systemd 会把两个流都收进 journal，但分开写可以让
// "日志"与"命令输出"不混在一起（CLI 本地命令的 stdout 是给人/脚本用的数据）。
func newServiceLogger(cfg config.Config) (*slog.Logger, error) {
	return logging.New(logging.Options{
		Level:  cfg.LogLevel,
		Format: cfg.LogFormat,
		Writer: nil, // nil = stderr
	})
}

// buildLinkAgent 组装连接器。
//
// 单独抽出来的原因：run 子命令里既要处理 agent_id 的读取，又要注入能力清单，
// 这段编排逻辑放在 main 流程里会把"连接"与"身份"两件事搅在一起。
func buildLinkAgent(cfg config.Config, log *slog.Logger, rt runtime.Runtime, reg *handler.Registry) (*link.Agent, error) {
	agentID, err := cfg.ResolveAgentID()
	if err != nil {
		return nil, err
	}
	return link.New(link.Options{
		HubURL:       cfg.HubURL,
		Token:        cfg.Token,
		AgentID:      agentID,
		DataDir:      cfg.DataDir,
		Name:         cfg.Name,
		AgentVersion: Version,
		HeartbeatSec: cfg.HeartbeatSec,
		InsecureTLS:  cfg.InsecureTLS,
		Logger:       log,
		Runtime:      rt,
		Dispatch:     reg.Dispatch,
		Capabilities: reg.Methods,
	})
}

// 说明：位置参数与选项的混合解析见 args.go 的 parseArgs。
