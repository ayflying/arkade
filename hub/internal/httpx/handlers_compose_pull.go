package httpx

import (
	"arkade/hub/internal/user"
	"arkade/protocol/envelope"
	"net/http"
	"strconv"
	"strings"
)

// 权限采用operate而非view：命令输出可能含服务端运行细节。
// task命名绑定需在GET返回前检查，避免拿另一个项目的任务ID跨项目读取。
func handleStartComposePull(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		agent, name := agentIDFromPath(r), pathParam(r, "name")
		if !requirePermission(w, d, r, agent, user.PermOperate) || !requireComposeName(w, d, name) {
			return
		}
		var p struct {
			RequestID string `json:"request_id"`
		}
		if !decodeAndValidate(w, d, r, &p) {
			return
		}
		if !validPullKey(p.RequestID) {
			badRequest(w, d, "任务请求标识不合法")
			return
		}
		if _, err := d.DB.GetComposeStack(r.Context(), agent, name); err != nil {
			respondStoreError(w, d, err, "编排不存在")
			return
		}
		task, cerr := d.Ops.ComposePullStart(r.Context(), agent, envelope.ComposePullStartPayload{Name: name, RequestID: p.RequestID})
		if cerr != nil {
			respondConnError(w, d, cerr)
			return
		}
		if task.Name != name || task.TaskID == "" {
			respondError(w, d.Log, "internal", "服务器返回了不匹配的镜像更新任务")
			return
		}
		// 202仅表示任务已接受，不表示镜像已更新成功。完成状态由GET查询。
		respondSecretJSON(w, d.Log, http.StatusAccepted, map[string]any{"task": task})
	}
}
func handleGetComposePull(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		agent, name, id := agentIDFromPath(r), pathParam(r, "name"), pathParam(r, "taskID")
		if !requirePermission(w, d, r, agent, user.PermOperate) || !requireComposeName(w, d, name) {
			return
		}
		if !validPullKey(id) {
			badRequest(w, d, "任务标识不合法")
			return
		}
		after := int64(0)
		if raw := r.URL.Query().Get("after"); raw != "" {
			var err error
			after, err = strconv.ParseInt(raw, 10, 64)
			if err != nil || after < 0 {
				badRequest(w, d, "输出游标不合法")
				return
			}
		}
		task, cerr := d.Ops.ComposePullGet(r.Context(), agent, envelope.ComposePullGetPayload{TaskID: id, After: after})
		if cerr != nil {
			respondConnError(w, d, cerr)
			return
		}
		if task.Name != name || task.TaskID != id {
			respondError(w, d.Log, "not_found", "镜像更新任务不存在")
			return
		}
		respondSecretJSON(w, d.Log, http.StatusOK, map[string]any{"task": task})
	}
}
func validPullKey(s string) bool {
	if len(s) < 8 || len(s) > 128 {
		return false
	}
	return strings.IndexFunc(s, func(c rune) bool {
		return !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_')
	}) < 0
}
