package agents

import (
	api "arkade/hub/api/agents/v1"
	"arkade/hub/internal/agentlink"
	"arkade/hub/internal/store"
	"arkade/hub/internal/user"
	"arkade/protocol/envelope"
	"context"
	"testing"
	"time"
)

type embeddedExecutor struct{}

func (embeddedExecutor) Call(context.Context, string, []byte) ([]byte, *envelope.Error) {
	return []byte(`{}`), nil
}

func TestEmbeddedAgentManagementProtected(t *testing.T) {
	c, db, session := setup(t)
	ctx := t.Context()
	// 本机不是预建远程服务器：真实启动落库的状态为 offline，而非 pending。
	// 固定身份也使断言能发现意外使用了远程记录的问题。
	const id = "agent-local"
	if err := db.UpsertAgent(ctx, store.AgentRecord{ID: id, Name: "embedded-host", Status: "offline"}); err != nil {
		t.Fatal(err)
	}
	hash := agentlink.HashEnrollmentToken("historical-local-token")
	if err := db.SaveAgentToken(ctx, hash, id, time.Now()); err != nil {
		t.Fatal(err)
	}
	var err error
	if err = c.d.Registry.ReserveLocalID(id); err != nil {
		t.Fatal(err)
	}
	operations := map[string]func() error{
		"delete":  func() error { _, err := c.Delete(ctx, &api.DeleteReq{AgentID: id}); return err },
		"reset":   func() error { _, err := c.ResetToken(ctx, &api.ResetTokenReq{AgentID: id}); return err },
		"disable": func() error { _, err := c.Disable(ctx, &api.DisableReq{AgentID: id}); return err },
	}
	for _, online := range []bool{true, false} {
		if online {
			if err = c.d.Registry.RegisterLocal(id, embeddedExecutor{}); err != nil {
				t.Fatal(err)
			}
		} else {
			c.d.Registry.RemoveLocal(id)
		}
		for name, operation := range operations {
			t.Run(name+map[bool]string{true: "-online", false: "-reserved"}[online], func(t *testing.T) {
				requireError(t, operation(), 400, "本机由 Hub 配置 local.enabled 管理，不支持删除、重置密钥或禁用")
				rec, err := db.GetAgent(ctx, id)
				if err != nil || rec.Disabled {
					t.Fatalf("本机记录被修改: %+v %v", rec, err)
				}
				if got, err := db.LookupAgentByTokenHash(ctx, hash); err != nil || got != id {
					t.Fatalf("本机凭据被修改: %s %v", got, err)
				}
				if c.d.Registry.IsLocal(id) != online || !c.d.Registry.IsReservedLocal(id) {
					t.Fatal("拒绝管理操作不得改变本机生命周期")
				}
			})
		}
	}
	// 历史 disabled 本机可显式解除，但不会因此动态注册或上线。
	if err = db.SetAgentDisabled(ctx, id, true, 1); err != nil {
		t.Fatal(err)
	}
	enabled, err := c.Enable(ctx, &api.EnableReq{AgentID: id})
	if err != nil || enabled.Agent.ID != id || enabled.Agent.Disabled || enabled.Agent.Status != "offline" {
		t.Fatalf("本机 Enable 应仅解除历史禁用: %+v %v", enabled, err)
	}
	if c.d.Registry.IsLocal(id) || c.d.Registry.IsOnline(id) || !c.d.Registry.IsReservedLocal(id) {
		t.Fatal("Enable 不得注册本机执行器或释放保留身份；需 local.enabled 并重启 Hub")
	}
	if got, err := db.LookupAgentByTokenHash(ctx, hash); err != nil || got != id {
		t.Fatalf("Enable 不得更换凭据: %s %v", got, err)
	}
	operations["enable"] = func() error { _, err := c.Enable(ctx, &api.EnableReq{AgentID: id}); return err }
	// 先做角色校验，不能向普通用户暴露本机身份或配置管理细节。
	session.actor.Role = user.RoleUser
	for name, operation := range operations {
		t.Run(name+"-non-admin", func(t *testing.T) { requireError(t, operation(), 403, "该操作仅管理员可用") })
	}
	session.ok = false
	for name, operation := range operations {
		t.Run(name+"-anonymous", func(t *testing.T) { requireError(t, operation(), 401, "登录已失效，请重新登录") })
	}
	session.ok = true
	session.actor.Role = user.RoleAdmin
	display, note := "我的本机", "可修改备注"
	patched, err := c.Patch(ctx, &api.PatchReq{AgentID: id, DisplayName: &display, Note: &note})
	if err != nil || patched.Agent.DisplayName != display || patched.Agent.Note != note {
		t.Fatalf("本机仍应允许修改显示信息: %+v %v", patched, err)
	}
	// 同一 Controller 下普通远程生命周期保持原状。
	remote, err := c.Create(ctx, &api.CreateReq{Name: "remote-host"})
	if err != nil {
		t.Fatal(err)
	}
	rid := remote.Agent.ID
	if _, err = c.Disable(ctx, &api.DisableReq{AgentID: rid}); err != nil {
		t.Fatal(err)
	}
	if _, err = c.Enable(ctx, &api.EnableReq{AgentID: rid}); err != nil {
		t.Fatal(err)
	}
	if _, err = c.ResetToken(ctx, &api.ResetTokenReq{AgentID: rid}); err != nil {
		t.Fatal(err)
	}
	if _, err = c.Delete(ctx, &api.DeleteReq{AgentID: rid}); err != nil {
		t.Fatal(err)
	}
}
