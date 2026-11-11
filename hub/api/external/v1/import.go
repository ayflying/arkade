package v1

import (
	stacks "arkade/hub/api/stacks/v1"
	"github.com/gogf/gf/v2/frame/g"
)

type ImportPreviewReq struct {
	g.Meta  `path:"/api/agents/{agentID}/stacks/import-preview" method:"post" tags:"External"`
	AgentID string `json:"-" in:"path" name:"agentID"`
}
type ImportReq struct {
	g.Meta  `path:"/api/agents/{agentID}/stacks/import" method:"post" tags:"External"`
	AgentID string `json:"-" in:"path" name:"agentID"`
}

// ImportInput is decoded after authorization, not coerced by generic GF binding.
type ImportInput struct {
	Name                string `json:"name"`
	Content             string `json:"content"`
	EnvContent          string `json:"env_content"`
	ExpectedContentHash string `json:"expected_content_hash"`
	Confirmation        bool   `json:"confirmation"`
}
type ImportPreviewRes struct {
	Name       string   `json:"name"`
	ConfigHash string   `json:"config_hash"`
	CanImport  bool     `json:"can_import"`
	Blockers   []string `json:"blockers"`
	Warnings   []string `json:"warnings"`
}
type ImportRes struct {
	Stack    stacks.StackView `json:"stack"`
	SyncOK   bool             `json:"sync_ok"`
	SyncHint string           `json:"sync_hint"`
	Adopted  bool             `json:"adopted"`
}

type ApplyPreviewReq struct {
	g.Meta  `path:"/api/agents/{agentID}/external-stacks/{name}/apply-preview" method:"post" tags:"External"`
	AgentID string `json:"-" in:"path" name:"agentID"`
	Name    string `json:"-" in:"path" name:"name"`
}
type ApplyPreviewInput struct {
	Content string `json:"content"`
}
type ApplyPreviewRes struct {
	CanImport  bool     `json:"can_import"`
	ConfigHash string   `json:"config_hash"`
	Blockers   []string `json:"blockers"`
	Warnings   []string `json:"warnings"`
}
type ApplyReq struct {
	g.Meta  `path:"/api/agents/{agentID}/external-stacks/{name}/apply" method:"post" tags:"External"`
	AgentID string `json:"-" in:"path" name:"agentID"`
	Name    string `json:"-" in:"path" name:"name"`
}
type ApplyRes = ImportRes

type CopyReq struct {
	g.Meta  `path:"/api/agents/{agentID}/external-stacks/{name}/copy" method:"post" tags:"External"`
	AgentID string `json:"-" in:"path" name:"agentID"`
	Name    string `json:"-" in:"path" name:"name"`
}
type CopyInput struct {
	TargetAgent  string `json:"target_agent_id"`
	TargetName   string `json:"target_name"`
	Content      string `json:"content"`
	Confirmation bool   `json:"confirmation"`
}
type CopyRes struct {
	Stack           stacks.StackView `json:"stack"`
	Scope           []string         `json:"scope"`
	DataIncluded    bool             `json:"data_included"`
	Started         bool             `json:"started"`
	SourcePreserved bool             `json:"source_preserved"`
	Message         string           `json:"message"`
}
