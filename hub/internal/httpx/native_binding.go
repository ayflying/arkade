package httpx

import (
	"net/http"
	"net/url"
	"strings"

	"arkade/hub/internal/httpwire"
	"github.com/gogf/gf/v2/net/ghttp"
)

// GF's converter matches case/underscore aliases; none may redirect path identity.
func shadowsPath(key string, params map[string]string) bool {
	normalize := func(value string) string {
		return strings.ToLower(strings.NewReplacer("_", "", "-", "").Replace(value))
	}
	for pathKey := range params {
		if normalize(key) == normalize(pathKey) {
			return true
		}
	}
	return false
}

// bindNativeModule delegates path/query binding to GF without letting its generic
// body parser read archives or change strict JSON semantics. Controllers own the
// typed decode after permission/existence checks, which preserves error priority.
func bindNativeModule(s *ghttp.Server, d Deps, ctl any) {
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
			gfChainWithSanitizer(d.Log, func(req *ghttp.Request) { writeBareResult(req, d) }, nil,
				func(_ *ghttp.Request, req *http.Request) (*http.Request, error) {
					values := url.Values{}
					for key, list := range req.URL.Query() {
						if shadowsPath(key, info.Params) {
							continue
						}
						if len(list) > 0 {
							values.Set(key, list[0])
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
