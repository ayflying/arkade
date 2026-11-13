package httpx

import (
	"context"
	"testing"

	"arkade/hub/internal/store"
)

// 远程 Agent 即使提交本机 ID，也只能获得自己的身份，不得接管内置执行器。
func TestRuntimeLocalIdentityRejectsRemote(t *testing.T) {
	cfg := embeddedLocalTestConfig(t)
	cfg.Auth.AgentToken = "remote-enrollment-test-token"
	app := embeddedLocalTestApp(t, cfg)
	ctx := context.Background()
	before, err := app.db.GetAgent(ctx, store.LocalAgentID)
	if err != nil {
		t.Fatal(err)
	}
	app.server.SetAddr("127.0.0.1:0")
	if err = app.server.Start(); err != nil {
		t.Fatal(err)
	}
	base := "http://" + app.server.GetListenedAddress()
	conn, ack := dialWith(t, base, cfg.Auth.AgentToken, store.LocalAgentID, "remote-node")
	defer conn.Close()
	if ack.AgentID == store.LocalAgentID {
		t.Fatal("远程握手冒充本机成功")
	}
	after, err := app.db.GetAgent(ctx, store.LocalAgentID)
	if err != nil {
		t.Fatal(err)
	}
	if after.ID != before.ID || after.Name != before.Name || after.Version != before.Version || after.Runtime != before.Runtime || !app.registry.IsLocal(store.LocalAgentID) {
		t.Fatalf("远程握手修改本机身份: before=%+v after=%+v", before, after)
	}
}
