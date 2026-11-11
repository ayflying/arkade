package v1

import (
	"arkade/protocol/envelope"
	"github.com/gogf/gf/v2/frame/g"
)

type ListImagesReq struct {
	g.Meta  `path:"/api/agents/{agentID}/images" method:"get" tags:"Resources"`
	AgentID string `json:"-" in:"path"`
}
type ListImagesRes struct {
	AgentID string                  `json:"agent_id"`
	Images  []envelope.ImageSummary `json:"images"`
}
type RemoveImageReq struct {
	g.Meta  `path:"/api/agents/{agentID}/images/{imageID}" method:"delete" tags:"Resources"`
	AgentID string `json:"-" in:"path"`
	ImageID string `json:"-" in:"path"`
	Force   string `json:"force" in:"query"`
}
type RemoveImageRes struct{}
type ListNetworksReq struct {
	g.Meta  `path:"/api/agents/{agentID}/networks" method:"get" tags:"Resources"`
	AgentID string `json:"-" in:"path"`
}
type ListNetworksRes struct {
	AgentID  string                    `json:"agent_id"`
	Networks []envelope.NetworkSummary `json:"networks"`
}
type RemoveNetworkReq struct {
	g.Meta    `path:"/api/agents/{agentID}/networks/{networkID}" method:"delete" tags:"Resources"`
	AgentID   string `json:"-" in:"path"`
	NetworkID string `json:"-" in:"path"`
}
type RemoveNetworkRes struct{}
type ListVolumesReq struct {
	g.Meta  `path:"/api/agents/{agentID}/volumes" method:"get" tags:"Resources"`
	AgentID string `json:"-" in:"path"`
}
type ListVolumesRes struct {
	AgentID string                   `json:"agent_id"`
	Volumes []envelope.VolumeSummary `json:"volumes"`
}
type RemoveVolumeReq struct {
	g.Meta  `path:"/api/agents/{agentID}/volumes/{name}" method:"delete" tags:"Resources"`
	AgentID string `json:"-" in:"path"`
	Name    string `json:"-" in:"path"`
}
type RemoveVolumeRes struct{}

type VolumeFilesReq struct {
	g.Meta  `path:"/api/agents/{agentID}/volume-files" method:"post" tags:"Resources"`
	AgentID string `json:"-" in:"path"`
}

// VolumeFilesInput is decoded strictly only after authorization. It is not GF-bound.
type VolumeFilesInput = envelope.VolumeFilesPayload
type VolumeFilesRes struct {
	Result envelope.VolumeFilesResult `json:"result"`
}
