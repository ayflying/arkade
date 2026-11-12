package auth

import (
	"arkade/hub/internal/store"
	"context"
	"errors"
	"time"
)

// IssueOIDCSession fences provider disable/version, identity unlink and user
// disable in the same transaction that creates the session.
func (s *Service) IssueOIDCSession(ctx context.Context, userID, providerID string, version int64, subject string) (LoginResult, error) {
	u, err := s.db.GetUserByID(ctx, userID)
	if err != nil {
		return LoginResult{}, err
	}
	if u.Disabled {
		return LoginResult{}, ErrInvalidCredentials
	}
	p, err := s.currentPolicy(ctx)
	if err != nil {
		return LoginResult{}, err
	}
	ttl := p.SessionTTL
	if ttl <= 0 {
		ttl = s.sessionTTL
	}
	token, err := newToken()
	if err != nil {
		return LoginResult{}, err
	}
	now := time.Now().UTC()
	expires := now.Add(ttl)
	if err := s.db.CreateOIDCSession(ctx, hashToken(token), userID, now.Unix(), expires.Unix(), providerID, version, subject); err != nil {
		if errors.Is(err, store.ErrNotFound) || errors.Is(err, store.ErrConflict) {
			return LoginResult{}, ErrInvalidCredentials
		}
		return LoginResult{}, err
	}
	return LoginResult{User: u, Token: token, ExpiresAt: expires}, nil
}
