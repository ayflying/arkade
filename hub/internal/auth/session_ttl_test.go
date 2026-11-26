package auth

import (
	"testing"
	"time"
)

func TestSessionTTLFallback(t *testing.T) {
	for _, tc := range []struct{ ttl, want time.Duration }{
		{0, 30 * 24 * time.Hour},
		{48 * time.Hour, 48 * time.Hour},
	} {
		if got := NewService(nil, tc.ttl).SessionTTL(); got != tc.want {
			t.Errorf("fallback for %v = %v, want %v", tc.ttl, got, tc.want)
		}
	}
}
