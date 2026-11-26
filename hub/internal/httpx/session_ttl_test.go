package httpx

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"testing"

	"arkade/hub/internal/store"
)

func TestSessionTTLUsesLiveDatabaseSettingsForPasswordAndOIDC(t *testing.T) {
	const (
		defaultTTL = 30 * 24 * 60 * 60
		customTTL  = 7 * 24 * 60 * 60
	)
	e := newOIDCTestEnv(t) // 与生产一致地装配 DB 策略，配置回退值只有 1 小时。
	oldLocal := e.adminCookie
	assertSessionCookieTTL(t, oldLocal, defaultTTL)
	providerID := e.createProvider(t, true)
	oldOIDC := e.runOIDCLogin(t, providerID).session
	assertSessionCookieTTL(t, oldOIDC, defaultTTL)

	getBody := e.jsonBody(t, e.get(t, "/api/settings", []*http.Cookie{oldLocal}))
	settings, ok := getBody["settings"].(map[string]any)
	if !ok || settings["session_ttl_seconds"] != float64(defaultTTL) {
		t.Fatalf("unexpected default session setting: %v", getBody)
	}
	settings["session_ttl_seconds"] = customTTL
	resp := e.do(t, http.MethodPut, "/api/settings", mustJSON(t, settings), []*http.Cookie{oldLocal}, nil)
	if body := e.body(resp); resp.StatusCode != http.StatusOK {
		t.Fatalf("update session TTL failed: %d %s", resp.StatusCode, body)
	}

	// 不重启服务；同一服务进程中两条登录路径都应立即使用新值。
	e.loginAdmin(t)
	assertSessionCookieTTL(t, e.adminCookie, customTTL)
	newOIDC := e.runOIDCLogin(t, providerID).session
	assertSessionCookieTTL(t, newOIDC, customTTL)

	for _, cookie := range []*http.Cookie{oldLocal, oldOIDC, e.adminCookie, newOIDC} {
		// 以 Cookie 的绝对过期时间为基准，验证 DB 边界一致。旧 Cookie 在策略变更后
		// 仍保留签发时的到期时间。这些调用传入模拟时钟，无需等待。
		sum := sha256.Sum256([]byte(cookie.Value))
		tokenHash := hex.EncodeToString(sum[:])
		expiresAt := cookie.Expires.Unix()
		if _, err := e.db.SessionUserID(context.Background(), tokenHash, expiresAt-1); err != nil {
			t.Fatalf("session expired before cookie expiry: %v", err)
		}
		if _, err := e.db.SessionUserID(context.Background(), tokenHash, expiresAt); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("session not expired at cookie expiry: %v", err)
		}
	}
}

func assertSessionCookieTTL(t *testing.T, cookie *http.Cookie, wantSeconds int) {
	t.Helper()
	if cookie == nil || cookie.Expires.IsZero() {
		t.Fatal("missing session cookie or expiry")
	}
	// MaxAge 基于写 Cookie 时剩余时长，向下截断到整秒。
	if cookie.MaxAge < wantSeconds-2 || cookie.MaxAge > wantSeconds {
		t.Fatalf("session cookie max-age = %d; want about %d", cookie.MaxAge, wantSeconds)
	}
}
