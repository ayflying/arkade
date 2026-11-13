package agents

import (
	api "arkade/hub/api/agents/v1"
	"arkade/hub/internal/store"
	"arkade/hub/internal/user"
	"arkade/protocol/envelope"
	"context"
	"testing"
)

type embeddedExecutor struct{}

func (embeddedExecutor) Call(context.Context, string, []byte) ([]byte, *envelope.Error) {
	return []byte(`{}`), nil
}

func TestEmbeddedAgentManagementProtected(t *testing.T) {
	c, db, session := setup(t)
	ctx := t.Context()
	const id = store.LocalAgentID
	if err := db.EnsureEmbeddedLocalAgent(ctx); err != nil {
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
		"enable":  func() error { _, err := c.Enable(ctx, &api.EnableReq{AgentID: id}); return err },
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
				requireError(t, operation(), 400, "本机由 Hub 配置 local.enabled 管理，不支持删除、重置密钥或启用禁用")
				rec, err := db.GetAgent(ctx, id)
				if err != nil || rec.Disabled {
					t.Fatalf("本机记录被修改: %+v %v", rec, err)
				}
				if has, err := db.HasAgentToken(ctx, id); err != nil || has {
					t.Fatalf("本机不得创建网络凭据: %v %v", has, err)
				}
				if c.d.Registry.IsLocal(id) != online || !c.d.Registry.IsReservedLocal(id) {
					t.Fatal("拒绝管理操作不得改变本机生命周期")
				}
			})
		}
	}
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
