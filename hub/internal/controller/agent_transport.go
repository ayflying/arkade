package controller

import (
	"context"
	"net/http"

	api "arkade/hub/api/agents/v1"
	"github.com/gogf/gf/v2/net/ghttp"
)

// AgentTransport only adapts the WebSocket handshake. Protocol authentication,
// enrollment and connection lifetime remain owned by agentlink, not session auth.
type AgentTransport struct{ Handler http.Handler }

func (c *AgentTransport) Connect(ctx context.Context, _ *api.ConnectReq) (*api.ConnectRes, error) {
	req := ghttp.RequestFromCtx(ctx)
	c.Handler.ServeHTTP(req.Response.Writer, req.Request)
	return nil, ErrResponseCommitted
}
