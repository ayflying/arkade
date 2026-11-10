package handler

// compose_pull.go：编排「更新镜像」的异步任务处理器。
//
// start 立即返回快照，get 增量轮询。两者共用同一套结果结构，
// 前端只需实现一次解析（详见协议注释）。

import (
	"context"
	"errors"
	"strings"
	"time"

	"arkade/agent/internal/compose"
	"arkade/protocol/envelope"
)

// newComposePullStartHandler 处理 compose.pull.start。
func newComposePullStartHandler(mgr *compose.PullManager) Handler {
	return func(ctx context.Context, payload []byte) (any, error) {
		var p envelope.ComposePullStartPayload
		if err := decodePayloadInto(payload, &p); err != nil {
			return nil, err
		}
		if strings.TrimSpace(p.Name) == "" {
			return nil, badRequestf("编排项目名不能为空")
		}
		snap, err := mgr.StartPull(p.Name, p.RequestID)
		if err != nil {
			return nil, translatePullError(err)
		}
		return toPullResult(snap), nil
	}
}

// newComposePullGetHandler 处理 compose.pull.get。
func newComposePullGetHandler(mgr *compose.PullManager) Handler {
	return func(ctx context.Context, payload []byte) (any, error) {
		var p envelope.ComposePullGetPayload
		if err := decodePayloadInto(payload, &p); err != nil {
			return nil, err
		}
		if strings.TrimSpace(p.TaskID) == "" {
			return nil, badRequestf("任务 ID 不能为空")
		}
		if p.After < 0 {
			return nil, badRequestf("游标不能为负数")
		}
		snap, err := mgr.GetPull(p.TaskID, p.After)
		if err != nil {
			return nil, translatePullError(err)
		}
		return toPullResult(snap), nil
	}
}

// toPullResult 把任务快照转成协议结构。
func toPullResult(s compose.PullSnapshot) envelope.ComposePullResult {
	out := envelope.ComposePullResult{
		TaskID:     s.TaskID,
		Name:       s.Name,
		Command:    s.Command,
		WorkingDir: s.WorkingDir,
		Status:     s.Status,
		NextCursor: s.NextCursor,
		Truncated:  s.Truncated,
		ExitCode:   s.ExitCode,
		Error:      s.Error,
		Output:     make([]envelope.ComposePullOutput, 0, len(s.Output)),
	}
	for _, ev := range s.Output {
		out.Output = append(out.Output, envelope.ComposePullOutput{
			Seq:    ev.Seq,
			Stream: ev.Stream,
			Text:   ev.Text,
		})
	}
	// 时间统一成 RFC3339（UTC），空值不输出。
	if !s.StartedAt.IsZero() {
		out.StartedAt = s.StartedAt.UTC().Format(time.RFC3339)
	}
	if !s.FinishedAt.IsZero() {
		out.FinishedAt = s.FinishedAt.UTC().Format(time.RFC3339)
	}
	return out
}

// translatePullError 把领域错误映射成协议错误码。
func translatePullError(err error) error {
	switch {
	case errors.Is(err, compose.ErrPullTaskNotFound):
		return envelope.NewError(envelope.CodeNotFound, err.Error())
	case errors.Is(err, compose.ErrTooManyPullTasks):
		return envelope.NewError(envelope.CodeAgentBusy, err.Error())
	case errors.Is(err, compose.ErrPullRequestMismatch):
		return envelope.NewError(envelope.CodeConflict, err.Error())
	default:
		// 包括"项目在本机不存在"（requireExists 返回的普通错误，
		// 文案已是中文且不含路径）与 ErrPullRedactUnavailable。
		// 后者是本次调用故障而不是"项目不能接管"的结论，
		// 且其文案本身安全（不含任何 .env 内容）。
		return envelope.NewError(envelope.CodeRuntimeFailed, err.Error())
	}
}
