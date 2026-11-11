// =================================================================================
// Code generated and maintained by GoFrame CLI tool. DO NOT EDIT.
// =================================================================================

package entity

// ComposeOperationRequests is the golang structure for table compose_operation_requests.
type ComposeOperationRequests struct {
	AgentId       string `json:"agentId"       orm:"agent_id"       ` //
	RequestId     string `json:"requestId"     orm:"request_id"     ` //
	Name          string `json:"name"          orm:"name"           ` //
	Action        string `json:"action"        orm:"action"         ` //
	Fingerprint   string `json:"fingerprint"   orm:"fingerprint"    ` //
	TaskId        string `json:"taskId"        orm:"task_id"        ` //
	StackId       string `json:"stackId"       orm:"stack_id"       ` //
	StackVersion  int64  `json:"stackVersion"  orm:"stack_version"  ` //
	ConfigHash    string `json:"configHash"    orm:"config_hash"    ` //
	CreatedAt     int64  `json:"createdAt"     orm:"created_at"     ` //
	Finalized     int    `json:"finalized"     orm:"finalized"      ` //
	FinalError    string `json:"finalError"    orm:"final_error"    ` //
	RecordRemoved int    `json:"recordRemoved" orm:"record_removed" ` //
}
