package stacks

import (
	api "arkade/hub/api/stacks/v1"
	"arkade/hub/internal/controller"
	comp "arkade/hub/internal/service/compose"
	"arkade/hub/internal/store"
	"arkade/hub/internal/user"
	"arkade/protocol/envelope"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"time"
)

func operationPermission(action string, save bool) user.Permission {
	if action == "remove" || save {
		return user.PermManage
	}
	return user.PermOperate
}
func validOperation(s string) bool {
	switch s {
	case "up", "down", "stop", "restart", "pull", "rebuild", "remove", "backup":
		return true
	}
	return false
}
func fingerprint(p api.OperationInput) string {
	p.RequestID = ""
	p.Services = append([]string(nil), p.Services...)
	sort.Strings(p.Services)
	raw, _ := json.Marshal(p)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
func (c *Controller) StartOperation(ctx context.Context, r *api.StartOperationReq) (*api.OperationRes, error) {
	if e := c.check(ctx, r.AgentID, r.Name, user.PermOperate); e != nil {
		return nil, e
	}
	var p api.OperationInput
	if e := decode(ctx, &p); e != nil {
		return nil, e
	}
	if !comp.ValidTaskKey(p.RequestID) || !validOperation(p.Action) {
		return nil, controller.Failure("bad_request", "操作或请求标识不合法")
	}
	save := p.Content != nil || p.EnvContent != nil || p.Create
	if e := c.access.Require(ctx, r.AgentID, operationPermission(p.Action, save)); e != nil {
		return nil, e
	}
	if p.RemoveVolumes && p.Action != "remove" && p.Action != "down" || p.IncludeVolumes && p.Action != "backup" || len(p.Services) > 0 && p.Action != "stop" && p.Action != "restart" {
		return nil, controller.Failure("bad_request", "操作参数与所选动作不匹配")
	}
	if save && p.Action != "up" {
		return nil, controller.Failure("bad_request", "只有部署操作可以保存配置")
	}
	if p.Create && p.Content == nil {
		return nil, controller.Failure("bad_request", "新建编排必须提供配置")
	}
	if save && !p.Create && p.Version <= 0 {
		return nil, controller.Failure("bad_request", "保存并部署必须提供读取时的 version")
	}
	if p.Content != nil {
		if e := comp.ValidateContent(*p.Content); e != nil {
			return nil, controller.Failure("bad_request", e.Error())
		}
	}
	if p.EnvContent != nil {
		if e := comp.ValidateEnv(*p.EnvContent); e != nil {
			return nil, controller.Failure("bad_request", e.Error())
		}
	}
	if prior, e := c.d.DB.FindOperationByRequest(ctx, r.AgentID, p.RequestID); e == nil {
		if prior.Name != r.Name || prior.Action != p.Action || prior.Fingerprint != fingerprint(p) {
			return nil, controller.Failure("conflict", "请求标识已用于不同的编排操作")
		}
		if prior.TaskID != "" {
			task, ce := c.d.Ops.ComposeOperationGet(ctx, r.AgentID, envelope.ComposeOperationGetPayload{TaskID: prior.TaskID})
			if ce != nil {
				return nil, controller.ConnectionError(ce)
			}
			if task.Name != r.Name || task.Action != p.Action || task.TaskID != prior.TaskID {
				return nil, controller.Failure("not_found", "编排任务不存在")
			}
			c.watchers.watch(c.d, prior)
			return c.operationResponse(ctx, prior, task, 202)
		}
	} else if !errors.Is(e, store.ErrNotFound) {
		return nil, c.storeError(e, "读取编排任务失败")
	}
	env, content := "", ""
	if p.Content != nil {
		content = *p.Content
	}
	if p.EnvContent != nil {
		env = *p.EnvContent
	}
	version := p.Version
	if !p.Create {
		old, e := c.d.DB.GetComposeStack(ctx, r.AgentID, r.Name)
		if e != nil {
			return nil, c.storeError(e, "编排不存在")
		}
		if !save {
			version = old.Version
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
		hash = comp.ProjectHash(content, env)
	}
	op, rec, _, e := c.d.DB.PrepareComposeOperation(ctx, store.ComposeOperationRequest{AgentID: r.AgentID, RequestID: p.RequestID, Name: r.Name, Action: p.Action, Fingerprint: fingerprint(p), SaveContent: p.Content, SaveEnv: p.EnvContent, ExpectedVersion: version, ExpectedHash: p.ExpectedConfigHash, Create: p.Create, StackID: randomID(), ConfigHash: hash})
	if e != nil {
		return nil, c.storeError(e, "准备编排操作失败")
	}
	var task envelope.ComposeOperationResult
	if op.TaskID != "" {
		result, ce := c.d.Ops.ComposeOperationGet(ctx, r.AgentID, envelope.ComposeOperationGetPayload{TaskID: op.TaskID})
		if ce != nil {
			return nil, controller.ConnectionError(ce)
		}
		task = result
	} else {
		payload := envelope.ComposeOperationStartPayload{Name: r.Name, RequestID: p.RequestID, Action: p.Action, Services: p.Services, RemoveVolumes: p.RemoveVolumes, IncludeVolumes: p.IncludeVolumes}
		if p.Action == "up" {
			payload.Config = rec.Content
			payload.Env = rec.EnvContent
			payload.ExpectHash = rec.ConfigHash
		}
		// 编排会重建多个容器，短暂抑制该服务器容器告警，不抑制服务器失联。
		if c.d.SuppressMonitor != nil && (p.Action == "up" || p.Action == "down" || p.Action == "stop" || p.Action == "restart" || p.Action == "remove") {
			c.d.SuppressMonitor(r.AgentID, "*", 5*time.Minute)
		}
		result, ce := c.d.Ops.ComposeOperationStart(ctx, r.AgentID, payload)
		if ce != nil {
			if save {
				return nil, controller.Failure(ce.Code, "配置已保存，尚未确认执行；请使用同一请求重试确认任务")
			}
			return nil, controller.ConnectionError(ce)
		}
		task = result
		if task.Name != r.Name || task.Action != p.Action || task.TaskID == "" {
			return nil, controller.Failure("internal", "服务器返回了不匹配的编排任务")
		}
		if e := c.d.DB.AttachOperationTask(ctx, r.AgentID, p.RequestID, task.TaskID); e != nil {
			return nil, c.storeError(e, "任务已受理但记录失败，请用同一请求重试")
		}
	}
	op.TaskID = task.TaskID
	c.watchers.watch(c.d, op)
	return c.operationResponse(ctx, op, task, 202)
}
func (c *Controller) GetOperation(ctx context.Context, r *api.GetOperationReq) (*api.OperationRes, error) {
	if e := c.check(ctx, r.AgentID, r.Name, user.PermOperate); e != nil {
		return nil, e
	}
	if !comp.ValidTaskKey(r.TaskID) {
		return nil, controller.Failure("bad_request", "任务标识不合法")
	}
	after, e := cursor(r.After)
	if e != nil {
		return nil, e
	}
	op, e := c.d.DB.FindOperationByTask(ctx, r.AgentID, r.Name, r.TaskID)
	if e != nil {
		if errors.Is(e, store.ErrNotFound) {
			if result, handled, err := c.migrateFallback(ctx, r.AgentID, r.Name, r.TaskID, after); handled {
				return result, err
			}
		}
		return nil, c.storeError(e, "编排任务不存在")
	}
	if e := c.access.Require(ctx, r.AgentID, operationPermission(op.Action, false)); e != nil {
		return nil, e
	}
	task, ce := c.d.Ops.ComposeOperationGet(ctx, r.AgentID, envelope.ComposeOperationGetPayload{TaskID: r.TaskID, After: after})
	if ce != nil {
		return nil, controller.ConnectionError(ce)
	}
	if task.Name != r.Name || task.TaskID != r.TaskID || task.Action != op.Action {
		return nil, controller.Failure("not_found", "编排任务不存在")
	}
	return c.operationResponse(ctx, op, task, 200)
}
func (c *Controller) operationResponse(ctx context.Context, op store.ComposeOperation, task envelope.ComposeOperationResult, status int) (*api.OperationRes, error) {
	if task.Status == "succeeded" || task.Status == "failed" {
		done, e := c.d.DB.FinalizeComposeOperation(ctx, op, task.Status == "succeeded")
		if e != nil {
			return nil, c.storeError(e, "执行结束但Hub状态尚未确认，请重试读取")
		}
		op = done
		if done.FinalError != "" && task.Status == "succeeded" {
			code := -1
			task.Status = "failed"
			task.Error = "服务器执行已结束，但编排配置已被修改或移除，Hub未覆盖新记录；请刷新确认"
			task.ExitCode = &code
		}
	}
	secret(ctx)
	controller.Status(ctx, status)
	return &api.OperationRes{Task: &api.OperationTask{ComposeOperationResult: task, StackVersion: op.StackVersion, RecordRemoved: op.RecordRemoved}}, nil
}
