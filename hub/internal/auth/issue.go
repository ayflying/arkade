package auth

import (
	"arkade/hub/internal/store"
	"arkade/hub/internal/user"
	"context"
	"errors"
	"time"
)

// IssueSession is the sole OIDC session issuance path; storage rechecks disabled
// inside the session insert transaction to close the lookup/disable race.
func (s *Service) IssueSession(ctx context.Context, userID string) (LoginResult, error) {
	u, err := s.db.GetUserByID(ctx, userID)
	if errors.Is(err, store.ErrNotFound) || err == nil && u.Disabled {
		return LoginResult{}, ErrInvalidCredentials
	}
	if err != nil {
		return LoginResult{}, err
	}
	p, err := s.currentPolicy(ctx)
	if err != nil {
		return LoginResult{}, err
	}
	return s.issue(ctx, u, nil, p)
}
func (s *Service) issue(ctx context.Context, u user.User, expectedHash *string, p Policy) (LoginResult, error) {
	token, err := newToken()
	if err != nil {
		return LoginResult{}, err
	}
	ttl := p.SessionTTL
	if ttl <= 0 {
		ttl = s.sessionTTL
	}
	now := time.Now().UTC()
	expires := now.Add(ttl)
	if err := s.db.CreateUserSession(ctx, hashToken(token), u.ID, now.Unix(), expires.Unix(), expectedHash); err != nil {
		if errors.Is(err, store.ErrNotFound) || errors.Is(err, store.ErrConflict) {
			return LoginResult{}, ErrInvalidCredentials
		}
		return LoginResult{}, err
	}
	return LoginResult{User: u, Token: token, ExpiresAt: expires}, nil
}
func (s *Service) VerifyLocalPassword(ctx context.Context, userID, password string) error {
	p, err := s.currentPolicy(ctx)
	if err != nil {
		return err
	}
	key := "reauth:" + userID
	if !s.limiter.reserve(time.Now(), key, "", p) {
		return ErrRateLimited
	}
	u, err := s.db.GetUserByID(ctx, userID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return ErrInvalidCredentials
		}
		return err
	}
	if u.Disabled || u.PasswordHash == "" {
		_ = user.VerifyPassword(dummyHash, password)
		return ErrInvalidCredentials
	}
	if user.VerifyPassword(u.PasswordHash, password) != nil {
		return ErrInvalidCredentials
	}
	s.limiter.success(key, "")
	return nil
}
