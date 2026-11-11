package httpx

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

	"arkade/hub/internal/imagetask"
	"arkade/hub/internal/user"
	"arkade/protocol/envelope"
)

// 所有归档操作要求manage，迁移同时要求源和目标权限。
// 每次下载/轮询重新校验授权，任务ID不是临时授权票据。
func imageTaskAllowed(w http.ResponseWriter, d Deps, r *http.Request, t imagetask.Task) bool {
	u, ok := userFrom(r.Context())
	if !ok {
		return false
	}
	if u.Role != user.RoleAdmin && t.OwnerID != u.ID {
		respondError(w, d.Log, envelope.CodeForbidden, "不能访问其他用户的镜像任务")
		return false
	}
	if !requirePermission(w, d, r, t.AgentID, user.PermManage) {
		return false
	}
	return t.TargetAgentID == "" || requirePermission(w, d, r, t.TargetAgentID, user.PermManage)
}
func handleStartImageTask(d Deps, kind string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		agent := agentIDFromPath(r)
		if !requirePermission(w, d, r, agent, user.PermManage) {
			return
		}
		var in struct {
			Reference     string `json:"reference"`
			TargetAgentID string `json:"target_agent_id"`
			Confirmation  bool   `json:"confirmation"`
		}
		if err := decodeJSON(r, &in); err != nil {
			badRequest(w, d, "请求内容无效")
			return
		}
		in.Reference = strings.TrimSpace(in.Reference)
		if len(in.Reference) == 0 || len(in.Reference) > 512 || strings.ContainsAny(in.Reference, "\r\n\x00") {
			badRequest(w, d, "请提供有效镜像引用")
			return
		}
		if kind == "migrate" {
			if !in.Confirmation || in.TargetAgentID == "" || in.TargetAgentID == agent {
				badRequest(w, d, "请选择不同的目标服务器并确认迁移")
				return
			}
			if !requirePermission(w, d, r, in.TargetAgentID, user.PermManage) {
				return
			}
		}
		if !d.Registry.IsOnline(agent) {
			respondError(w, d.Log, envelope.CodeAgentOffline, "源服务器离线")
			return
		}
		if kind == "migrate" && !d.Registry.IsOnline(in.TargetAgentID) {
			respondError(w, d.Log, envelope.CodeAgentOffline, "目标服务器离线")
			return
		}
		u, _ := userFrom(r.Context())
		t, err := d.ImageTasks.Start(kind, agent, in.TargetAgentID, in.Reference, u.ID)
		if err != nil {
			badRequest(w, d, "无法创建镜像任务，请检查并发任务数与输入")
			return
		}
		respondJSON(w, d.Log, http.StatusAccepted, map[string]any{"task": t})
	}
}
func handleUploadImage(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		agent := agentIDFromPath(r)
		if !requirePermission(w, d, r, agent, user.PermManage) {
			return
		}
		size := r.ContentLength
		if header := r.Header.Get("X-Archive-Size"); header != "" {
			declared, err := strconv.ParseInt(header, 10, 64)
			if err != nil || declared <= 0 || (size > 0 && declared != size) {
				badRequest(w, d, "归档大小声明与请求长度不符")
				return
			}
		}
		if size <= 0 {
			size, _ = strconv.ParseInt(r.Header.Get("X-Archive-Size"), 10, 64)
		}
		if size <= 0 {
			w.WriteHeader(http.StatusLengthRequired)
			return
		}
		if size > 2<<30 {
			badRequest(w, d, "归档必须提供确切大小，且在1字节到2GiB之间")
			return
		}
		// 限制流式body而不是ParseMultipartForm或ReadAll，防止归档占满Hub内存/磁盘。
		r.Body = http.MaxBytesReader(w, r.Body, size+1)
		defer r.Body.Close()
		// 请求body读也必须有期限；仅取消RPC不足以打断慢客户端的Read。
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Minute)
		defer cancel()
		rc := http.NewResponseController(w)
		_ = rc.SetReadDeadline(time.Now().Add(30 * time.Minute))
		defer rc.SetReadDeadline(time.Time{})
		stop := context.AfterFunc(ctx, func() { _ = rc.SetReadDeadline(time.Now()); _ = r.Body.Close() })
		defer stop()
		r = r.WithContext(ctx)
		u, _ := userFrom(r.Context())
		t, err := d.ImageTasks.Upload(r.Context(), agent, u.ID, r.Body, size)
		if err != nil {
			badRequest(w, d, "上传失败：请检查文件大小、服务器连接或传输配额")
			return
		}
		respondJSON(w, d.Log, http.StatusAccepted, map[string]any{"task": t})
	}
}
func handleListImageTasks(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u, _ := userFrom(r.Context())
		grants := loadGrants(r, d, u)
		items := []imagetask.Task{}
		filter := r.URL.Query().Get("agent_id")
		for _, t := range d.ImageTasks.List() {
			if filter != "" && t.AgentID != filter && t.TargetAgentID != filter {
				continue
			}
			if u.Role != user.RoleAdmin && t.OwnerID != u.ID {
				continue
			}
			if !canAccess(u, grants, t.AgentID, user.PermManage) || t.TargetAgentID != "" && !canAccess(u, grants, t.TargetAgentID, user.PermManage) {
				continue
			}
			items = append(items, t)
		}
		respondSecretJSON(w, d.Log, http.StatusOK, map[string]any{"tasks": items})
	}
}
func imageTaskFromRequest(w http.ResponseWriter, d Deps, r *http.Request) (imagetask.Task, bool) {
	t, err := d.ImageTasks.Get(pathParam(r, "taskID"))
	if err != nil {
		respondError(w, d.Log, envelope.CodeNotFound, "镜像任务不存在或已过期")
		return t, false
	}
	return t, imageTaskAllowed(w, d, r, t)
}
func handleGetImageTask(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		t, ok := imageTaskFromRequest(w, d, r)
		if !ok {
			return
		}
		respondSecretJSON(w, d.Log, http.StatusOK, map[string]any{"task": t})
	}
}
func handleCancelImageTask(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		t, ok := imageTaskFromRequest(w, d, r)
		if !ok {
			return
		}
		t, err := d.ImageTasks.Cancel(t.ID)
		if err != nil {
			badRequest(w, d, "任务不能取消")
			return
		}
		respondJSON(w, d.Log, http.StatusOK, map[string]any{"task": t})
	}
}
func handleDownloadImage(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		t, ok := imageTaskFromRequest(w, d, r)
		if !ok {
			return
		}
		if !t.DownloadReady {
			badRequest(w, d, "导出未完成或下载已过期")
			return
		}
		stream := &imageArchiveWriter{w: w, size: t.ArchiveSize}
		// 流式下载限时写，取消可中断阻塞的网络Write，失败可在过期前重试。
		rc := http.NewResponseController(w)
		_ = rc.SetWriteDeadline(time.Now().Add(30 * time.Minute))
		defer rc.SetWriteDeadline(time.Time{})
		stop := context.AfterFunc(r.Context(), func() { _ = rc.SetWriteDeadline(time.Now()) })
		defer stop()
		// 网络中断后不能补写JSON到tar；下载只保留已成功传输字节，客户端须核验完成。
		if err := d.ImageTasks.Download(r.Context(), t.ID, stream); err != nil {
			if !stream.started {
				badRequest(w, d, "归档暂不可下载、已有下载进行中或已过期")
				return
			}
			d.Log.Warn("镜像归档下载未完成", "task", t.ID)
		}
	}
}
