package stacks

import (
	api "arkade/hub/api/stacks/v1"
	"arkade/hub/internal/stackmigrate"
	"arkade/hub/internal/store"
	"arkade/protocol/envelope"
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestOperationValidationAndSavedOfflineRetry(t *testing.T) {
	c, db, _ := setup(t)
	_, e := c.StartOperation(bodyCtx(`{"request_id":"request_123","action":"restart","remove_volumes":true}`), &api.StartOperationReq{AgentID: "agent", Name: "app"})
	wantError(t, e, 400, "操作参数与所选动作不匹配")
	_, e = c.StartOperation(bodyCtx(`{"request_id":"request_123","action":"up","create":true}`), &api.StartOperationReq{AgentID: "agent", Name: "app"})
	wantError(t, e, 400, "新建编排必须提供配置")
	p := `{"request_id":"request_123","action":"up","create":true,"content":"services: {}","env_content":"PASSWORD=secret"}`
	for i := 0; i < 2; i++ {
		_, e = c.StartOperation(bodyCtx(p), &api.StartOperationReq{AgentID: "agent", Name: "app"})
		wantError(t, e, 503, "配置已保存，尚未确认执行；请使用同一请求重试确认任务")
	}
	rec, e := db.GetComposeStack(context.Background(), "agent", "app")
	if e != nil || rec.Version != 1 || rec.EnvContent != "PASSWORD=secret" {
		t.Fatalf("retry changed asset %+v %v", rec, e)
	}
	_, e = c.StartOperation(bodyCtx(strings.Replace(p, `services: {}`, `other`, 1)), &api.StartOperationReq{AgentID: "agent", Name: "app"})
	wantError(t, e, 409, "请求标识已用于不同的编排操作")
	_, e = c.GetOperation(context.Background(), &api.GetOperationReq{AgentID: "agent", Name: "app", TaskID: "task_12345", After: "-1"})
	wantError(t, e, 400, "输出游标不合法")
	_, e = c.GetOperation(context.Background(), &api.GetOperationReq{AgentID: "agent", Name: "app", TaskID: "task_12345"})
	wantError(t, e, 404, "编排任务不存在")
	_, e = c.StartPull(bodyCtx(`{"request_id":"bad"}`), &api.StartPullReq{AgentID: "agent", Name: "app"})
	wantError(t, e, 400, "任务请求标识不合法")
}
func TestMigrateValidationAndTypedView(t *testing.T) {
	c, db, _ := setup(t)
	if e := db.UpsertAgent(context.Background(), store.AgentRecord{ID: "target", Name: "target", Labels: map[string]string{}}); e != nil {
		t.Fatal(e)
	}
	_, e := c.Migrate(bodyCtx(`{`), &api.MigrateReq{AgentID: "agent", Name: "app"})
	wantError(t, e, 400, "请求内容不是合法的 JSON")
	_, e = c.Migrate(bodyCtx(`{"target_agent_id":"target","confirmation":"app","unknown":true}`), &api.MigrateReq{AgentID: "agent", Name: "app"})
	wantError(t, e, 500, "迁移功能未启用（服务端未装配迁移编排器）")
	v := migrateView("agent", "app", stackmigrate.Snapshot{TaskID: "migration", Source: "agent", Target: "target", Status: "queued", SkippedVolumes: 2, BindDirs: 3, Residual: stackmigrate.Residual{ImagesImported: 4}})
	raw, _ := json.Marshal(v)
	if !strings.Contains(string(raw), `"output":[]`) || !strings.Contains(string(raw), `"images_imported":4`) {
		t.Fatal(string(raw))
	}
}
func TestOperationTaskFinalizationCASFailureIsVisible(t *testing.T) {
	c, db, _ := setup(t)
	_, e := c.Create(bodyCtx(`{"name":"app","content":"services: {}"}`), &api.CreateReq{AgentID: "agent"})
	if e != nil {
		t.Fatal(e)
	}
	op, _, _, e := db.PrepareComposeOperation(context.Background(), store.ComposeOperationRequest{AgentID: "agent", RequestID: "request_123", Name: "app", Action: "remove", Fingerprint: "x", ExpectedVersion: 1})
	if e != nil {
		t.Fatal(e)
	}
	if e := db.AttachOperationTask(context.Background(), "agent", "request_123", "task_12345"); e != nil {
		t.Fatal(e)
	}
	op.TaskID = "task_12345"
	_, e = c.Update(bodyCtx(`{"content":"services: {app: {}}","version":1}`), &api.UpdateReq{AgentID: "agent", Name: "app"})
	if e != nil {
		t.Fatal(e)
	}
	res, e := c.operationResponse(context.Background(), op, envelope.ComposeOperationResult{TaskID: "task_12345", Name: "app", Action: "remove", Status: "succeeded"}, 200)
	if e != nil {
		t.Fatal(e)
	}
	if res.Task.Status != "failed" || res.Task.ExitCode == nil || *res.Task.ExitCode != -1 {
		t.Fatalf("CAS hidden %+v", res)
	}
}
