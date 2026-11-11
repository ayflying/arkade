package audit

import (
	"net/http"
	"strconv"
	"time"

	api "arkade/hub/api/audit/v1"
	"arkade/hub/internal/audit"
	"arkade/hub/internal/controller"
	"arkade/hub/internal/store"
	"arkade/hub/internal/user"
)

// parseFilters preserves legacy order and exact values: no trimming, action
// validation or range normalization. Pagination defaults/clamps remain in store.
func parseFilters(req *api.ListReq, u user.User) (store.AuditFilters, error) {
	f := store.AuditFilters{UserID: req.UserID, AgentID: req.AgentID, Action: req.Action, Result: req.Result}
	// Scope is mandatory on the server even if user_id is forged.
	if u.Role != user.RoleAdmin {
		f.UserID = u.ID
	}
	var err error
	if f.Limit, err = optionalInt(req.Limit, "limit"); err != nil {
		return f, err
	}
	if f.Offset, err = optionalInt(req.Offset, "offset"); err != nil {
		return f, err
	}
	if f.Result != "" && f.Result != audit.ResultSuccess && f.Result != audit.ResultFailed {
		return f, badRequest("result 只能是 success 或 failed")
	}
	if req.From != "" {
		if f.From, err = unixTime(req.From); err != nil {
			return f, badRequest("from 必须是 Unix 秒或将时间格式")
		}
	}
	if req.To != "" {
		if f.To, err = unixTime(req.To); err != nil {
			return f, badRequest("to 必须是 Unix 秒或将时间格式")
		}
	}
	return f, nil
}

func optionalInt(s, name string) (int, error) {
	if s == "" {
		return 0, nil
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0, badRequest(name + " 必须是整数")
	}
	return n, nil
}

func unixTime(s string) (int64, error) {
	if n, err := strconv.ParseInt(s, 10, 64); err == nil {
		return n, nil
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return 0, err
	}
	return t.Unix(), nil
}

func badRequest(message string) *controller.Error {
	return &controller.Error{Status: http.StatusBadRequest, Code: "bad_request", Message: message}
}
