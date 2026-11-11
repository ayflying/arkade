package httpx

import (
	"arkade/hub/internal/user"
	"net/http"
	"strings"
)

// 管理权限才可见挂载源路径和完整启动命令；普通查看权限仍可看列表与日志。
func handleContainerDetail(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		agentID := agentIDFromPath(r)
		if !requirePermission(w, d, r, agentID, user.PermManage) {
			return
		}
		id := pathParam(r, "containerID")
		if strings.TrimSpace(id) == "" || len(id) > 256 {
			badRequest(w, d, "容器标识无效")
			return
		}
		result, err := d.Ops.InspectContainer(r.Context(), agentID, id)
		if err != nil {
			respondConnError(w, d, err)
			return
		}
		respondSecretJSON(w, d.Log, http.StatusOK, map[string]any{"container": result})
	}
}
