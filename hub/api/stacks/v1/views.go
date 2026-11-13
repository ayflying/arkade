package v1

import "arkade/protocol/envelope"

type StackView struct {
	AgentID         string   `json:"agent_id"`
	Name            string   `json:"name"`
	Managed         bool     `json:"managed"`
	AutoAdoptStatus string   `json:"auto_adopt_status,omitempty"`
	AutoAdoptReason string   `json:"auto_adopt_reason,omitempty"`
	Status          string   `json:"status"`
	ConfigHash      string   `json:"config_hash"`
	RemoteHash      string   `json:"remote_hash"`
	Drift           bool     `json:"drift"`
	Services        []string `json:"services"`
	Running         int      `json:"running"`
	Total           int      `json:"total"`
	Version         int64    `json:"version"`
	UpdatedAt       int64    `json:"updated_at"`
	SyncedAt        int64    `json:"synced_at"`
	SyncHint        string   `json:"sync_hint,omitempty"`
}
type ExternalStackView struct {
	Name            string   `json:"name"`
	Status          string   `json:"status"`
	Services        []string `json:"services"`
	Running         int      `json:"running"`
	Total           int      `json:"total"`
	Managed         bool     `json:"managed"`
	AutoAdoptStatus string   `json:"auto_adopt_status,omitempty"`
	AutoAdoptReason string   `json:"auto_adopt_reason,omitempty"`
}
type OpResult struct {
	AgentID    string `json:"agent_id"`
	Name       string `json:"name"`
	Status     string `json:"status"`
	ConfigHash string `json:"config_hash"`
	Output     string `json:"output"`
}
type SyncResult struct {
	Status string `json:"status"`
	Output string `json:"output"`
}
type SaveRes struct {
	Stack    StackView   `json:"stack"`
	SyncOK   bool        `json:"sync_ok"`
	SyncHint string      `json:"sync_hint"`
	Result   *SyncResult `json:"result"`
}
type ListRes struct {
	AgentID        string               `json:"agent_id"`
	Stacks         []StackView          `json:"stacks"`
	AgentOnline    bool                 `json:"agent_online"`
	ExternalStacks *[]ExternalStackView `json:"external_stacks,omitempty"`
}
type DetailRes struct {
	Stack           StackView                   `json:"stack"`
	Containers      []envelope.ContainerSummary `json:"containers"`
	ContainersError string                      `json:"containers_error,omitempty"`
}
type ConfigRes struct {
	Content    string `json:"content"`
	EnvContent string `json:"env_content"`
	Version    int64  `json:"version"`
}
type DeleteRes struct {
	Name       string      `json:"name"`
	SyncOK     bool        `json:"sync_ok"`
	SyncHint   string      `json:"sync_hint"`
	Result     *SyncResult `json:"result"`
	ConfigHash string      `json:"config_hash"`
}
type LogsRes struct {
	Lines     []string `json:"lines"`
	Truncated bool     `json:"truncated"`
}
type PSRes struct {
	AgentID    string                      `json:"agent_id"`
	Containers []envelope.ContainerSummary `json:"containers"`
}
