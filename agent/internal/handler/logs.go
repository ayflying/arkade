package handler

import (
	"context"
	"encoding/json"
	"strings"

	"arkade/agent/internal/runtime"
	"arkade/protocol/envelope"
)

// 日志行数上限。
//
// 协议里的 tail_lines 是"请求意图"，但服务端必须有硬上限：日志是无界数据，
// 无脑相信调用方会拖垮 WebSocket 队列（03 技术选型对此有明确要求）。
// 上限取 5000 行：足够覆盖排障常用场景（几 MB 文本），又不会撑爆单条消息。
const (
	maxLogLines     = 5000
	defaultLogLines = 200
)

// newLogsHandler 处理 container.logs。
//
// 载荷：envelope.ContainerLogsPayload。
// 应答：envelope.ContainerLogsResult（含 MarkTruncated 标记）。
func newLogsHandler(rt runtime.Runtime) Handler {
	return func(ctx context.Context, payload []byte) (any, error) {
		p, clamped, err := logsPayload(payload)
		if err != nil {
			return nil, err
		}
		res, err := rt.Logs(ctx, p.ContainerID, runtime.LogOptions{
			TailLines: p.TailLines,
			// 默认同时要 stdout 与 stderr：用户说"看日志"时不会想漏掉错误输出。
			Stdout: true,
			Stderr: true,
		})
		if err != nil {
			return nil, err
		}
		lines := res.Lines
		if lines == nil {
			// 返回空数组而不是 null：前端与 CLI 可以直接遍历，不必判空。
			lines = []string{}
		}
		// Truncated 有两个来源：请求超上限被裁剪（clamped），或运行时返回的行数
		// 比请求的多（res.Truncated）。任一成立都说明用户看到的不是全部。
		return envelope.ContainerLogsResult{Lines: lines, Truncated: res.Truncated || clamped}, nil
	}
}

// logsPayload 解码并裁剪日志请求。
//
// TailLines 的边界处理：
//   - <=0 → 使用默认值（而不是"全量"，避免调用方填 0 造成无界拉取）；
//   - >maxLogLines → 截到上限，并通过第二个返回值告知调用方"请求被裁剪过"，
//     使结果里的 Truncated 反映真实情况（调用方要的比给的多）。
func logsPayload(payload []byte) (envelope.ContainerLogsPayload, bool, error) {
	var p envelope.ContainerLogsPayload
	if len(payload) == 0 || string(payload) == "null" {
		return p, false, badRequestf("缺少载荷：需要 container_id")
	}
	if err := json.Unmarshal(payload, &p); err != nil {
		return p, false, badRequestf("载荷格式错误: %v", err)
	}
	if strings.TrimSpace(p.ContainerID) == "" {
		return p, false, badRequestf("container_id 不能为空")
	}
	clamped := false
	if p.TailLines <= 0 {
		p.TailLines = defaultLogLines
	}
	if p.TailLines > maxLogLines {
		p.TailLines = maxLogLines
		clamped = true
	}
	return p, clamped, nil
}
