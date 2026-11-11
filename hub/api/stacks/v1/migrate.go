package v1

import "github.com/gogf/gf/v2/frame/g"

type MigrateReq struct {
	g.Meta  `path:"/api/agents/{agentID}/stacks/{name}/migrate" method:"post"`
	AgentID string `json:"-" in:"path" name:"agentID"`
	Name    string `json:"-" in:"path" name:"name"`
}
type MigrateInput struct {
	TargetAgentID  string `json:"target_agent_id"`
	IncludeVolumes bool   `json:"include_volumes"`
	StartAfter     bool   `json:"start_after"`
	Confirmation   string `json:"confirmation"`
	RequestID      string `json:"request_id,omitempty"`
}
type MigrateRes struct {
	Task MigrateTask `json:"task"`
}
type MigrateEvent struct {
	Seq    int64  `json:"seq"`
	Stream string `json:"stream"`
	Text   string `json:"text"`
}
type MigrateTask struct {
	TaskID     string         `json:"task_id"`
	AgentID    string         `json:"agent_id"`
	Name       string         `json:"name"`
	Action     string         `json:"action"`
	Status     string         `json:"status"`
	NextCursor int64          `json:"next_cursor"`
	Output     []MigrateEvent `json:"output"`
	Truncated  bool           `json:"truncated,omitempty"`
	Error      string         `json:"error,omitempty"`
	StartedAt  string         `json:"started_at,omitempty"`
	FinishedAt string         `json:"finished_at,omitempty"`
	Migrate    MigrationInfo  `json:"migrate"`
}
type MigrationInfo struct {
	Source         string            `json:"source_agent_id"`
	Target         string            `json:"target_agent_id"`
	IncludeVolumes bool              `json:"include_volumes"`
	StartAfter     bool              `json:"start_after"`
	Stage          string            `json:"stage"`
	StageTitle     string            `json:"stage_title"`
	ImageCount     int               `json:"image_count"`
	VolumeCount    int               `json:"volume_count"`
	SkippedVolumes int               `json:"skipped_volumes"`
	BindDirs       int               `json:"bind_dirs"`
	Residual       MigrationResidual `json:"residual"`
}
type MigrationResidual struct {
	ImagesImported  int  `json:"images_imported"`
	VolumesRestored int  `json:"volumes_restored"`
	ComposeWritten  bool `json:"compose_written"`
	Started         bool `json:"started"`
	Uncertain       bool `json:"uncertain,omitempty"`
}
