package auth

import (
	"arkade/hub/internal/store"
	"arkade/hub/internal/testdb"
	"arkade/hub/internal/user"
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestLoginLimiterConcurrentAccountAndIP(t *testing.T) {
	var l loginLimiter
	var allowed atomic.Int32
	var wg sync.WaitGroup
	p := Policy{LoginMaxAttempts: 5, LoginWindow: time.Minute}
	now := time.Now()
	for i := 0; i < 40; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if l.reserve(now, "account", "127.0.0.1", p) {
				allowed.Add(1)
			}
		}()
	}
	wg.Wait()
	if allowed.Load() != 5 {
		t.Fatal(allowed.Load())
	}
	if l.reserve(now, "other", "127.0.0.1", p) {
		t.Fatal("IP rotation bypass")
	}
	if !l.reserve(now.Add(2*time.Minute), "account", "127.0.0.1", p) {
		t.Fatal("expired limit not released")
	}
}
func authFixture(t *testing.T) (*Service, *store.DB, user.User) {
	t.Helper()
	db, err := store.Open(context.Background(), testdb.New(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	hash, err := user.HashPassword("Password12345!")
	if err != nil {
		t.Fatal(err)
	}
	u := user.User{ID: "u", Username: "local", DisplayName: "Local", Role: user.RoleAdmin, PasswordHash: hash, CreatedAt: time.Now(), UpdatedAt: time.Now()}
	if err := db.CreateUser(context.Background(), u); err != nil {
		t.Fatal(err)
	}
	return NewService(db, time.Hour), db, u
}
func TestDynamicTTLAndOIDCOnlyPasswordDenial(t *testing.T) {
	s, db, _ := authFixture(t)
	s.SetPolicy(func(context.Context) (Policy, error) {
		return Policy{SessionTTL: 2 * time.Hour, LoginMaxAttempts: 5, LoginWindow: time.Minute}, nil
	})
	result, err := s.IssueSession(context.Background(), "u")
	if err != nil {
		t.Fatal(err)
	}
	if d := time.Until(result.ExpiresAt); d < 119*time.Minute || d > 121*time.Minute {
		t.Fatal(d)
	}
	u := user.User{ID: "oidc", Username: "oidc", DisplayName: "OIDC", Role: user.RoleUser}
	if err := db.CreateUser(context.Background(), u); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Login(context.Background(), "oidc", ""); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatal(err)
	}
	if err := s.VerifyLocalPassword(context.Background(), "oidc", ""); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatal(err)
	}
	if _, err := s.IssueSession(context.Background(), "oidc"); err != nil {
		t.Fatal(err)
	}
}
func TestSuccessfulLoginsDoNotExhaustFailureBudget(t *testing.T) {
	s, _, _ := authFixture(t)
	s.SetPolicy(func(context.Context) (Policy, error) {
		return Policy{LoginMaxAttempts: 2, LoginWindow: time.Minute}, nil
	})
	for i := 0; i < 5; i++ {
		if _, err := s.LoginWithClient(context.Background(), "local", "Password12345!", "ip"); err != nil {
			t.Fatal("normal login consumed budget", err)
		}
	}
	for i := 0; i < 2; i++ {
		if err := s.VerifyLocalPassword(context.Background(), "u", "wrong"); !errors.Is(err, ErrInvalidCredentials) {
			t.Fatal(err)
		}
	}
	if err := s.VerifyLocalPassword(context.Background(), "u", "Password12345!"); !errors.Is(err, ErrRateLimited) {
		t.Fatal("reauth limiter bypass", err)
	}
}

func TestLoginRateLimitAndPolicyFailureClosed(t *testing.T) {
	s, _, _ := authFixture(t)
	s.SetPolicy(func(context.Context) (Policy, error) {
		return Policy{LoginMaxAttempts: 2, LoginWindow: time.Minute}, nil
	})
	for i := 0; i < 2; i++ {
		if _, err := s.LoginWithClient(context.Background(), " LOCAL ", "wrong", "ip"); !errors.Is(err, ErrInvalidCredentials) {
			t.Fatal(err)
		}
	}
	if _, err := s.Login(context.Background(), "local", "Password12345!"); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("old Login bypassed limit: %v", err)
	}
	s.SetPolicy(func(context.Context) (Policy, error) { return Policy{}, errors.New("policy unavailable") })
	if _, err := s.IssueSession(context.Background(), "u"); err == nil {
		t.Fatal("policy read failure permitted session")
	}
}
