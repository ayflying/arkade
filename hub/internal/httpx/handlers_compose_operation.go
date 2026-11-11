package httpx

import (
	"arkade/hub/internal/store"
	"arkade/hub/internal/user"
	"arkade/protocol/envelope"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strconv"
)

type operationRequest struct {
	RequestID          string   `json:"request_id"`
	Action             string   `json:"action"`
	Services           []string `json:"services,omitempty"`
	RemoveVolumes      bool     `json:"remove_volumes,omitempty"`
	IncludeVolumes     bool     `json:"include_volumes,omitempty"`
	Content            *string  `json:"content,omitempty"`
	EnvContent         *string  `json:"env_content,omitempty"`
	Version            int64    `json:"version,omitempty"`
	ExpectedConfigHash string   `json:"expected_config_hash,omitempty"`
	Create             bool     `json:"create,omitempty"`
}

func operationPermission(action string, save bool) user.Permission {
	if action == "remove" || save {
		return user.PermManage
	}
	return user.PermOperate
}
func validOperation(a string) bool {
	switch a {
	case "up", "down", "stop", "restart", "pull", "rebuild", "remove", "backup":
		return true
	}
	return false
}
func operationFingerprint(p operationRequest) string {
	p.RequestID = ""
	p.Services = append([]string(nil), p.Services...)
	sort.Strings(p.Services)
	raw, _ := json.Marshal(p)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
func handleStartComposeOperation(d Deps) http.HandlerFunc {
	watchers := &operationWatchers{}
	return func(w http.ResponseWriter, r *http.Request) {
		agent, name := agentIDFromPath(r), pathParam(r, "name")
		if !requirePermission(w, d, r, agent, user.PermOperate) || !requireComposeName(w, d, name) {
			return
		}
		var p operationRequest
		if !decodeAndValidate(w, d, r, &p) {
			return
		}
		if !validPullKey(p.RequestID) || !validOperation(p.Action) {
			badRequest(w, d, "操作或请求标识不合法")
			return
		}
		save := p.Content != nil || p.EnvContent != nil || p.Create
		if !requirePermission(w, d, r, agent, operationPermission(p.Action, save)) {
			return
		}
		if (p.RemoveVolumes && p.Action != "remove" && p.Action != "down") || (p.IncludeVolumes && p.Action != "backup") || (len(p.Services) > 0 && p.Action != "stop" && p.Action != "restart") {
			badRequest(w, d, "操作参数与所选动作不匹配")
			return
		}
		if save && p.Action != "up" {
			badRequest(w, d, "只有部署操作可以保存配置")
			return
		}
		if p.Create && p.Content == nil {
			badRequest(w, d, "新建编排必须提供配置")
			return
		}
		if save && !p.Create && p.Version <= 0 {
			badRequest(w, d, "保存并部署必须提供读取时的 version")
			return
		}
		if p.Content != nil {
			if err := validateComposeContent(*p.Content); err != nil {
				badRequest(w, d, "%s", err.Error())
				return
			}
		}
		if p.EnvContent != nil {
			if err := validateComposeEnv(*p.EnvContent); err != nil {
				badRequest(w, d, "%s", err.Error())
				return
			}
		}
		// 已知任务直接读取，不再依赖可能已删除或更新的项目记录。
		if prior, err := d.DB.FindOperationByRequest(r.Context(), agent, p.RequestID); err == nil {
			if prior.Name != name || prior.Action != p.Action || prior.Fingerprint != operationFingerprint(p) {
				respondError(w, d.Log, codeConflict, "请求标识已用于不同的编排操作")
				return
			}
			if prior.TaskID != "" {
				task, e := d.Ops.ComposeOperationGet(r.Context(), agent, envelope.ComposeOperationGetPayload{TaskID: prior.TaskID})
				if e != nil {
					respondConnError(w, d, e)
					return
				}
				if task.Name != name || task.Action != p.Action || task.TaskID != prior.TaskID {
					respondError(w, d.Log, "not_found", "编排任务不存在")
					return
				}
				watchers.watch(d, prior)
				respondOperationTask(w, r, d, prior, task, http.StatusAccepted)
				return
			}
		} else if !errors.Is(err, store.ErrNotFound) {
			respondStoreError(w, d, err, "读取编排任务失败")
			return
		}
		// Prepare把保存和幂等记录放在同一事务：网络重试不能再递增版本或改投新配置。
		env := ""
		content := ""
		if p.Content != nil {
			content = *p.Content
		}
		if p.EnvContent != nil {
			env = *p.EnvContent
		}
		expectedVersion := p.Version
		if !p.Create {
			old, err := d.DB.GetComposeStack(r.Context(), agent, name)
			if err != nil {
				respondStoreError(w, d, err, "编排不存在")
				return
			}
			if !save {
				expectedVersion = old.Version
			}
			if p.Content == nil {
				content = old.Content
			}
			if p.EnvContent == nil {
				env = old.EnvContent
			}
		}
		hash := ""
		if save {
			hash = composeProjectHash(content, env)
		}
		op, rec, _, err := d.DB.PrepareComposeOperation(r.Context(), store.ComposeOperationRequest{AgentID: agent, RequestID: p.RequestID, Name: name, Action: p.Action, Fingerprint: operationFingerprint(p), SaveContent: p.Content, SaveEnv: p.EnvContent, ExpectedVersion: expectedVersion, ExpectedHash: p.ExpectedConfigHash, Create: p.Create, StackID: "stk-" + randomSuffix(), ConfigHash: hash})
		if err != nil {
			respondStoreError(w, d, err, "准备编排操作失败")
			return
		}
		var task envelope.ComposeOperationResult
		if op.TaskID != "" {
			result, e := d.Ops.ComposeOperationGet(r.Context(), agent, envelope.ComposeOperationGetPayload{TaskID: op.TaskID})
			if e != nil {
				respondConnError(w, d, e)
				return
			}
			task = result
		} else {
			payload := envelope.ComposeOperationStartPayload{Name: name, RequestID: p.RequestID, Action: p.Action, Services: p.Services, RemoveVolumes: p.RemoveVolumes, IncludeVolumes: p.IncludeVolumes}
			if p.Action == "up" {
				payload.Config = rec.Content
				payload.Env = rec.EnvContent
				payload.ExpectHash = rec.ConfigHash
			}
			result, e := d.Ops.ComposeOperationStart(r.Context(), agent, payload)
			if e != nil {
				if save {
					respondError(w, d.Log, e.Code, "配置已保存，尚未确认执行；请使用同一请求重试确认任务")
				} else {
					respondConnError(w, d, e)
				}
				return
			}
			task = result
			if task.Name != name || task.Action != p.Action || task.TaskID == "" {
				respondError(w, d.Log, "internal", "服务器返回了不匹配的编排任务")
				return
			}
			if err := d.DB.AttachOperationTask(r.Context(), agent, p.RequestID, task.TaskID); err != nil {
				respondStoreError(w, d, err, "任务已受理但记录失败，请用同一请求重试")
				return
			}
		}
		op.TaskID = task.TaskID
		watchers.watch(d, op)
		respondOperationTask(w, r, d, op, task, http.StatusAccepted)
	}
}

func handleGetComposeOperation(d Deps) http.HandlerFunc {
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
			var e error
			after, e = strconv.ParseInt(raw, 10, 64)
			if e != nil || after < 0 {
				badRequest(w, d, "输出游标不合法")
				return
			}
		}
		op, err := d.DB.FindOperationByTask(r.Context(), agent, name, id)
		if err != nil {
			// ============================ 迁移任务回落 ============================
			// 跨服务器迁移的任务**不写** operation_tasks 表（由 stackmigrate.Manager
			// 在内存里维护），因此这里查不到是正常的。若直接 404，前端会把
			// 「查看迁移日志」显示成「后端尚未部署」。
			//
			// **只在"确实没有这条记录"时回落**：数据库真故障必须原样报错，
			// 不能借回落把一个 DB 错误变成看似正常的迁移响应——
			// 那会把故障藏起来，也会让真实的存储问题无从发现。
			if errors.Is(err, store.ErrNotFound) {
				if handleGetMigrateTask(d, w, r, agent, name, id) {
					return
				}
			}
			respondStoreError(w, d, err, "编排任务不存在")
			return
		}
		if !requirePermission(w, d, r, agent, operationPermission(op.Action, false)) {
			return
		}
		task, e := d.Ops.ComposeOperationGet(r.Context(), agent, envelope.ComposeOperationGetPayload{TaskID: id, After: after})
		if e != nil {
			respondConnError(w, d, e)
			return
		}
		if task.Name != name || task.TaskID != id || task.Action != op.Action {
			respondError(w, d.Log, "not_found", "编排任务不存在")
			return
		}
		respondOperationTask(w, r, d, op, task, http.StatusOK)
	}
}
func respondOperationTask(w http.ResponseWriter, r *http.Request, d Deps, op store.ComposeOperation, task envelope.ComposeOperationResult, status int) {
	if task.Status == "succeeded" || task.Status == "failed" {
		done, err := d.DB.FinalizeComposeOperation(r.Context(), op, task.Status == "succeeded")
		if err != nil {
			respondStoreError(w, d, err, "执行结束但Hub状态尚未确认，请重试读取")
			return
		}
		op = done
		if done.FinalError != "" && task.Status == "succeeded" {
			code := -1
			task.Status = "failed"
			task.Error = "服务器执行已结束，但编排配置已被修改或移除，Hub未覆盖新记录；请刷新确认"
			task.ExitCode = &code
		}
	}
	// 附加字段只含元数据，绝不把spec/env内容回传operate客户端。
	raw, _ := json.Marshal(task)
	view := map[string]any{}
	_ = json.Unmarshal(raw, &view)
	view["stack_version"] = op.StackVersion
	view["record_removed"] = op.RecordRemoved
	respondSecretJSON(w, d.Log, status, map[string]any{"task": view})
}
