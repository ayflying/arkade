// Package v1 declares the native GoFrame audit read API.
package v1

import "github.com/gogf/gf/v2/frame/g"

// ListReq deliberately keeps numbers as strings. Parsing belongs to the
// controller so malformed/overflowing integers keep the legacy error messages.
// Only query parameters are accepted; the HTTP binding adapter preserves their
// original URL values instead of merging JSON/form input into these fields.
type ListReq struct {
	g.Meta  `path:"/api/audit" method:"get" tags:"Audit" summary:"查询操作记录"`
	UserID  string `json:"user_id" in:"query"`
	AgentID string `json:"agent_id" in:"query"`
	Action  string `json:"action" in:"query"`
	Result  string `json:"result" in:"query"`
	Limit   string `json:"limit" in:"query"`
	Offset  string `json:"offset" in:"query"`
	From    string `json:"from" in:"query"`
	To      string `json:"to" in:"query"`
}

// ListRes is bare JSON, not the GoFrame default response envelope.
type ListRes struct {
	Total int    `json:"total"`
	Items []Item `json:"items"`
}

// Item explicitly lists public fields; internal records never get serialized.
type Item struct {
	ID          string `json:"id"`
	UserID      string `json:"user_id"`
	Username    string `json:"username"`
	AgentID     string `json:"agent_id"`
	AgentName   string `json:"agent_name"`
	Action      string `json:"action"`
	ActionLabel string `json:"action_label"`
	Target      string `json:"target"`
	Detail      string `json:"detail"`
	Result      string `json:"result"`
	Error       string `json:"error"`
	DurationMs  int64  `json:"duration_ms"`
	CreatedAt   int64  `json:"created_at"`
}

type ActionsReq struct {
	g.Meta `path:"/api/audit/actions" method:"get" tags:"Audit" summary:"查询审计动作"`
}

type ActionsRes struct {
	Actions []ActionOption `json:"actions"`
}

type ActionOption struct {
	Action string `json:"action"`
	Label  string `json:"label"`
}
