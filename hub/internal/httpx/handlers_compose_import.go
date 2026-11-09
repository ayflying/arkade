package httpx

import (
	"errors"
	"net/http"
	"time"

	"arkade/hub/internal/store"
	"arkade/hub/internal/user"
	"arkade/protocol/envelope"
)

type importStackRequest struct {
	Name                string `json:"name"`
	Content             string `json:"content"`
	EnvContent          string `json:"env_content"`
	ExpectedContentHash string `json:"expected_content_hash"`
	Confirmation        bool   `json:"confirmation"`
}

func validImportRequest(w http.ResponseWriter, d Deps, req importStackRequest) bool {
	if !requireComposeName(w, d, req.Name) {
		return false
	}
	if err := validateComposeContent(req.Content); err != nil {
		badRequest(w, d, "%s", err.Error())
		return false
	}
	if err := validateComposeEnv(req.EnvContent); err != nil {
		badRequest(w, d, "%s", err.Error())
		return false
	}
	return true
}

// 预览内容只交给Agent做安全校验，不读宿主路径、不保存或执行服务动作。
func handleImportStackPreview(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		agentID := agentIDFromPath(r)
		if !requirePermission(w, d, r, agentID, user.PermManage) {
			return
		}
		var req importStackRequest
		if !decodeAndValidate(w, d, r, &req) || !validImportRequest(w, d, req) {
			return
		}
		result, cerr := d.Ops.ComposePreview(r.Context(), agentID, envelope.ComposePreviewPayload{Name: req.Name, Content: req.Content, EnvContent: req.EnvContent})
		if cerr != nil {
			respondError(w, d.Log, cerr.Code, "导入预检失败："+cerr.Message)
			return
		}
		if existing, err := d.DB.GetComposeStack(r.Context(), agentID, req.Name); err == nil && (!existing.SyncedAt.IsZero() || existing.ConfigHash != composeProjectHash(req.Content, req.EnvContent)) {
			result.CanImport = false
			result.Blockers = append(result.Blockers, "Hub已存在该项目或配置与未同步记录不一致，请使用项目编辑而非重复导入")
		} else if err != nil && !errors.Is(err, store.ErrNotFound) {
			respondStoreError(w, d, err, "检查导入项目失败")
			return
		}
		respondSecretJSON(w, d.Log, http.StatusOK, map[string]any{"name": req.Name, "config_hash": composeProjectHash(req.Content, req.EnvContent), "can_import": result.CanImport && result.ConfigHash == composeProjectHash(req.Content, req.EnvContent) && len(result.Blockers) == 0, "blockers": result.Blockers, "warnings": result.Warnings})
	}
}

// 显式确认后仅复制配置，保存失败/下发失败均不伪装成运行中的项目已变更。
func handleImportStack(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		agentID := agentIDFromPath(r)
		if !requirePermission(w, d, r, agentID, user.PermManage) {
			return
		}
		var req importStackRequest
		if !decodeAndValidate(w, d, r, &req) || !validImportRequest(w, d, req) {
			return
		}
		hash := composeProjectHash(req.Content, req.EnvContent)
		if !req.Confirmation || req.ExpectedContentHash != hash {
			badRequest(w, d, "请先预检当前配置并明确确认导入；配置已变化时必须重新预检")
			return
		}
		preview, cerr := d.Ops.ComposePreview(r.Context(), agentID, envelope.ComposePreviewPayload{Name: req.Name, Content: req.Content, EnvContent: req.EnvContent})
		if cerr != nil {
			respondError(w, d.Log, cerr.Code, "导入预检失败："+cerr.Message)
			return
		}
		if !preview.CanImport || preview.ConfigHash != hash || len(preview.Blockers) > 0 {
			respondError(w, d.Log, envelope.CodeConflict, "当前项目不满足安全导入条件，请重新预检并解决阻塞项")
			return
		}
		now := time.Now().UTC()
		rec := store.ComposeStack{ID: "stk-" + randomSuffix(), AgentID: agentID, Name: req.Name, Content: req.Content, EnvContent: req.EnvContent, ConfigHash: hash, Version: 1, CreatedAt: now, UpdatedAt: now}
		// 先保存权威副本，Agent失败保留用户输入并明确展示尚未同步。
		if err := d.DB.CreateComposeStack(r.Context(), rec); err != nil {
			// 首次下发失败允许同内容重试，但不能把已同步项目当成外部项目覆盖。
			if !errors.Is(err, store.ErrConflict) {
				respondStoreError(w, d, err, "导入保存失败")
				return
			}
			existing, getErr := d.DB.GetComposeStack(r.Context(), agentID, req.Name)
			if getErr != nil || !existing.SyncedAt.IsZero() || existing.ConfigHash != hash {
				respondStoreError(w, d, err, "项目已存在，不能重复导入")
				return
			}
			rec = existing
		}
		result, cerr := d.Ops.ComposeAdopt(r.Context(), agentID, envelope.ComposeAdoptPayload{Name: req.Name, Content: req.Content, EnvContent: req.EnvContent, ExpectedContentHash: hash, Confirmation: true})
		syncOK := cerr == nil && result.Adopted && result.ConfigHash == hash
		hint := "仅导入配置，未启动、停止或重建任何容器"
		if !syncOK {
			hint = "导入配置已保存，但Agent未确认正确接管；请重新执行导入，不要启动或重建"
			if cerr != nil {
				hint = "导入配置已保存，但Agent接管未完成；请勿执行启动或重建：" + cerr.Message
			}
		} else {
			if err := d.DB.MarkComposeStackSynced(r.Context(), agentID, req.Name); err != nil {
				d.Log.Error("记录导入同步时间失败", "agent", agentID, "name", req.Name)
			}
		}
		respondJSON(w, d.Log, http.StatusOK, map[string]any{"stack": toStackView(rec, nil), "sync_ok": syncOK, "sync_hint": hint, "adopted": syncOK && result.Adopted})
	}
}
