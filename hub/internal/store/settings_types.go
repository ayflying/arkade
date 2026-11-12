package store

import (
	"github.com/gogf/gf/v2/errors/gerror"
	"time"
)

type SystemSettings struct {
	ID                                                                                                      int
	SiteName, LoginNotice, RegistrationGroupID                                                              string
	RegistrationEnabled                                                                                     bool
	SessionTTLSeconds, PasswordMinLength, LoginMaxAttempts, LoginWindowSeconds, AuditRetentionDays, Version int64
}
type OIDCProvider struct {
	ID, DisplayName, Issuer, ClientID, ClientSecretCipher, Scopes, DefaultGroupID string
	Enabled, AutoRegister                                                         bool
	Version                                                                       int64
	CreatedAt, UpdatedAt                                                          time.Time
}
type Identity struct {
	ID, UserID, ProviderID, Subject string
	CreatedAt                       time.Time
}

var (
	ErrRegistrationDisabled         = gerror.New("registration disabled")
	ErrRegistrationGroupUnavailable = gerror.New("registration group unavailable")
	ErrPasswordPolicyChanged        = gerror.New("password policy changed")
	ErrLastLocalAdmin               = gerror.New("cannot remove the last active local administrator")
	ErrLastLoginMethod              = gerror.New("cannot remove the last login method")
	ErrProviderIdentityLocked       = gerror.Wrap(ErrConflict, "bound provider identity authority cannot change")
	ErrProviderUnavailable          = gerror.New("identity provider unavailable or changed")
)
