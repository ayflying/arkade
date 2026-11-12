package oidclogin

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"mime"
	"net/http"
	"time"

	api "arkade/hub/api/oidclogin/v1"
	base "arkade/hub/internal/controller"
	"arkade/hub/internal/httpwire"
	service "arkade/hub/internal/service/oidclogin"
	"github.com/gogf/gf/v2/net/ghttp"
)

type controller struct {
	svc     *service.Service
	session base.SessionAccessor
	secure  bool
}

type Public struct{ c *controller }
type Private struct{ c *controller }

func NewPublic(svc *service.Service, session base.SessionAccessor, secure bool) *Public {
	return &Public{c: &controller{svc: svc, session: session, secure: secure}}
}
func NewPrivate(svc *service.Service, session base.SessionAccessor, secure bool) *Private {
	return &Private{c: &controller{svc: svc, session: session, secure: secure}}
}
func cookieName(state string) string {
	digest := sha256.Sum256([]byte(state))
	return "arkade_oidc_" + hex.EncodeToString(digest[:12])
}
func (c *controller) cookie(ctx context.Context, state, value string, expires time.Time) {
	req := ghttp.RequestFromCtx(ctx)
	if req == nil {
		return
	}
	age := 300
	if value == "" {
		age = -1
	}
	http.SetCookie(req.Response.Writer, &http.Cookie{Name: cookieName(state), Value: value, Path: "/api/auth/oidc/", Expires: expires, MaxAge: age, HttpOnly: true, Secure: c.secure, SameSite: http.SameSiteLaxMode})
}
func committedRedirect(ctx context.Context, to string) error {
	req := ghttp.RequestFromCtx(ctx)
	if req == nil {
		return base.Failure("internal", "请求上下文不可用")
	}
	req.Response.Header().Set("Cache-Control", "no-store")
	req.Response.Header().Set("Referrer-Policy", "no-referrer")
	http.Redirect(req.Response.Writer, req.Request, to, http.StatusFound)
	return base.ErrResponseCommitted
}
func (p *Public) Start(ctx context.Context, req *api.StartReq) (*api.StartRes, error) {
	c := p.c
	result, err := c.svc.Start(ctx, req.ProviderID, req.Redirect)
	if err != nil {
		return nil, base.Failure("bad_request", "无法发起外部登录，请重新尝试")
	}
	c.cookie(ctx, result.State, result.BrowserToken, result.Expires)
	return nil, committedRedirect(ctx, result.URL)
}
func (p *Public) Callback(ctx context.Context, req *api.CallbackReq) (*api.CallbackRes, error) {
	c := p.c
	gf := ghttp.RequestFromCtx(ctx)
	browser := ""
	if gf != nil {
		if ck, err := gf.Request.Cookie(cookieName(req.State)); err == nil {
			browser = ck.Value
		}
	}
	c.cookie(ctx, req.State, "", time.Unix(1, 0))
	actor := ""
	if u, ok := c.session.CurrentUser(ctx); ok {
		actor = u.ID
	}
	code := req.Code
	if req.Error != "" {
		code = ""
	}
	result, err := c.svc.Complete(ctx, req.ProviderID, req.State, browser, code, actor, c.session.Token(ctx))
	if err != nil {
		return nil, committedRedirect(ctx, "/login?oidc_error=login_failed")
	}
	if result.Login != nil {
		c.session.SetCookie(ctx, result.Login.Token, result.Login.ExpiresAt)
	}
	return nil, committedRedirect(ctx, result.Redirect)
}
func (p *Private) Bind(ctx context.Context, req *api.BindReq) (*api.BindRes, error) {
	c := p.c
	u, ok := c.session.CurrentUser(ctx)
	if !ok {
		return nil, base.Failure("unauthorized", "请先登录")
	}
	raw := httpwire.RestoreRawBody(ctx)
	if raw == nil || raw.Body == nil {
		return nil, base.Failure("bad_request", "缺少请求体")
	}
	media, _, mediaErr := mime.ParseMediaType(raw.Header.Get("Content-Type"))
	if mediaErr != nil || media != "application/json" {
		return nil, base.Failure("bad_request", "绑定请求必须为 JSON")
	}
	b, err := io.ReadAll(io.LimitReader(raw.Body, 8193))
	if err != nil || len(b) > 8192 {
		return nil, base.Failure("bad_request", "绑定请求无效")
	}
	var input struct {
		Password string `json:"password"`
		Redirect string `json:"redirect"`
	}
	decoder := json.NewDecoder(bytes.NewReader(b))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&input) != nil || decoder.Decode(&struct{}{}) != io.EOF {
		return nil, base.Failure("bad_request", "绑定请求无效")
	}
	result, err := c.svc.StartBind(ctx, req.ProviderID, u.ID, c.session.Token(ctx), input.Password, input.Redirect)
	if err != nil {
		return nil, base.Failure("bad_request", "绑定失败：请使用当前账号本地密码重新验证，纯 OIDC 账号暂不支持此入口")
	}
	c.cookie(ctx, result.State, result.BrowserToken, result.Expires)
	return &api.BindRes{AuthorizationURL: result.URL}, nil
}
func (p *Private) List(ctx context.Context, _ *api.ListReq) (*api.ListRes, error) {
	c := p.c
	u, ok := c.session.CurrentUser(ctx)
	if !ok {
		return nil, base.Failure("unauthorized", "请先登录")
	}
	list, err := c.svc.Identities(ctx, u.ID)
	if err != nil {
		return nil, base.Failure("internal", "无法查询已绑定身份")
	}
	items := make([]api.IdentityView, 0, len(list))
	for _, i := range list {
		items = append(items, api.IdentityView{ID: i.ID, ProviderID: i.ProviderID, CreatedAt: i.CreatedAt.Unix()})
	}
	return &api.ListRes{Items: items}, nil
}
func (p *Private) Delete(ctx context.Context, req *api.DeleteReq) (*api.DeleteRes, error) {
	c := p.c
	u, ok := c.session.CurrentUser(ctx)
	if !ok {
		return nil, base.Failure("unauthorized", "请先登录")
	}
	if err := c.svc.Unlink(ctx, u.ID, req.IdentityID); err != nil {
		return nil, base.Failure("conflict", "无法解绑：请保留至少一种可用的登录方式")
	}
	return &api.DeleteRes{Deleted: true}, nil
}
