package httpx

import (
	"context"
	"net/http"

	api "arkade/hub/api/mcp/v1"
	"arkade/hub/internal/auth"
	"arkade/hub/internal/controller"
	apikeyctl "arkade/hub/internal/controller/apikey"
	"arkade/hub/internal/httpwire"
	mcpsvc "arkade/hub/internal/service/mcp"
	"arkade/hub/internal/user"

	"github.com/gogf/gf/v2/net/ghttp"
)

const maxMCPRequest = 8 << 20

type mcpActorKey struct{}
type mcpInternalKey struct{}
type mcpActor struct {
	user       user.User
	keyID      string
	remoteAddr string
	tool       string
}

// The nested request must not inherit GoFrame's outer request or any of its
// routing/body context. Preserve cancellation and deadlines only.
type mcpRequestContext struct{ context.Context }

func (mcpRequestContext) Value(any) any { return nil }

func bindMCPRoutes(s *ghttp.Server, d Deps) {
	bindNativeModule(s, d, apikeyctl.New(d.Auth, newSessionAccessor()))
	h := mcpsvc.NewHandler(d.HubVersion, func(ctx context.Context, req *http.Request, out http.ResponseWriter, tool string) {
		actor, ok := ctx.Value(mcpActorKey{}).(mcpActor)
		if !ok {
			http.Error(out, "MCP 身份已失效", http.StatusUnauthorized)
			return
		}
		actor.tool = tool
		inner := context.WithValue(mcpRequestContext{ctx}, mcpInternalKey{}, actor)
		req = req.WithContext(inner)
		req.RemoteAddr = actor.remoteAddr
		s.ServeHTTP(out, req)
	})
	s.Group("/", func(group *ghttp.RouterGroup) {
		group.Middleware(gfChainWithSanitizer(d.Log,
			func(req *ghttp.Request) { writeBareResult(req, d) }, nil,
			func(_ *ghttp.Request, req *http.Request) (*http.Request, error) {
				return httpwire.HideRawBody(req), nil
			},
			recoverer(d.Log), requestLogger(d.Log), securityHeaders))
		group.Bind(&mcpTransport{auth: d.Auth, handler: h})
	})
}

type mcpTransport struct {
	auth    *auth.Service
	handler http.Handler
}

func (m *mcpTransport) Transport(ctx context.Context, _ *api.TransportReq) (*api.TransportRes, error) {
	gf := ghttp.RequestFromCtx(ctx)
	req := httpwire.RestoreRawBody(ctx)
	w := gf.Response.Writer
	w.Header().Set("Cache-Control", "no-store")
	if !mcpOriginAllowed(req) {
		http.Error(w, "拒绝跨站 MCP 请求", http.StatusForbidden)
		return nil, controller.ErrResponseCommitted
	}
	token, _ := auth.ParseAPIKeyAuthorization(req.Header)
	actor, keyID, err := m.auth.AuthenticateAPIKey(ctx, token)
	if err != nil {
		w.Header().Set("WWW-Authenticate", "Bearer")
		http.Error(w, "MCP 密钥无效或已撤销", http.StatusUnauthorized)
		return nil, controller.ErrResponseCommitted
	}
	if req.ContentLength > maxMCPRequest {
		http.Error(w, "MCP 请求超过大小限制", http.StatusRequestEntityTooLarge)
		return nil, controller.ErrResponseCommitted
	}
	if req.Body != nil {
		req.Body = http.MaxBytesReader(w, req.Body, maxMCPRequest)
	}
	inner := context.WithValue(ctx, mcpActorKey{}, mcpActor{user: actor, keyID: keyID, remoteAddr: req.RemoteAddr})
	req = req.Clone(inner)
	// Never give the protocol SDK or nested requests the original credential.
	req.Header.Del("Authorization")
	req.Header.Del("Cookie")
	m.handler.ServeHTTP(w, req)
	return nil, controller.ErrResponseCommitted
}

// mcpOriginAllowed 现在不再拦截浏览器来源，原因与 csrf.go 相同：
// Hub 在反向代理后面看到的协议是代理那一跳的明文 http，而浏览器发来的是 https，
// 服务端据此判断"是否同源"永远失败——配置在 HTTPS 域名下的部署会连 MCP 都连不上。
//
// MCP 端点本身的防线与这层无关：它只认 Authorization: Bearer 的 API 密钥，
// 不依赖 Cookie，跨站请求拿不到密钥就调不动。真正的来源限制交给反向代理。
func mcpOriginAllowed(req *http.Request) bool {
	_ = req
	return true
}
