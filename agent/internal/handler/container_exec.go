package handler

// container_exec.go：container.exec 处理器。
//
// ============================ 校验放在这里，而不是指望引擎 ============================
//
// 引擎对 exec 参数几乎不做校验：命令不存在它会报 500 并带原文，
// 工作目录不存在也是一样。这些原始错误对界面用户没有指导意义，
// 而且"命令为空"这类明显非法输入应该在 Agent 就被挡下来——
// 让它走到引擎等于凭空多一次容器内的进程创建尝试。
//
// ============================ 不拼接字符串 ============================
//
// 命令以数组形式传给引擎，**绝不做** `strings.Join(cmd, " ")` 再交给 shell：
// 拼接会把参数里的空格、引号、分号重新解释一遍，用户输入的
// `ls -l "a b"` 会变成四个参数，注入也就在这一步发生。

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"arkade/agent/internal/runtime"
	"arkade/protocol/envelope"
)

const (
	// 单条命令的参数个数与单个参数长度上限。
	// 这里的目的是挡住"把整个文件塞进一个参数"，不是在限制正常使用。
	maxExecArgs     = 64
	maxExecArgBytes = 8 << 10
	// 身份与工作目录的长度上限。
	maxExecUserBytes = 128
	maxExecDirBytes  = 4096
)

// newExecHandler 构造容器内执行命令处理器。
func newExecHandler(rt runtime.Runtime) Handler {
	return func(ctx context.Context, payload []byte) (any, error) {
		var p envelope.ContainerExecPayload
		if len(payload) == 0 || string(payload) == "null" {
			return nil, badRequestf("缺少载荷：需要 container_id 与 command")
		}
		if err := json.Unmarshal(payload, &p); err != nil {
			return nil, badRequestf("载荷格式错误: %v", err)
		}
		if strings.TrimSpace(p.ContainerID) == "" {
			return nil, badRequestf("container_id 不能为空")
		}
		if err := validateExecPayload(&p); err != nil {
			return nil, err
		}

		cap, ok := rt.(runtime.ContainerExecRuntime)
		if !ok {
			return nil, envelope.NewError(envelope.CodeUnsupported, "当前引擎不支持在容器内执行命令")
		}
		res, err := cap.ExecContainer(ctx, p.ContainerID, runtime.ExecOptions{
			Command: p.Command,
			User:    p.User,
			WorkDir: p.WorkDir,
			Timeout: time.Duration(p.TimeoutSec) * time.Second,
		})
		if err != nil {
			return nil, err
		}
		return envelope.ContainerExecResult{
			ExitCode:   res.ExitCode,
			Stdout:     res.Stdout,
			Stderr:     res.Stderr,
			Truncated:  res.Truncated,
			TimedOut:   res.TimedOut,
			DurationMs: res.Duration.Milliseconds(),
		}, nil
	}
}

// validateExecPayload 校验并规范化执行参数（就地改写 TimeoutSec 等）。
func validateExecPayload(p *envelope.ContainerExecPayload) error {
	if len(p.Command) == 0 {
		return badRequestf("command 不能为空")
	}
	if len(p.Command) > maxExecArgs {
		return badRequestf("命令参数过多（最多 %d 个）", maxExecArgs)
	}
	for _, arg := range p.Command {
		if len(arg) > maxExecArgBytes {
			return badRequestf("单个命令参数超过 %d 字节", maxExecArgBytes)
		}
		// 只挡 NUL：它在多数引擎/系统调用里会截断字符串，
		// 让"校验过的内容"与"实际执行的内容"不一致。
		if strings.ContainsRune(arg, 0) {
			return badRequestf("命令参数不能包含 NUL 字符")
		}
	}
	// 首项是程序路径；给一个明确提示，避免用户以为传整行也可以。
	if strings.TrimSpace(p.Command[0]) == "" {
		return badRequestf("命令的第一个元素必须是可执行文件路径")
	}
	if len(p.User) > maxExecUserBytes || hasControl(p.User) {
		return badRequestf("执行用户无效")
	}
	if len(p.WorkDir) > maxExecDirBytes || hasControl(p.WorkDir) {
		return badRequestf("工作目录无效")
	}
	if p.WorkDir != "" && !strings.HasPrefix(p.WorkDir, "/") {
		return badRequestf("工作目录必须是绝对路径")
	}
	if p.TimeoutSec <= 0 {
		p.TimeoutSec = 30
	}
	if p.TimeoutSec > 120 {
		p.TimeoutSec = 120
	}
	return nil
}

// hasControl 判断是否含控制字符（含换行与制表符）。
func hasControl(s string) bool {
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return true
		}
	}
	return false
}
