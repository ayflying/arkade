package handler

import (
	"errors"

	"arkade/agent/internal/compose"
	"arkade/protocol/envelope"
)

// translatePullError 供统一编排任务接口使用，映射底层任务引擎的领域错误。
func translatePullError(err error) error {
	switch {
	case errors.Is(err, compose.ErrPullTaskNotFound):
		return envelope.NewError(envelope.CodeNotFound, err.Error())
	case errors.Is(err, compose.ErrTooManyPullTasks):
		return envelope.NewError(envelope.CodeAgentBusy, err.Error())
	case errors.Is(err, compose.ErrPullRequestMismatch):
		return envelope.NewError(envelope.CodeConflict, err.Error())
	default:
		return envelope.NewError(envelope.CodeRuntimeFailed, err.Error())
	}
}
