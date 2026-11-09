package cli

import (
	"context"
	"flag"
	"fmt"
	"io"

	"arkade/protocol/envelope"
)

// cmdPS 实现 `arkade-agent ps`：本地列出容器。
//
// 输出刻意对齐 docker ps 的列顺序（CONTAINER ID / IMAGE / STATE / STATUS / PORTS / NAMES），
// 运维在两套工具之间切换时不需要重新建立视觉映射。
func cmdPS(args []string, stdout, stderr io.Writer) int {
	setup, _, code, ok := prepareLocal(args, stderr, nil)
	if !ok {
		return code
	}

	ctx, cancel := context.WithTimeout(context.Background(), localTimeout)
	defer cancel()

	resp, err := callLocal(ctx, setup.registry, envelope.LocalMethodContainerList, nil)
	if err != nil {
		return reportError(stderr, err)
	}

	var res envelope.ContainerListResult
	if derr := envelope.DecodePayload(resp, &res); derr != nil {
		fmt.Fprintf(stderr, "错误: 解析容器列表失败: %v\n", derr)
		return exitFail
	}

	if len(res.Containers) == 0 {
		fmt.Fprintln(stdout, "本机没有容器。")
		return exitOK
	}

	tw := newTabWriter(stdout)
	fmt.Fprintln(tw, "CONTAINER ID\tIMAGE\tSTATE\tSTATUS\tPORTS\tNAMES")
	for _, c := range res.Containers {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n",
			c.ID, c.Image, c.State, c.Status, emptyDash(c.Ports), c.Name)
	}
	_ = tw.Flush()
	return exitOK
}

// cmdContainerOp 实现 start / stop 两个子命令。
//
// 两者的参数与流程完全一致（只差方法与 force 选项），因此合并成一个函数，
// 用 startOp 区分——避免写两份几乎相同的代码。
func cmdContainerOp(args []string, stdout, stderr io.Writer, startOp bool) int {
	force := false
	setup, pa, code, ok := prepareLocal(args, stderr, func(fs *flag.FlagSet) {
		if !startOp {
			fs.BoolVar(&force, "force", false, "强制停止（SIGKILL），跳过优雅退出")
		}
	})
	if !ok {
		return code
	}

	ref := pa.First()
	if ref == "" {
		fmt.Fprintf(stderr, "错误: 需要容器 ID 或名称，例如: arkade-agent %s web\n", opName(startOp))
		return exitUsage
	}

	method := envelope.LocalMethodContainerStart
	if !startOp {
		method = envelope.LocalMethodContainerStop
	}

	ctx, cancel := context.WithTimeout(context.Background(), localTimeout)
	defer cancel()

	payload := envelope.ContainerOpPayload{ContainerID: ref, Force: force}
	resp, err := callLocal(ctx, setup.registry, method, payload)
	if err != nil {
		return reportError(stderr, err)
	}

	var res envelope.ContainerOpResult
	if derr := envelope.DecodePayload(resp, &res); derr != nil {
		fmt.Fprintf(stderr, "错误: 解析操作结果失败: %v\n", derr)
		return exitFail
	}
	if res.State == "" {
		fmt.Fprintf(stdout, "%s: %s 指令已提交\n", opNameCN(startOp), ref)
		return exitOK
	}
	fmt.Fprintf(stdout, "%s: %s 当前状态 %s\n", opNameCN(startOp), displayRef(res.ContainerID, ref), res.State)
	return exitOK
}

// opName 返回英文动作名（用于命令行提示）。
func opName(start bool) string {
	if start {
		return "start"
	}
	return "stop"
}

// opNameCN 返回中文动作名（用于面向人的结果输出）。
func opNameCN(start bool) string {
	if start {
		return "启动"
	}
	return "停止"
}

// cmdLogs 实现 `arkade-agent logs`。
//
// 退出约定：日志正文写 stdout（可直接管道给 grep），截断提示写 stderr。
func cmdLogs(args []string, stdout, stderr io.Writer) int {
	lines := 0
	setup, pa, code, ok := prepareLocal(args, stderr, func(fs *flag.FlagSet) {
		fs.IntVar(&lines, "n", 200, "只显示末尾 N 行（默认 200，上限 5000）")
	})
	if !ok {
		return code
	}

	ref := pa.First()
	if ref == "" {
		fmt.Fprintln(stderr, "错误: 需要容器 ID 或名称，例如: arkade-agent logs web -n 100")
		return exitUsage
	}
	if lines <= 0 {
		fmt.Fprintln(stderr, "错误: -n 必须是正整数")
		return exitUsage
	}

	ctx, cancel := context.WithTimeout(context.Background(), localTimeout)
	defer cancel()

	payload := envelope.ContainerLogsPayload{ContainerID: ref, TailLines: lines}
	resp, err := callLocal(ctx, setup.registry, envelope.LocalMethodContainerLogs, payload)
	if err != nil {
		return reportError(stderr, err)
	}

	var res envelope.ContainerLogsResult
	if derr := envelope.DecodePayload(resp, &res); derr != nil {
		fmt.Fprintf(stderr, "错误: 解析日志结果失败: %v\n", derr)
		return exitFail
	}
	if len(res.Lines) == 0 {
		fmt.Fprintln(stdout, "（该容器暂无日志）")
		return exitOK
	}
	for _, line := range res.Lines {
		fmt.Fprintln(stdout, line)
	}
	if res.Truncated {
		fmt.Fprintln(stderr, "提示: 日志已按行数上限截断，可用 -n 调整（上限 5000）")
	}
	return exitOK
}

// cmdInfo 实现 `arkade-agent info`：查看运行时探测结果。
func cmdInfo(args []string, stdout, stderr io.Writer) int {
	setup, _, code, ok := prepareLocal(args, stderr, nil)
	if !ok {
		return code
	}

	ctx, cancel := context.WithTimeout(context.Background(), localTimeout)
	defer cancel()

	resp, err := callLocal(ctx, setup.registry, envelope.LocalMethodRuntimeInfo, nil)
	if err != nil {
		return reportError(stderr, err)
	}

	var info envelope.RuntimeInfo
	if derr := envelope.DecodePayload(resp, &info); derr != nil {
		fmt.Fprintf(stderr, "错误: 解析运行时信息失败: %v\n", derr)
		return exitFail
	}
	tw := newTabWriter(stdout)
	fmt.Fprintf(tw, "运行时类型\t%s\n", info.Kind)
	fmt.Fprintf(tw, "引擎版本\t%s\n", emptyDash(info.Version))
	fmt.Fprintf(tw, "API 版本\t%s\n", emptyDash(info.APIVer))
	fmt.Fprintf(tw, "端点\t%s\n", emptyDash(info.Socket))
	fmt.Fprintf(tw, "rootless\t%v\n", info.Rootless)
	_ = tw.Flush()
	if info.Kind == envelope.RuntimeNone {
		fmt.Fprintln(stdout, "\n本机未检测到可用的 Docker / Podman。")
	}
	return exitOK
}
