package httpx

// handlers_resources.go 实现镜像 / 网络 / 存储卷的代理接口。
//
// 设计要点：Hub 不缓存这些资源的状态，每次请求都现场问 Agent。
// 理由：镜像是被频繁拉取与清理的资源，缓存必然过时；而这些接口的调用频率
// 不高（用户在资源页手动刷新），实时查询的代价完全可以接受。
//
// 接口清单：
//   GET    /api/agents/{agentID}/images
//   DELETE /api/agents/{agentID}/images/{imageID}?force=
//   GET    /api/agents/{agentID}/networks
//   DELETE /api/agents/{agentID}/networks/{networkID}
//   GET    /api/agents/{agentID}/volumes
//   DELETE /api/agents/{agentID}/volumes/{name}
//
// 权限：列表要 view，删除要 manage（删除卷等于删数据，删除镜像会影响其他容器）。

import (
	"net/http"


	"arkade/hub/internal/user"
	"arkade/protocol/envelope"
)

// handleListImages 处理 GET /api/agents/{agentID}/images。
func handleListImages(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		agentID := agentIDFromPath(r)
		if !requirePermission(w, d, r, agentID, user.PermView) {
			return
		}
		res, cerr := d.Ops.ListImages(r.Context(), agentID)
		if cerr != nil {
			respondConnError(w, d, cerr)
			return
		}
		images := res.Images
		if images == nil {
			images = []envelope.ImageSummary{}
		}
		respondJSON(w, d.Log, http.StatusOK, map[string]any{
			"agent_id": agentID,
			"images":   images,
		})
	}
}

// handleRemoveImage 处理 DELETE /api/agents/{agentID}/images/{imageID}。
//
// force 通过查询参数传入（?force=1）。语义与 docker rmi -f 一致，
// 属于危险操作，因此要求 manage 权限而不是 operate——
// 删除一个被容器使用的镜像会影响线上服务。
func handleRemoveImage(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		agentID := agentIDFromPath(r)
		if !requirePermission(w, d, r, agentID, user.PermManage) {
			return
		}
		imageID := pathParam(r, "imageID")
		if imageID == "" {
			badRequest(w, d, "镜像 ID 不能为空")
			return
		}
		force := r.URL.Query().Get("force") == "1" || r.URL.Query().Get("force") == "true"

		if cerr := d.Ops.RemoveImage(r.Context(), agentID, envelope.ImageRemovePayload{
			ImageID: imageID, Force: force,
		}); cerr != nil {
			respondConnError(w, d, cerr)
			return
		}
		d.Log.Info("删除镜像", "agent", agentID, "image", imageID, "force", force)
		w.WriteHeader(http.StatusNoContent)
	}
}

// handleListNetworks 处理 GET /api/agents/{agentID}/networks。
func handleListNetworks(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		agentID := agentIDFromPath(r)
		if !requirePermission(w, d, r, agentID, user.PermView) {
			return
		}
		res, cerr := d.Ops.ListNetworks(r.Context(), agentID)
		if cerr != nil {
			respondConnError(w, d, cerr)
			return
		}
		networks := res.Networks
		if networks == nil {
			networks = []envelope.NetworkSummary{}
		}
		respondJSON(w, d.Log, http.StatusOK, map[string]any{
			"agent_id": agentID,
			"networks": networks,
		})
	}
}

// handleRemoveNetwork 处理 DELETE /api/agents/{agentID}/networks/{networkID}。
func handleRemoveNetwork(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		agentID := agentIDFromPath(r)
		if !requirePermission(w, d, r, agentID, user.PermManage) {
			return
		}
		networkID := pathParam(r, "networkID")
		if networkID == "" {
			badRequest(w, d, "网络 ID 不能为空")
			return
		}
		if cerr := d.Ops.RemoveNetwork(r.Context(), agentID, envelope.NetworkRemovePayload{
			NetworkID: networkID,
		}); cerr != nil {
			respondConnError(w, d, cerr)
			return
		}
		d.Log.Info("删除网络", "agent", agentID, "network", networkID)
		w.WriteHeader(http.StatusNoContent)
	}
}

// handleListVolumes 处理 GET /api/agents/{agentID}/volumes。
func handleListVolumes(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		agentID := agentIDFromPath(r)
		if !requirePermission(w, d, r, agentID, user.PermView) {
			return
		}
		res, cerr := d.Ops.ListVolumes(r.Context(), agentID)
		if cerr != nil {
			respondConnError(w, d, cerr)
			return
		}
		volumes := res.Volumes
		if volumes == nil {
			volumes = []envelope.VolumeSummary{}
		}
		respondJSON(w, d.Log, http.StatusOK, map[string]any{
			"agent_id": agentID,
			"volumes":  volumes,
		})
	}
}

// handleRemoveVolume 处理 DELETE /api/agents/{agentID}/volumes/{name}。
//
// 这是数据丢失操作：卷里可能存着数据库文件，删了不可恢复。
// 因此要求 manage 权限，并且不提供 force —— Docker 本身也不允许删使用中的卷。
// 若 Agent 返回"卷正在被使用"，错误信息原样透出，让用户知道要先停容器。
func handleRemoveVolume(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		agentID := agentIDFromPath(r)
		if !requirePermission(w, d, r, agentID, user.PermManage) {
			return
		}
		name := pathParam(r, "name")
		if name == "" {
			badRequest(w, d, "存储卷名称不能为空")
			return
		}
		if cerr := d.Ops.RemoveVolume(r.Context(), agentID, envelope.VolumeRemovePayload{
			Name: name,
		}); cerr != nil {
			respondConnError(w, d, cerr)
			return
		}
		d.Log.Info("删除存储卷", "agent", agentID, "volume", name)
		w.WriteHeader(http.StatusNoContent)
	}
}
