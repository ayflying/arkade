// Package v1 defines native image-task HTTP contracts.
package v1

import (
	"arkade/hub/internal/imagetask"
	"github.com/gogf/gf/v2/frame/g"
)

// TaskView deliberately excludes internal owner and archive bookkeeping fields.
type TaskView struct {
	ID            string   `json:"id"`
	Kind          string   `json:"kind"`
	AgentID       string   `json:"agent_id"`
	TargetAgentID string   `json:"target_agent_id,omitempty"`
	Status        string   `json:"status"`
	Phase         string   `json:"phase"`
	Message       string   `json:"message"`
	Progress      *float64 `json:"progress"`
	BytesDone     int64    `json:"bytes_done"`
	BytesTotal    *int64   `json:"bytes_total"`
	CreatedAt     string   `json:"created_at"`
	FinishedAt    string   `json:"finished_at,omitempty"`
	DownloadReady bool     `json:"download_ready"`
}

func View(t imagetask.Task) TaskView {
	return TaskView{ID: t.ID, Kind: t.Kind, AgentID: t.AgentID, TargetAgentID: t.TargetAgentID,
		Status: t.Status, Phase: t.Phase, Message: t.Message, Progress: t.Progress, BytesDone: t.BytesDone,
		BytesTotal: t.BytesTotal, CreatedAt: t.CreatedAt, FinishedAt: t.FinishedAt, DownloadReady: t.DownloadReady}
}

type ListReq struct {
	g.Meta  `path:"/api/image-tasks" method:"get" tags:"ImageTasks" summary:"列出镜像任务"`
	AgentID string `json:"agent_id" in:"query"`
}
type ListRes struct {
	Tasks []TaskView `json:"tasks"`
}

type GetReq struct {
	g.Meta `path:"/api/image-tasks/{taskID}" method:"get" tags:"ImageTasks" summary:"读取镜像任务"`
	TaskID string `json:"-" in:"path"`
}
type GetRes struct {
	Task TaskView `json:"task"`
}

type CancelReq struct {
	g.Meta `path:"/api/image-tasks/{taskID}/cancel" method:"post" tags:"ImageTasks" summary:"取消镜像任务"`
	TaskID string `json:"-" in:"path"`
}
type CancelRes struct {
	Task TaskView `json:"task"`
}

type DownloadReq struct {
	g.Meta `path:"/api/image-tasks/{taskID}/download" method:"get" tags:"ImageTasks" summary:"下载镜像归档"`
	TaskID string `json:"-" in:"path"`
}
type DownloadRes struct{}
