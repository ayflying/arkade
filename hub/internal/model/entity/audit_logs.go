// =================================================================================
// Code generated and maintained by GoFrame CLI tool. DO NOT EDIT.
// =================================================================================

package entity

// AuditLogs is the golang structure for table audit_logs.
type AuditLogs struct {
	Id         string `json:"id"         orm:"id"          ` //
	UserId     string `json:"userId"     orm:"user_id"     ` //
	Username   string `json:"username"   orm:"username"    ` //
	AgentId    string `json:"agentId"    orm:"agent_id"    ` //
	AgentName  string `json:"agentName"  orm:"agent_name"  ` //
	Action     string `json:"action"     orm:"action"      ` //
	Target     string `json:"target"     orm:"target"      ` //
	Detail     string `json:"detail"     orm:"detail"      ` //
	Result     string `json:"result"     orm:"result"      ` //
	Error      string `json:"error"      orm:"error"       ` //
	DurationMs int64  `json:"durationMs" orm:"duration_ms" ` //
	CreatedAt  int64  `json:"createdAt"  orm:"created_at"  ` //
}
