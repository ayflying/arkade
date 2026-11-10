package httpx

import (
	"arkade/hub/internal/agentlink"
	"arkade/hub/internal/store"
	"arkade/protocol/envelope"
	"errors"
	"net/http"
)

// 同一个详情入口兼容Hub配置与服务器现存项目。读取没有登记或生命周期副作用。
func handleUnifiedExternalDetail(w http.ResponseWriter, r *http.Request, d Deps, agent, name string) {
	p, err := externalProject(r.Context(), d, agent, name)
	if err != nil {
		var cerr *agentlink.ConnError
		if errors.As(err, &cerr) {
			respondConnError(w, d, cerr)
		} else {
			respondStoreError(w, d, store.ErrNotFound, "引擎中未找到该编排项目")
		}
		return
	}
	view := composeStackView{AgentID: agent, Name: name, Managed: false, Status: p.Status, Services: p.Services, Running: p.Running, Total: p.Total}
	if view.Services == nil {
		view.Services = []string{}
	}
	if d.AutoAdopt != nil {
		s := d.AutoAdopt.state(agent, name)
		view.AutoAdoptStatus = s.Status
		view.AutoAdoptReason = s.Reason
	}
	respondUnifiedStackDetail(w, r, d, agent, name, view)
}

func respondUnifiedStackDetail(w http.ResponseWriter, r *http.Request, d Deps, agent, name string, view composeStackView) {
	containers := make([]envelope.ContainerSummary, 0)
	out := map[string]any{"stack": view, "containers": containers}
	list, cerr := d.Ops.ListContainers(r.Context(), agent)
	if cerr != nil {
		// 离线/失败不伪装成空项目，保留配置元数据并明确容器状态未知。
		out["containers_error"] = "暂时无法读取容器列表，请检查服务器连接后刷新"
	} else {
		for _, c := range list.Containers {
			if c.StackID == name {
				containers = append(containers, c)
			}
		}
		out["containers"] = containers
	}
	respondJSON(w, d.Log, http.StatusOK, out)
}
