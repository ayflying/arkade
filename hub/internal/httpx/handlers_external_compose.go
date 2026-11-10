package httpx

// 外部项目的“能看”不依赖是否可安全复制配置，GET绝不自动登记或执行Compose。
import (
	"context"
	"errors"
	"net/http"

	"arkade/hub/internal/agentlink"
	"arkade/hub/internal/store"
	"arkade/hub/internal/user"
	"arkade/protocol/envelope"
	"github.com/go-chi/chi/v5"
)

func externalProject(ctx context.Context, d Deps, agent, name string) (*envelope.ComposeProjectSummary, error) {
	list, cerr := d.Ops.ComposeList(ctx, agent)
	if cerr != nil {
		return nil, cerr
	}
	// 已自动纳管的项目也可从原详情链接访问，避免扫描与点击之间竞态造成404。
	for _, p := range append(list.External, list.Projects...) {
		if p.Name == name {
			return &p, nil
		}
	}
	return nil, store.ErrNotFound
}
func requireExternal(w http.ResponseWriter, r *http.Request, d Deps, perm user.Permission) (string, string, *envelope.ComposeProjectSummary) {
	agent, name := agentIDFromPath(r), chi.URLParam(r, "name")
	if !requirePermission(w, d, r, agent, perm) || !requireComposeName(w, d, name) {
		return "", "", nil
	}
	p, err := externalProject(r.Context(), d, agent, name)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			respondStoreError(w, d, err, "引擎中未找到该编排项目")
		} else {
			var cerr *agentlink.ConnError
			if errors.As(err, &cerr) {
				respondConnError(w, d, cerr)
			} else {
				respondError(w, d.Log, envelope.CodeRuntimeFailed, "无法读取服务器编排列表")
			}
		}
		return "", "", nil
	}
	return agent, name, p
}
func handleExternalStack(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		agent, name, p := requireExternal(w, r, d, user.PermView)
		if p == nil {
			return
		}
		list, cerr := d.Ops.ListContainers(r.Context(), agent)
		if cerr != nil {
			respondConnError(w, d, cerr)
			return
		}
		containers := make([]envelope.ContainerSummary, 0)
		for _, c := range list.Containers {
			if c.StackID == name {
				containers = append(containers, c)
			}
		}
		view := externalStackView{Name: name, Status: p.Status, Services: p.Services, Running: p.Running, Total: p.Total, Managed: p.Managed}
		if d.AutoAdopt != nil {
			s := d.AutoAdopt.state(agent, name)
			view.AutoAdoptStatus = s.Status
			view.AutoAdoptReason = s.Reason
		}
		respondJSON(w, d.Log, http.StatusOK, map[string]any{"project": view, "containers": containers})
	}
}
func handleExternalConfig(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		agent, name, p := requireExternal(w, r, d, user.PermManage)
		if p == nil {
			return
		}
		out := map[string]any{"readable": false, "reason": "当前无法读取原始配置；可查看容器和日志，不会把读取失败当成空文件"}
		source, cerr := d.Ops.ComposeSourceRaw(r.Context(), agent, name)
		if cerr == nil {
			if source.Name == name && source.ConfigHash == composeProjectHash(source.Content, "") && validateComposeContent(source.Content) == nil {
				out["readable"] = true
				out["content"] = source.Content
				out["config_hash"] = source.ConfigHash
				out["reason"] = ""
				out["blockers"] = source.Blockers
			} else if len(source.Blockers) > 0 {
				out["reason"] = safeAutoReason(source.Blockers)
			}
		} else {
			out["reason"] = "读取配置失败，请检查项目来源、文件权限及Agent状态"
		}
		if draft, err := d.DB.GetExternalComposeDraft(r.Context(), agent, name); err == nil {
			out["draft"] = draft
		} else if !errors.Is(err, store.ErrNotFound) {
			respondStoreError(w, d, err, "读取草稿失败")
			return
		}
		respondSecretJSON(w, d.Log, http.StatusOK, out)
	}
}

type externalDraftRequest struct {
	Content         string `json:"content"`
	SourceHash      string `json:"source_hash"`
	ExpectedVersion int64  `json:"expected_version"`
}

func handleExternalDraft(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		agent, name, p := requireExternal(w, r, d, user.PermManage)
		if p == nil {
			return
		}
		var req externalDraftRequest
		if !decodeAndValidate(w, d, r, &req) {
			return
		}
		if err := validateComposeContent(req.Content); err != nil {
			badRequest(w, d, "%s", err)
			return
		}
		if req.ExpectedVersion < 0 || len(req.SourceHash) > 64 {
			badRequest(w, d, "草稿版本或来源哈希不合法")
			return
		}
		draft, err := d.DB.SaveExternalComposeDraft(r.Context(), agent, name, req.Content, req.SourceHash, req.ExpectedVersion)
		if err != nil {
			respondStoreError(w, d, err, "草稿已被其他页面修改，请重新读取后再保存")
			return
		}
		respondSecretJSON(w, d.Log, http.StatusOK, map[string]any{"draft": draft, "message": "草稿已保存到Hub，未修改宿主文件，未重启或重建容器"})
	}
}
