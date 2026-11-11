package httpx

import (
	"net/http"
	"net/url"
	"strings"

	"arkade/hub/internal/httpwire"
	"arkade/hub/internal/user"

	"github.com/gogf/gf/v2/net/ghttp"
)

// bindPrivateModule binds a controller once; method metadata remains owned by its API package.
func bindPrivateModule(s *ghttp.Server, d Deps, ctl any, factory func(*ghttp.Request) any) {
	s.Group("/", func(group *ghttp.RouterGroup) {
		group.Middleware(func(r *ghttp.Request) {
			info := &routeInfo{Pattern: r.Router.Uri, Params: map[string]string{}}
			for _, key := range paramsOf(info.Pattern) {
				info.Params[key] = r.GetRouter(key).String()
			}
			r.Request = r.Request.WithContext(withRouteInfo(r.Context(), info))
			mws := []middlewareFunc{recoverer(d.Log), requestLogger(d.Log), securityHeaders}
			mws = append(mws, gateMiddlewares([]gate{authenticatorGF(d.Auth)})...)
			mws = append(mws, auditMiddleware(d.DB, d.Log))
			mws = append(mws, gateMiddlewares([]gate{func(req *ghttp.Request) bool {
				if !strings.HasPrefix(req.URL.Path, "/api/users") && !strings.HasPrefix(req.URL.Path, "/api/groups") {
					return true
				}
				actor, ok := userFrom(req.Context())
				if !ok {
					commit(req, d.Log, http.StatusUnauthorized, nil, "unauthorized", "登录已失效，请重新登录")
					return false
				}
				if actor.Role != user.RoleAdmin {
					commit(req, d.Log, http.StatusForbidden, nil, "forbidden", "该操作仅管理员可用")
					return false
				}
				return true
			}})...)
			gfChainWithSanitizer(d.Log, func(req *ghttp.Request) { writeBareResult(req, d) }, nil,
				func(gf *ghttp.Request, req *http.Request) (*http.Request, error) {
					if req.Method == http.MethodGet {
						// Legacy query readers use Values.Get: first occurrence wins; bodies never filter a GET.
						values := url.Values{}
						for key, list := range req.URL.Query() {
							if len(list) > 0 {
								values.Set(key, list[0])
							}
						}
						cloned := *req.URL
						cloned.RawQuery = values.Encode()
						req.URL = &cloned
						req.RequestURI = cloned.RequestURI()
						req.Body = http.NoBody
						req.ContentLength = 0
						return req, nil
					}
					var target any
					if factory != nil {
						target = factory(gf)
					}
					if target == nil {
						// DELETE and other bodyless methods ignore any supplied body, as before.
						req.Body = http.NoBody
						req.ContentLength = 0
					}
					guarded, err := sanitizeModuleBody(req, target)
					if err == nil && target != nil {
						guarded = guarded.WithContext(httpwire.WithDecodedBody(guarded.Context(), target))
					}
					return guarded, err
				}, mws...)(r)
		})
		group.Bind(ctl)
	})
}
