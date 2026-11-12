package account

import (
	"arkade/hub/internal/auth"
	"arkade/hub/internal/store"
	"arkade/hub/internal/user"
	"context"
	"errors"
	"fmt"
	"github.com/gogf/gf/v2/errors/gerror"
	"time"
	"unicode/utf8"
)

var (
	ErrInvalidPassword = gerror.New("原密码不正确")
	ErrNoLocalPassword = gerror.New("该账户没有可修改的本地密码")
	ErrSamePassword    = gerror.New("新密码不能与原密码相同")
)

type Service struct {
	db       *store.DB
	verifier *auth.Service
}

func New(db *store.DB) *Service {
	s := &Service{db: db, verifier: auth.NewService(db, 0)}
	s.verifier.SetPolicy(s.AuthPolicy)
	return s
}
func (s *Service) Get(ctx context.Context, userID string) (user.User, error) {
	return s.db.GetUserByID(ctx, userID)
}
func (s *Service) Update(ctx context.Context, userID, displayName string) (user.User, error) {
	if err := user.ValidateDisplayName(displayName); err != nil {
		return user.User{}, err
	}
	if err := s.db.UpdateUser(ctx, userID, store.UserUpdate{DisplayName: &displayName}, time.Now().UTC().Unix()); err != nil {
		return user.User{}, err
	}
	return s.db.GetUserByID(ctx, userID)
}

// PasswordPolicy layers the configurable minimum over the fixed bcrypt/safety
// policy. The upper bound remains enforced by user.ValidatePassword.
func PasswordPolicy(password string, min int64) error {
	if err := user.ValidatePassword(password); err != nil {
		return err
	}
	if int64(utf8.RuneCountInString(password)) < min {
		return gerror.Newf("密码至少需要 %d 个字符", min)
	}
	return nil
}
func (s *Service) ChangePassword(ctx context.Context, userID, oldPassword, newPassword string) error {
	u, err := s.db.GetUserByID(ctx, userID)
	if err != nil {
		return err
	}
	if u.Disabled {
		return auth.ErrSessionExpired
	}
	if u.PasswordHash == "" {
		return ErrNoLocalPassword
	}
	if err := s.verifier.VerifyLocalPassword(ctx, userID, oldPassword); err != nil {
		if errors.Is(err, auth.ErrInvalidCredentials) {
			return ErrInvalidPassword
		}
		return err
	}
	settings, err := s.db.GetSystemSettings(ctx)
	if err != nil {
		return err
	}
	if err := PasswordPolicy(newPassword, settings.PasswordMinLength); err != nil {
		return invalid(err)
	}
	if user.VerifyPassword(u.PasswordHash, newPassword) == nil {
		return ErrSamePassword
	}
	hash, err := user.HashPassword(newPassword)
	if err != nil {
		return err
	}
	return s.db.ChangeLocalPassword(ctx, userID, u.PasswordHash, hash, time.Now().UTC().Unix(), false)
}

// AuthPolicy is a lightweight callback; auth never imports account/settings.
func (s *Service) AuthPolicy(ctx context.Context) (auth.Policy, error) {
	settings, err := s.db.GetSystemSettings(ctx)
	if err != nil {
		return auth.Policy{}, err
	}
	if settings.SessionTTLSeconds <= 0 || settings.LoginMaxAttempts <= 0 || settings.LoginWindowSeconds <= 0 {
		return auth.Policy{}, fmt.Errorf("无效的认证策略")
	}
	return auth.Policy{SessionTTL: time.Duration(settings.SessionTTLSeconds) * time.Second, LoginMaxAttempts: int(settings.LoginMaxAttempts), LoginWindow: time.Duration(settings.LoginWindowSeconds) * time.Second}, nil
}
