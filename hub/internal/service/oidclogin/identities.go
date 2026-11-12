package oidclogin

import (
	"arkade/hub/internal/store"
	"context"
)

func (s *Service) bind(ctx context.Context, provider string, version int64, id, actor, subject string) error {
	return s.d.DB.BindOIDCIdentity(ctx, provider, version, store.Identity{ID: id, UserID: actor, ProviderID: provider, Subject: subject, CreatedAt: s.now()})
}
func (s *Service) Identities(ctx context.Context, userID string) ([]store.Identity, error) {
	if userID == "" {
		return nil, ErrIdentity
	}
	return s.d.DB.ListUserIdentities(ctx, userID)
}
func (s *Service) Unlink(ctx context.Context, userID, identityID string) error {
	if userID == "" || identityID == "" {
		return ErrIdentity
	}
	return s.d.DB.DeleteIdentity(ctx, userID, identityID)
}
