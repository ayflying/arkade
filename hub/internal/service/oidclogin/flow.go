package oidclogin

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"strings"

	"arkade/hub/internal/user"
	oidc "github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
)

// StartBind requires a fresh local-password check. OIDC-only accounts cannot use
// this endpoint: an existing-provider reauthentication protocol is not implied.
func (s *Service) StartBind(ctx context.Context, providerID, actorID, sessionToken, password, redirect string) (StartResult, error) {
	if actorID == "" || sessionToken == "" {
		return StartResult{}, ErrIdentity
	}
	if err := s.d.Auth.VerifyLocalPassword(ctx, actorID, password); err != nil {
		return StartResult{}, ErrIdentity
	}
	return s.start(ctx, providerID, actorID, sessionToken, redirect)
}
func (s *Service) Start(ctx context.Context, providerID, redirect string) (StartResult, error) {
	return s.start(ctx, providerID, "", "", redirect)
}
func (s *Service) start(ctx context.Context, id, actor, session, redirect string) (StartResult, error) {
	local, err := LocalRedirect(redirect)
	if err != nil {
		return StartResult{}, err
	}
	c, _, cfg, err := s.provider(ctx, id)
	if err != nil {
		return StartResult{}, err
	}
	state, err := randomToken()
	if err != nil {
		return StartResult{}, ErrFlow
	}
	browser, err := randomToken()
	if err != nil {
		return StartResult{}, ErrFlow
	}
	nonce, err := randomToken()
	if err != nil {
		return StartResult{}, ErrFlow
	}
	verifier, err := randomToken()
	if err != nil {
		return StartResult{}, ErrFlow
	}
	expiry := s.now().Add(s.d.TTL)
	f := flow{ProviderID: id, Version: c.Provider.Version, BrowserHash: sha256.Sum256([]byte(browser)), ActorID: actor, SessionHash: sessionDigest(session), Verifier: verifier, Nonce: nonce, Redirect: local, Expires: expiry}
	s.mu.Lock()
	for key, prior := range s.flows {
		if !s.now().Before(prior.Expires) {
			delete(s.flows, key)
		}
	}
	if len(s.flows) >= s.d.MaxFlows {
		s.mu.Unlock()
		return StartResult{}, ErrFlow
	}
	s.flows[state] = f
	s.mu.Unlock()
	return StartResult{URL: cfg.AuthCodeURL(state, oidc.Nonce(nonce), oauth2.S256ChallengeOption(verifier)), BrowserToken: browser, State: state, Expires: expiry}, nil
}
func sessionDigest(raw string) string {
	if raw == "" {
		return ""
	}
	b := sha256.Sum256([]byte(raw))
	return string(b[:])
}
func (s *Service) Complete(ctx context.Context, providerID, state, browser, code, actorID, sessionToken string) (result CompleteResult, resultErr error) {
	var auditedFlow flow
	defer func() {
		if s.d.Audit != nil {
			outcome := "success"
			if resultErr != nil {
				outcome = "failure"
			}
			action := "oidc.login"
			id := ""
			if auditedFlow.ActorID != "" {
				action = "oidc.bind"
				id = auditedFlow.ActorID
			}
			if result.Login != nil {
				id = result.Login.User.ID
			}
			s.d.Audit(ctx, AuditEvent{Action: action, ProviderID: providerID, UserID: id, Outcome: outcome})
		}
	}()
	// Consume before network operations: even failed exchanges cannot be replayed.
	s.mu.Lock()
	f, ok := s.flows[state]
	auditedFlow = f
	delete(s.flows, state)
	s.mu.Unlock()
	got := sha256.Sum256([]byte(browser))
	if !ok || state == "" || browser == "" || code == "" || len(code) > 8192 || f.ProviderID != providerID || !s.now().Before(f.Expires) || subtle.ConstantTimeCompare(got[:], f.BrowserHash[:]) != 1 {
		return CompleteResult{}, ErrFlow
	}
	if f.ActorID != "" && (actorID != f.ActorID || sessionDigest(sessionToken) != f.SessionHash) {
		return CompleteResult{}, ErrFlow
	}
	c, p, cfg, err := s.provider(ctx, providerID)
	if err != nil || c.Provider.Version != f.Version {
		return CompleteResult{}, ErrProvider
	}
	ctx = clientContext(ctx, s.d.HTTPClient)
	tok, err := cfg.Exchange(ctx, code, oauth2.VerifierOption(f.Verifier))
	if err != nil {
		return CompleteResult{}, ErrIdentity
	}
	raw, ok := tok.Extra("id_token").(string)
	if !ok {
		return CompleteResult{}, ErrIdentity
	}
	verified, err := p.Verifier(&oidc.Config{ClientID: c.Provider.ClientID}).Verify(ctx, raw)
	if err != nil || verified.Subject == "" || len(verified.Subject) > 1024 || subtle.ConstantTimeCompare([]byte(verified.Nonce), []byte(f.Nonce)) != 1 {
		return CompleteResult{}, ErrIdentity
	}
	identityID, err := randomToken()
	if err != nil {
		return CompleteResult{}, ErrIdentity
	}
	if f.ActorID != "" {
		err = s.bind(ctx, providerID, f.Version, identityID, f.ActorID, verified.Subject)
		if err != nil {
			return CompleteResult{}, ErrIdentity
		}
		return CompleteResult{Redirect: f.Redirect, Bound: true}, nil
	}
	userID, err := randomToken()
	if err != nil {
		return CompleteResult{}, ErrIdentity
	}
	// No email matching, no roles/groups/admin/username claims are trusted.
	localUser, err := s.d.DB.ResolveOIDCIdentity(ctx, providerID, f.Version, verified.Subject, user.User{ID: userID, Username: "oidc_" + strings.ToLower(userID[:20]), DisplayName: "OIDC 用户", Role: user.RoleUser, CreatedAt: s.now(), UpdatedAt: s.now()}, identityID)
	if err != nil {
		return CompleteResult{}, ErrIdentity
	}
	login, err := s.d.Auth.IssueOIDCSession(ctx, localUser.ID, providerID, f.Version, verified.Subject)
	if err != nil {
		return CompleteResult{}, ErrIdentity
	}
	return CompleteResult{Login: &login, Redirect: f.Redirect}, nil
}
