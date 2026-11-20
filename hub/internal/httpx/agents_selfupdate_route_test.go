package httpx

import (
	"net/http"
	"strings"
	"testing"

	"arkade/hub/internal/auth"
	agents "arkade/hub/internal/controller/agents"
	"github.com/gogf/gf/v2/net/ghttp"
)

func TestSelfUpdateStatusRouteRequiresSession(t *testing.T) {
	s := ghttp.GetServer(uniqueServerName())
	s.SetDumpRouterMap(false)
	bindNativeModule(s, Deps{Auth: auth.NewService(nil, 0)}, agents.New(agents.Deps{Session: newSessionAccessor()}))
	base := startRouterForTest(t, s)
	resp, body := doHTTP(t, http.MethodGet, base+"/api/agents/agent-local/self-update/0123456789abcdef0123456789abcdef", "", nil)
	if resp.StatusCode != http.StatusUnauthorized || !strings.Contains(string(body), `"unauthorized"`) {
		t.Fatalf("更新状态路由未要求登录：%d %s", resp.StatusCode, body)
	}
}
