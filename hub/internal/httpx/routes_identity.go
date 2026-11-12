package httpx

import (
	"context"
	"net/http"

	"arkade/hub/internal/auth"
	"arkade/hub/internal/controller"
	accountctl "arkade/hub/internal/controller/account"
	oidcctl "arkade/hub/internal/controller/oidclogin"
	settingsctl "arkade/hub/internal/controller/settings"
	"arkade/hub/internal/httpwire"
	"github.com/gogf/gf/v2/net/ghttp"
)

func bindIdentityRoutes(s *ghttp.Server, d Deps) {
	session := newSessionAccessor()
	bindNativeModule(s, d, accountctl.New(d.DB, session, d.Log))
	bindPublicIdentity(s, d, accountctl.NewRegistration(d.DB, d.Log), false)
	if d.Identity == nil {
		return
	}
	access := controller.Access{DB: d.DB, Session: session, Log: d.Log}
	bindNativeModule(s, d, settingsctl.New(d.Identity.settings, access))
	bindPublicIdentity(s, d, settingsctl.NewPublic(d.Identity.settings), false)
	bindPublicIdentity(s, d, oidcctl.NewPublic(d.Identity.oidc, session, d.Identity.secure), true)
	bindNativeModule(s, d, oidcctl.NewPrivate(d.Identity.oidc, session, d.Identity.secure))
}

// Public registration and callback retain the same error, audit and body
// boundaries as private native modules. Callback optionally restores an already
// authenticated actor, required for session-bound identity attachment.
func bindPublicIdentity(s *ghttp.Server, d Deps, ctl any, optionalSession bool) {
	s.Group("/", func(group *ghttp.RouterGroup) {
		group.Middleware(func(r *ghttp.Request) {
			info := &routeInfo{Pattern: r.Router.Uri, Params: map[string]string{}}
			for _, key := range paramsOf(info.Pattern) {
				info.Params[key] = r.GetRouter(key).String()
			}
			r.Request = r.Request.WithContext(withRouteInfo(r.Context(), info))
			mws := []middlewareFunc{recoverer(d.Log), requestLogger(d.Log), securityHeaders}
			if optionalSession {
				mws = append(mws, optionalIdentitySession(d.Auth))
			}
			mws = append(mws, auditMiddleware(d.DB, d.Log))
			gfChainWithSanitizer(d.Log, func(req *ghttp.Request) { writeBareResult(req, d) }, nil,
				func(_ *ghttp.Request, req *http.Request) (*http.Request, error) {
					values := req.URL.Query()
					for key := range values {
						if shadowsPath(key, info.Params) {
							values.Del(key)
						}
					}
					cloned := *req.URL
					cloned.RawQuery = values.Encode()
					req.URL = &cloned
					req.RequestURI = cloned.RequestURI()
					return httpwire.HideRawBody(req), nil
				}, mws...)(r)
		})
		group.Bind(ctl)
	})
}
func optionalIdentitySession(svc *auth.Service) middlewareFunc {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			token := extractToken(r)
			if u, err := svc.Authenticate(r.Context(), token); err == nil {
				ctx := context.WithValue(r.Context(), ctxKeyUser, u)
				ctx = context.WithValue(ctx, ctxKeyToken, token)
				r = r.WithContext(ctx)
			}
			next.ServeHTTP(w, r)
		})
	}
}
