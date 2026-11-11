package httpx

// handlers_containers_ops.go 承载容器的重启与删除接口。
//
// 与 handlers_agents.go 里的启停分开的理由：
// 这两个接口涉及权限档位差异（restart 只要 operate，remove 要 manage）
// 与危险参数（force / remove_volumes），集中在一处便于审查权限边界。
// 删容器是不可逆操作，权限必须比启停更高一档。

import (
	"net/http"
	"strings"


	"arkade/hub/internal/user"
	"arkade/protocol/envelope"
)

// handleContainerRestart 处理 POST /api/agents/{agentID}/containers/{containerID}/restart。
//
// 权限：operate。重启不改变纳管关系也不销毁对象，
// 与启停同级即可——把它提到 manage 会让日常运维（滚动重启）变得寸步难行。
func handleContainerRestart(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		agentID := pathParam(r, "agentID")
		if !requirePermission(w, d, r, agentID, user.PermOperate) {
			return
		}
		cid := pathParam(r, "containerID")
		if strings.TrimSpace(cid) == "" {
			respondError(w, d.Log, envelope.CodeBadRequest, "容器 ID 不能为空")
			return
		}

		res, cerr := d.Ops.RestartContainer(r.Context(), agentID, cid)
		if cerr != nil {
			respondConnError(w, d, cerr)
			return
		}
		respondJSON(w, d.Log, http.StatusOK, map[string]any{
			"container_id": res.ContainerID,
			"state":        res.State,
		})
	}
}

// handleContainerRemove 处理 DELETE /api/agents/{agentID}/containers/{containerID}。
//
// 权限：manage。删除是唯一不可逆的容器操作（容器上的命名、
// 端口映射、环境变量全都随之消失），必须由对该服务器有完整管理权的人执行。
//
// 查询参数：
//   - force=1          容器在运行也强制删除。默认 false；
//     不提供这个参数的话，运行中的容器根本删不掉。
//   - remove_volumes=1 同时删除关联的匿名卷。**默认 false**——
//     这是数据丢失操作，绝不能用默认值替用户决定。
func handleContainerRemove(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		agentID := pathParam(r, "agentID")
		if !requirePermission(w, d, r, agentID, user.PermManage) {
			return
		}
		cid := pathParam(r, "containerID")
		if strings.TrimSpace(cid) == "" {
			respondError(w, d.Log, envelope.CodeBadRequest, "容器 ID 不能为空")
			return
		}

		force, ok := parseBoolQuery(r.URL.Query().Get("force"))
		if !ok {
			respondError(w, d.Log, envelope.CodeBadRequest, "force 只能是 true/false/1/0")
			return
		}
		removeVolumes, ok := parseBoolQuery(r.URL.Query().Get("remove_volumes"))
		if !ok {
			respondError(w, d.Log, envelope.CodeBadRequest, "remove_volumes 只能是 true/false/1/0")
			return
		}

		res, cerr := d.Ops.RemoveContainer(r.Context(), agentID, cid, force, removeVolumes)
		if cerr != nil {
			respondConnError(w, d, cerr)
			return
		}
		respondJSON(w, d.Log, http.StatusOK, map[string]any{
			"container_id": res.ContainerID,
			"state":        res.State,
		})
	}
}

// parseBoolQuery 解析布尔查询参数，第二个返回值表示"解析是否成功"。
//
// 空串表示"未提供"，取 false——这是所有布尔开关的安全默认值，
// 尤其对 remove_volumes 而言：漏传应该是"不删数据"，而不是"删"。
//
// 为什么无法识别的值要报错而不是当成 false：
// 如果用户传 remove_volumes=yes 而服务端静默按 false 处理，他会以为卷已删；
// 传 remove_volumes=ture（拼错）若被宽松地当成 true，则直接丢数据。
// 两种误判都不可接受，让请求失败才能把问题摆到明面上。
func parseBoolQuery(raw string) (bool, bool) {
	switch strings.TrimSpace(strings.ToLower(raw)) {
	case "", "0", "false", "f", "no", "n":
		return false, true
	case "1", "true", "t", "yes", "y":
		return true, true
	}
	return false, false
}
