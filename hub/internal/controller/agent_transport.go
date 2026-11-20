package controller

import (
	"context"
	"net/http"

	api "arkade/hub/api/agents/v1"
	"github.com/gogf/gf/v2/net/ghttp"
)

// AgentTransport adapts discovery and WebSocket requests. Authentication and
// connection lifetime remain owned by agentlink, not session auth.
type AgentTransport struct {
	Handler   http.Handler
	Discovery http.Handler
}

func (c *AgentTransport) Discover(ctx context.Context, _ *api.DiscoverReq) (*api.DiscoverRes, error) {
	req := ghttp.RequestFromCtx(ctx)
	if c.Discovery == nil {
		http.NotFound(req.Response.Writer, req.Request)
	} else {
		c.Discovery.ServeHTTP(req.Response.Writer, req.Request)
	}
	return nil, ErrResponseCommitted
}

func (c *AgentTransport) Connect(ctx context.Context, _ *api.ConnectReq) (*api.ConnectRes, error) {
	req := ghttp.RequestFromCtx(ctx)
	c.Handler.ServeHTTP(req.Response.Writer, req.Request)
	return nil, ErrResponseCommitted
}
