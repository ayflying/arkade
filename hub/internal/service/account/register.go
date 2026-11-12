package account

import (
	"arkade/hub/internal/store"
	"arkade/hub/internal/user"
	"context"
	"crypto/rand"
	"encoding/hex"
	"time"
)

func newID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("生成随机 ID 失败: " + err.Error())
	}
	return "u-" + hex.EncodeToString(b[:])
}
func (s *Service) Register(ctx context.Context, username, displayName, password string) (user.User, error) {
	// Gate before expensive bcrypt and validation, then enforce it again inside
	// the storage transaction: settings flips and group deletes cannot bypass it.
	settings, err := s.db.GetSystemSettings(ctx)
	if err != nil {
		return user.User{}, err
	}
	if !settings.RegistrationEnabled {
		return user.User{}, store.ErrRegistrationDisabled
	}
	username = user.NormalizeUsername(username)
	if err := user.ValidateUsername(username); err != nil {
		return user.User{}, invalid(err)
	}
	if displayName == "" {
		displayName = username
	}
	if err := user.ValidateDisplayName(displayName); err != nil {
		return user.User{}, invalid(err)
	}
	if err := PasswordPolicy(password, settings.PasswordMinLength); err != nil {
		return user.User{}, invalid(err)
	}
	hash, err := user.HashPassword(password)
	if err != nil {
		return user.User{}, err
	}
	now := time.Now().UTC()
	u := user.User{ID: newID(), Username: username, DisplayName: displayName, PasswordHash: hash, Role: user.RoleUser, CreatedAt: now, UpdatedAt: now}
	if err := s.db.RegisterLocalUser(ctx, u, settings.Version); err != nil {
		return user.User{}, err
	}
	return u, nil
}
