// Package v1 declares external Compose inspection and explicit configuration import.
package v1

import (
	stacks "arkade/hub/api/stacks/v1"
	"arkade/hub/internal/store"
	"arkade/protocol/envelope"
	"github.com/gogf/gf/v2/frame/g"
)

type DetailReq struct {
	g.Meta  `path:"/api/agents/{agentID}/external-stacks/{name}" method:"get" tags:"External"`
	AgentID string `json:"-" in:"path" name:"agentID"`
	Name    string `json:"-" in:"path" name:"name"`
}
type DetailRes struct {
	Project    stacks.ExternalStackView    `json:"project"`
	Containers []envelope.ContainerSummary `json:"containers"`
}
type ConfigReq struct {
	g.Meta  `path:"/api/agents/{agentID}/external-stacks/{name}/config" method:"get" tags:"External"`
	AgentID string `json:"-" in:"path" name:"agentID"`
	Name    string `json:"-" in:"path" name:"name"`
}

// Pointer fields preserve omission versus explicitly present empty strings/lists.
type ConfigRes struct {
	Readable   bool                        `json:"readable"`
	Reason     string                      `json:"reason"`
	Content    *string                     `json:"content,omitempty"`
	ConfigHash *string                     `json:"config_hash,omitempty"`
	Blockers   *[]string                   `json:"blockers,omitempty"`
	Draft      *store.ExternalComposeDraft `json:"draft,omitempty"`
}

type DraftReq struct {
	g.Meta  `path:"/api/agents/{agentID}/external-stacks/{name}/draft" method:"put" tags:"External"`
	AgentID string `json:"-" in:"path" name:"agentID"`
	Name    string `json:"-" in:"path" name:"name"`
}
type DraftInput struct {
	Content         string `json:"content"`
	SourceHash      string `json:"source_hash"`
	ExpectedVersion int64  `json:"expected_version"`
}
type DraftRes struct {
	Draft   store.ExternalComposeDraft `json:"draft"`
	Message string                     `json:"message"`
}
