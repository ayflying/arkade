package httpx

import (
	"errors"
	"net/http"
	"time"

	"arkade/hub/internal/store"
	"arkade/hub/internal/user"
	"arkade/protocol/envelope"
)

// preview/apply复用已有安全导入入口，但项目名由路径绑定，不接受正文冒充其他项目。
func handleExternalPreview(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		agent, name, p := requireExternal(w, r, d, user.PermManage)
		if p == nil {
			return
		}
		var req struct {
			Content string `json:"content"`
		}
		if !decodeAndValidate(w, d, r, &req) {
			return
		}
		if err := validateComposeContent(req.Content); err != nil {
			badRequest(w, d, "%s", err)
			return
		}
		preview, cerr := d.Ops.ComposePreview(r.Context(), agent, envelope.ComposePreviewPayload{Name: name, Content: req.Content})
		if cerr != nil {
			respondConnError(w, d, cerr)
			return
		}
		respondSecretJSON(w, d.Log, http.StatusOK, map[string]any{"can_import": preview.CanImport, "config_hash": preview.ConfigHash, "blockers": preview.Blockers, "warnings": preview.Warnings})
	}
}
func handleExternalApply(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		agent, name, p := requireExternal(w, r, d, user.PermManage)
		if p == nil {
			return
		}
		var req importStackRequest
		if !decodeAndValidate(w, d, r, &req) {
			return
		}
		req.Name = name
		if !validImportRequest(w, d, req) {
			return
		}
		hash := composeProjectHash(req.Content, req.EnvContent)
		if !req.Confirmation || req.ExpectedContentHash != hash {
			badRequest(w, d, "请先预检当前配置并明确确认登记；登记不修改正在运行的容器")
			return
		}
		preview, cerr := d.Ops.ComposePreview(r.Context(), agent, envelope.ComposePreviewPayload{Name: name, Content: req.Content, EnvContent: req.EnvContent})
		if cerr != nil {
			respondConnError(w, d, cerr)
			return
		}
		if !preview.CanImport || preview.ConfigHash != hash || len(preview.Blockers) > 0 {
			respondError(w, d.Log, envelope.CodeConflict, "当前配置未通过安全登记预检，草稿仍保留，未改变运行中的项目")
			return
		}
		now := time.Now().UTC()
		rec := store.ComposeStack{ID: "stk-" + randomSuffix(), AgentID: agent, Name: name, Content: req.Content, EnvContent: req.EnvContent, ConfigHash: hash, Version: 1, CreatedAt: now, UpdatedAt: now}
		if err := d.DB.CreateComposeStack(r.Context(), rec); err != nil {
			// Agent下发失败后允许原内容重试，不允许覆盖已同步或不同内容。
			existing, getErr := d.DB.GetComposeStack(r.Context(), agent, name)
			if !errors.Is(err, store.ErrConflict) || getErr != nil || !existing.SyncedAt.IsZero() || existing.ConfigHash != hash {
				respondStoreError(w, d, err, "同名编排已存在，请从已纳管项目继续管理")
				return
			}
			rec = existing
		}
		result, cerr := d.Ops.ComposeAdopt(r.Context(), agent, envelope.ComposeAdoptPayload{Name: name, Content: req.Content, EnvContent: req.EnvContent, ExpectedContentHash: hash, Confirmation: true})
		ok := cerr == nil && result.Adopted && result.ConfigHash == hash
		hint := "已登记到Arkade，未修改宿主原文件，未启动或重建容器"
		if ok {
			if err := d.DB.MarkComposeStackSynced(r.Context(), agent, name); err != nil {
				ok = false
				hint = "登记完成但同步状态保存失败，请刷新确认"
			}
		} else {
			hint = "Hub已保存配置，但Agent尚未完成登记；不会启动或重建容器"
		}
		respondSecretJSON(w, d.Log, http.StatusOK, map[string]any{"stack": toStackView(rec, nil), "sync_ok": ok, "sync_hint": hint, "adopted": ok})
	}
}

type externalCopyRequest struct {
	TargetAgent  string `json:"target_agent_id"`
	TargetName   string `json:"target_name"`
	Content      string `json:"content"`
	Confirmation bool   `json:"confirmation"`
}

// 仅复制配置草案，不调用目标up/adopt；跨服务器数据一致性未经确认前禁止宣称完整迁移。
func handleExternalCopy(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_, _, p := requireExternal(w, r, d, user.PermManage)
		if p == nil {
			return
		}
		var req externalCopyRequest
		if !decodeAndValidate(w, d, r, &req) {
			return
		}
		if !requirePermission(w, d, r, req.TargetAgent, user.PermManage) || !requireComposeName(w, d, req.TargetName) {
			return
		}
		if !req.Confirmation {
			badRequest(w, d, "请确认本次只复制配置，不含数据、镜像或构建文件，目标不会启动")
			return
		}
		if err := validateComposeContent(req.Content); err != nil {
			badRequest(w, d, "%s", err)
			return
		}
		if _, err := d.DB.GetAgent(r.Context(), req.TargetAgent); err != nil {
			respondStoreError(w, d, err, "目标服务器不存在")
			return
		}
		// 除Hub记录，还检查目标引擎已有项目，避免把同名外部业务误认作复制结果。
		list, cerr := d.Ops.ComposeList(r.Context(), req.TargetAgent)
		if cerr != nil {
			respondConnError(w, d, cerr)
			return
		}
		for _, existing := range append(list.External, list.Projects...) {
			if existing.Name == req.TargetName {
				respondError(w, d.Log, envelope.CodeConflict, "目标引擎已有同名项目，请更换名称，未覆盖")
				return
			}
		}
		now := time.Now().UTC()
		rec := store.ComposeStack{ID: "stk-" + randomSuffix(), AgentID: req.TargetAgent, Name: req.TargetName, Content: req.Content, ConfigHash: composeProjectHash(req.Content, ""), Version: 1, CreatedAt: now, UpdatedAt: now}
		if err := d.DB.CreateComposeStack(r.Context(), rec); err != nil {
			respondStoreError(w, d, err, "目标已存在同名项目，未覆盖")
			return
		}
		respondSecretJSON(w, d.Log, http.StatusOK, map[string]any{"stack": toStackView(rec, nil), "scope": []string{"config"}, "data_included": false, "started": false, "source_preserved": true, "message": "仅配置已复制到目标Hub记录；未复制环境文件、构建文件、镜像或卷数据，未下发或启动目标，源项目保留不变"})
	}
}
