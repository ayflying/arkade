package settings

import (
	"context"
	"errors"
	"net/http"
	"time"

	api "arkade/hub/api/settings/v1"
	"arkade/hub/internal/controller"
	"arkade/hub/internal/httpwire"
	service "arkade/hub/internal/service/settings"
	"arkade/hub/internal/store"
	"github.com/gogf/gf/v2/net/ghttp"
)

type Controller struct {
	service *service.Service
	access  controller.Access
}

func New(s *service.Service, access controller.Access) *Controller {
	return &Controller{service: s, access: access}
}

// Public is separately bound so admin routes cannot accidentally become public.
type Public struct{ service *service.Service }

func NewPublic(s *service.Service) *Public            { return &Public{service: s} }
func (c *Controller) admin(ctx context.Context) error { _, err := c.access.Admin(ctx); return err }
func decode(ctx context.Context, out any) error {
	req := httpwire.RestoreRawBody(ctx)
	if req == nil || httpwire.DecodeJSON(req, out) != nil {
		return controller.Failure("bad_request", "请求内容无效")
	}
	return nil
}
func mapped(err error) error {
	if err == nil {
		return nil
	}
	var domain *service.Error
	if errors.As(err, &domain) {
		status := http.StatusBadRequest
		if domain.Code == "bad_gateway" {
			status = http.StatusBadGateway
		}
		if domain.Code == "conflict" {
			status = http.StatusConflict
		}
		if domain.Code == "not_found" {
			status = http.StatusNotFound
		}
		return &controller.Error{Status: status, Code: domain.Code, Message: domain.Message}
	}
	if errors.Is(err, store.ErrNotFound) {
		return controller.Failure("not_found", "设置或登录服务不存在")
	}
	if errors.Is(err, store.ErrConflict) {
		return controller.Failure("conflict", "设置已被他人修改，请刷新后重试")
	}
	if errors.Is(err, store.ErrLastLoginMethod) {
		return controller.Failure("conflict", "该登录服务仍是部分用户的唯一登录方式，不能删除或禁用")
	}
	return controller.ErrInternal
}
func noCache(ctx context.Context) {
	if r := ghttp.RequestFromCtx(ctx); r != nil && r.Response != nil {
		r.Response.Header().Set("Cache-Control", "no-store")
	}
}
func view(p store.SystemSettings) api.SettingsView {
	return api.SettingsView{SiteName: p.SiteName, LoginNotice: p.LoginNotice, RegistrationEnabled: p.RegistrationEnabled, RegistrationGroupID: p.RegistrationGroupID, SessionTTLSeconds: p.SessionTTLSeconds, PasswordMinLength: p.PasswordMinLength, LoginMaxAttempts: p.LoginMaxAttempts, LoginWindowSeconds: p.LoginWindowSeconds, AuditRetentionDays: p.AuditRetentionDays, Version: p.Version, LocalLoginEnabled: true}
}
func settingsRecord(p api.SettingsView) store.SystemSettings {
	return store.SystemSettings{ID: 1, SiteName: p.SiteName, LoginNotice: p.LoginNotice, RegistrationEnabled: p.RegistrationEnabled, RegistrationGroupID: p.RegistrationGroupID, SessionTTLSeconds: p.SessionTTLSeconds, PasswordMinLength: p.PasswordMinLength, LoginMaxAttempts: p.LoginMaxAttempts, LoginWindowSeconds: p.LoginWindowSeconds, AuditRetentionDays: p.AuditRetentionDays, Version: p.Version}
}
func (c *Controller) providerView(p store.OIDCProvider) api.ProviderView {
	return api.ProviderView{ID: p.ID, DisplayName: p.DisplayName, Issuer: p.Issuer, ClientID: p.ClientID, Scopes: p.Scopes, DefaultGroupID: p.DefaultGroupID, Enabled: p.Enabled, AutoRegister: p.AutoRegister, HasClientSecret: p.ClientSecretCipher != "", CallbackURL: c.service.CallbackURL(p.ID), Version: p.Version, CreatedAt: p.CreatedAt.UTC().Format(time.RFC3339), UpdatedAt: p.UpdatedAt.UTC().Format(time.RFC3339)}
}
