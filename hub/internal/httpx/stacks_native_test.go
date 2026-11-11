package httpx

import (
	"net/http"
	"strings"
	"testing"
)

func TestStacksNativeBodySourceAndPermissionOrder(t *testing.T) {
	env := newTestEnv(t)
	admin := env.login(t)
	env.seedAgent(t, "native-stack-agent")
	rec, _ := env.do(t, http.MethodPost, "/api/agents/native-stack-agent/stacks?name=injected", `{"name":"wrong","Name":"native","content":"services: {}","env_content":"PASSWORD=hidden"} {"name":"ignored"}`, admin)
	if rec.Code != 200 || strings.Contains(rec.Body.String(), "hidden") {
		t.Fatalf("create strict body: %d %s", rec.Code, rec.Body.String())
	}
	if _, err := env.db.GetComposeStack(t.Context(), "native-stack-agent", "native"); err != nil {
		t.Fatal(err)
	}
	rec, body := env.do(t, http.MethodPut, "/api/agents/native-stack-agent/stacks/native?version=1&content=injected", `{}`, admin)
	if rec.Code != 400 || body.Error.Message != "必须提供 version（乐观锁版本号）" {
		t.Fatalf("query bound body: %d %s", rec.Code, rec.Body.String())
	}
	rec, _ = env.do(t, http.MethodGet, "/api/agents/native-stack-agent/stacks/native/config", "", admin)
	if rec.Code != 200 || rec.Header().Get("Cache-Control") != "no-store" || !strings.Contains(rec.Body.String(), "PASSWORD=hidden") {
		t.Fatalf("config contract: %d %s", rec.Code, rec.Body.String())
	}
	env.createUser(t, admin, "stack-native-reader", testPassword, "user")
	reader := env.loginAs(t, "stack-native-reader", testPassword)
	rec, body = env.do(t, http.MethodPost, "/api/agents/native-stack-agent/stacks", `not json`, reader)
	if rec.Code != 403 || body.Error.Message != "没有该服务器的操作权限" {
		t.Fatalf("permission order: %d %s", rec.Code, rec.Body.String())
	}
}
