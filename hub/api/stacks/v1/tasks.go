package v1

import (
	"arkade/protocol/envelope"
	"encoding/json"
	"github.com/gogf/gf/v2/frame/g"
)

type StartOperationReq struct {
	g.Meta  `path:"/api/agents/{agentID}/stacks/{name}/operation-tasks" method:"post"`
	AgentID string `json:"-" in:"path" name:"agentID"`
	Name    string `json:"-" in:"path" name:"name"`
}
type OperationInput struct {
	RequestID          string   `json:"request_id"`
	Action             string   `json:"action"`
	Services           []string `json:"services,omitempty"`
	RemoveVolumes      bool     `json:"remove_volumes,omitempty"`
	IncludeVolumes     bool     `json:"include_volumes,omitempty"`
	Content            *string  `json:"content,omitempty"`
	EnvContent         *string  `json:"env_content,omitempty"`
	Version            int64    `json:"version,omitempty"`
	ExpectedConfigHash string   `json:"expected_config_hash,omitempty"`
	Create             bool     `json:"create,omitempty"`
}
type GetOperationReq struct {
	g.Meta  `path:"/api/agents/{agentID}/stacks/{name}/operation-tasks/{taskID}" method:"get"`
	AgentID string `json:"-" in:"path" name:"agentID"`
	Name    string `json:"-" in:"path" name:"name"`
	TaskID  string `json:"-" in:"path" name:"taskID"`
	After   string `json:"-" in:"query" name:"after"`
}
type OperationTask struct {
	envelope.ComposeOperationResult
	StackVersion  int64 `json:"stack_version"`
	RecordRemoved bool  `json:"record_removed"`
}

// OperationRes holds exactly one native typed task variant.
type OperationRes struct {
	Task      *OperationTask
	Migration *MigrateTask
}

func (r OperationRes) MarshalJSON() ([]byte, error) {
	if r.Migration != nil {
		return json.Marshal(struct {
			Task *MigrateTask `json:"task"`
		}{r.Migration})
	}
	return json.Marshal(struct {
		Task *OperationTask `json:"task"`
	}{r.Task})
}

type StartPullReq struct {
	g.Meta  `path:"/api/agents/{agentID}/stacks/{name}/pull-tasks" method:"post"`
	AgentID string `json:"-" in:"path" name:"agentID"`
	Name    string `json:"-" in:"path" name:"name"`
}
type PullInput struct {
	RequestID string `json:"request_id"`
}
type GetPullReq struct {
	g.Meta  `path:"/api/agents/{agentID}/stacks/{name}/pull-tasks/{taskID}" method:"get"`
	AgentID string `json:"-" in:"path" name:"agentID"`
	Name    string `json:"-" in:"path" name:"name"`
	TaskID  string `json:"-" in:"path" name:"taskID"`
	After   string `json:"-" in:"query" name:"after"`
}
type PullRes struct {
	Task envelope.ComposePullResult `json:"task"`
}
