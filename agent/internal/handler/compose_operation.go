package handler

// compose_operation.go：编排动作的异步任务处理器。
//
// 与 compose_pull.go 共用同一套任务引擎与快照结构，
// 因此这里的代码只是"协议字段 → 领域参数"的翻译。

import (
	"context"
	"strings"
	"time"

	"arkade/agent/internal/compose"
	"arkade/protocol/envelope"
)

// newComposeOperationStartHandler 处理 compose.operation.start。
func newComposeOperationStartHandler(mgr *compose.PullManager) Handler {
	return func(ctx context.Context, payload []byte) (any, error) {
		var p envelope.ComposeOperationStartPayload
		if err := decodePayloadInto(payload, &p); err != nil {
			return nil, err
		}
		if strings.TrimSpace(p.Name) == "" {
			return nil, badRequestf("编排项目名不能为空")
		}

		spec := compose.OperationSpec{
			Action:         p.Action,
			Config:         p.Config,
			Env:            p.Env,
			ExpectHash:     p.ExpectHash,
			Services:       p.Services,
			RemoveVolumes:  p.RemoveVolumes,
			IncludeVolumes: p.IncludeVolumes,
		}
		snap, err := mgr.StartOperation(spec, p.Name, p.RequestID)
		if err != nil {
			return nil, translatePullError(err)
		}
		return toOperationResult(snap), nil
	}
}

// newComposeOperationGetHandler 处理 compose.operation.get。
func newComposeOperationGetHandler(mgr *compose.PullManager) Handler {
	return func(ctx context.Context, payload []byte) (any, error) {
		var p envelope.ComposeOperationGetPayload
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
		return toOperationResult(snap), nil
	}
}

// toOperationResult 把任务快照转成协议结构。
func toOperationResult(s compose.PullSnapshot) envelope.ComposeOperationResult {
	out := envelope.ComposeOperationResult{
		TaskID:     s.TaskID,
		Name:       s.Name,
		Action:     s.Action,
		WorkingDir: s.WorkingDir,
		Command:    s.Command,
		Status:     s.Status,
		NextCursor: s.NextCursor,
		Truncated:  s.Truncated,
		ExitCode:   s.ExitCode,
		Error:      s.Error,
		BackupPath: s.BackupPath,
		Output:     make([]envelope.ComposeOperationEvent, 0, len(s.Output)),
	}
	for _, ev := range s.Output {
		out.Output = append(out.Output, envelope.ComposeOperationEvent{
			Seq:    ev.Seq,
			Stream: ev.Stream,
			Text:   ev.Text,
		})
	}
	out.StartedAt = formatTime(s.StartedAt)
	out.FinishedAt = formatTime(s.FinishedAt)
	return out
}

// formatTime 把时间统一成 RFC3339（UTC），零值不输出。
func formatTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}
