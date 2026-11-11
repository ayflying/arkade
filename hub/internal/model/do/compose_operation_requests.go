// =================================================================================
// Code generated and maintained by GoFrame CLI tool. DO NOT EDIT.
// =================================================================================

package do

import (
	"github.com/gogf/gf/v2/frame/g"
)

// ComposeOperationRequests is the golang structure of table compose_operation_requests for DAO operations like Where/Data.
type ComposeOperationRequests struct {
	g.Meta        `orm:"table:compose_operation_requests, do:true"`
	AgentId       any //
	RequestId     any //
	Name          any //
	Action        any //
	Fingerprint   any //
	TaskId        any //
	StackId       any //
	StackVersion  any //
	ConfigHash    any //
	CreatedAt     any //
	Finalized     any //
	FinalError    any //
	RecordRemoved any //
}
