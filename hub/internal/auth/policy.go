package auth

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"sync"
	"time"
)

// Policy is a dependency-neutral projection of dynamic system settings.
// A failed policy read fails closed; it never silently restores weaker limits.
type Policy struct {
	SessionTTL       time.Duration
	LoginMaxAttempts int
	LoginWindow      time.Duration
}
type PolicyReader func(context.Context) (Policy, error)

func (s *Service) SetPolicy(reader PolicyReader) {
	s.policyMu.Lock()
	s.policy = reader
	s.policyMu.Unlock()
}
func (s *Service) currentPolicy(ctx context.Context) (Policy, error) {
	s.policyMu.RLock()
	reader := s.policy
	s.policyMu.RUnlock()
	if reader != nil {
		return reader(ctx)
	}
	return Policy{SessionTTL: s.sessionTTL, LoginMaxAttempts: 5, LoginWindow: 15 * time.Minute}, nil
}

func limitKey(prefix, value string) string {
	sum := sha256.Sum256([]byte(value))
	return prefix + hex.EncodeToString(sum[:])
}

// successful authentication releases its reservations; failed attempts remain
// until the fixed window expires. Concurrent in-flight calls still hold slots.
func (l *loginLimiter) success(username, ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	keys := []string{limitKey("user:", username)}
	if ip != "" {
		keys = append(keys, limitKey("ip:", ip))
	}
	for _, k := range keys {
		v := l.entries[k]
		if v.count > 1 {
			v.count--
			l.entries[k] = v
		} else {
			delete(l.entries, k)
		}
	}
}

type attempt struct {
	count   int
	expires time.Time
}
type loginLimiter struct {
	mu      sync.Mutex
	entries map[string]attempt
}

// reserve counts requests before password work, so concurrent brute force cannot
// pass a check-then-increment gap. Limits apply independently to account and IP;
// no untrusted forwarding headers are parsed at this security boundary.
func (l *loginLimiter) reserve(now time.Time, username, ip string, p Policy) bool {
	max := p.LoginMaxAttempts
	if max < 1 {
		max = 5
	}
	window := p.LoginWindow
	if window <= 0 {
		window = 15 * time.Minute
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.entries == nil {
		l.entries = map[string]attempt{}
	}
	for k, v := range l.entries {
		if !now.Before(v.expires) {
			delete(l.entries, k)
		}
	}
	keys := []string{limitKey("user:", username)}
	if ip != "" {
		keys = append(keys, limitKey("ip:", ip))
	}
	for _, k := range keys {
		if l.entries[k].count >= max {
			return false
		}
	}
	if len(l.entries)+len(keys) > 10000 {
		return false
	}
	for _, k := range keys {
		v := l.entries[k]
		if v.count == 0 {
			v.expires = now.Add(window)
		}
		v.count++
		l.entries[k] = v
	}
	return true
}
