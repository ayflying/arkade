package httpx

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"arkade/hub/internal/audit"
	"arkade/hub/internal/service/oidclogin"
	"arkade/hub/internal/store"
)

// oidcAudit accepts only the deliberately credential-free service event.
func oidcAudit(db *store.DB, log *slog.Logger) oidclogin.AuditHook {
	return identityAuditHook(db, log)
}

func identityAuditHook(db auditWriter, log *slog.Logger) oidclogin.AuditHook {
	return func(_ context.Context, event oidclogin.AuditEvent) {
		action := audit.ActionUserLogin
		switch event.Action {
		case "oidc.login":
		case "oidc.bind":
			action = audit.ActionIdentityBind
		default:
			return
		}
		result := audit.ResultFailed
		if event.Outcome == "success" {
			result = audit.ResultSuccess
		}
		if err := db.WriteAudit(audit.Record{ID: audit.NewID(), Action: action, UserID: event.UserID, Target: event.ProviderID, Result: result, CreatedAt: time.Now().UTC()}); err != nil && log != nil {
			log.Error("写入身份审计失败（不影响业务响应）", "action", action)
		}
	}
}

func credentialAuditPath(path string) bool {
	return path == "/api/auth/register" || path == "/api/auth/login" || path == "/api/account" || path == "/api/account/password" || path == "/api/settings" ||
		strings.HasPrefix(path, "/api/settings/oidc-providers/") || path == "/api/settings/oidc-providers" || strings.HasPrefix(path, "/api/account/identities/")
}
