package link

import (
	"net/url"
	"testing"
)

func TestDialAuthenticationNeverUsesURLToken(t *testing.T) {
	d, err := newDialer("ws://hub/agent/connect?token=old-secret&trace=1", "new-secret", "agent", "test", 1, false)
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(d.connectURL())
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := u.Query()["token"]; exists {
		t.Fatal("authentication token leaked into URL")
	}
	if u.Query().Get("trace") != "1" {
		t.Fatal("unrelated query lost")
	}
	if d.headers().Get(headerAuth) != "Bearer new-secret" {
		t.Fatal("missing Bearer header")
	}
}
