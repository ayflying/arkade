// =================================================================================
// Code generated and maintained by GoFrame CLI tool. DO NOT EDIT.
// =================================================================================

package entity

// AutoAdoptStates is the golang structure for table auto_adopt_states.
type AutoAdoptStates struct {
	AgentId       string `json:"agentId"       orm:"agent_id"        ` //
	Name          string `json:"name"          orm:"name"            ` //
	Status        string `json:"status"        orm:"status"          ` //
	Reason        string `json:"reason"        orm:"reason"          ` //
	Attempts      int64  `json:"attempts"      orm:"attempts"        ` //
	NextCheckAt   int64  `json:"nextCheckAt"   orm:"next_check_at"   ` //
	LastScannedAt int64  `json:"lastScannedAt" orm:"last_scanned_at" ` //
}
