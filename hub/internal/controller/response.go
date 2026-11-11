package controller

import (
	"context"
	"net/http"

	"arkade/hub/internal/agentlink"
	"github.com/gogf/gf/v2/errors/gerror"
	"github.com/gogf/gf/v2/net/ghttp"
)

// ErrResponseCommitted is only for native streaming responses, never JSON business results.
var ErrResponseCommitted = gerror.New("response already committed")

const statusContextKey = "arkade_controller_status"

func Status(ctx context.Context, status int) {
	if req := ghttp.RequestFromCtx(ctx); req != nil {
		req.SetCtxVar(statusContextKey, status)
	}
}
func ResponseStatus(req *ghttp.Request) int {
	if status := req.GetCtxVar(statusContextKey); status != nil && status.Int() >= 100 {
		return status.Int()
	}
	return http.StatusOK
}
func Failure(code, message string) *Error {
	status := http.StatusInternalServerError
	switch code {
	case "unauthorized":
		status = http.StatusUnauthorized
	case "forbidden":
		status = http.StatusForbidden
	case "not_found":
		status = http.StatusNotFound
	case "bad_request":
		status = http.StatusBadRequest
	case "conflict":
		status = http.StatusConflict
	case "agent_offline", "timeout", "agent_busy", "runtime_failed":
		status = http.StatusServiceUnavailable
	case "unsupported":
		status = http.StatusNotImplemented
	}
	return &Error{Status: status, Code: code, Message: message}
}
func ConnectionError(err *agentlink.ConnError) error {
	if err == nil {
		return nil
	}
	return Failure(err.Code, err.Message).WithErr(err)
}
