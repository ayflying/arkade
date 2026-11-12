package notify

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestWebhookValidation(t *testing.T) {
	for _, v := range []string{
		"http://oapi.dingtalk.com/robot/send?access_token=abc", "https://oapi.dingtalk.com.evil/robot/send?access_token=abc",
		"https://oapi.dingtalk.com:443/robot/send?access_token=abc", "https://x@oapi.dingtalk.com/robot/send?access_token=abc",
		"https://oapi.dingtalk.com/robot/%73end?access_token=abc", "https://oapi.dingtalk.com/robot/send?access_token=abc&sign=x",
		"https://oapi.dingtalk.com/robot/send?access_token=abc&access_token=def", "https://oapi.dingtalk.com/robot/send?access_token=abc#x",
	} {
		if ValidateChannel(ChannelConfig{Kind: "dingtalk", DingTalk: DingTalkConfig{Webhook: v}}) == nil {
			t.Errorf("accepted %s", v)
		}
	}
}

func TestDingTalkLocal(t *testing.T) {
	var response = `{"errcode":0}`
	var status = 200
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		q := r.URL.Query()
		mac := hmac.New(sha256.New, []byte("secret"))
		mac.Write([]byte(q.Get("timestamp") + "\nsecret"))
		if q.Get("sign") != base64.StdEncoding.EncodeToString(mac.Sum(nil)) {
			t.Error("invalid signature")
		}
		body, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(body), `"title":"标题"`) {
			t.Errorf("payload: %s", body)
		}
		w.Header().Set("Location", serverURLPlaceholder)
		w.WriteHeader(status)
		fmt.Fprint(w, response)
	}))
	defer server.Close()
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		clone := r.Clone(r.Context())
		u := *r.URL
		clone.URL = &u
		clone.URL.Scheme = "http"
		clone.URL.Host = strings.TrimPrefix(server.URL, "http://")
		return http.DefaultTransport.RoundTrip(clone)
	})
	s := &DefaultSender{httpClient: &http.Client{Transport: transport}}
	cfg := ChannelConfig{Kind: "dingtalk", DingTalk: DingTalkConfig{Webhook: "https://oapi.dingtalk.com/robot/send?access_token=private-token", Secret: "secret"}}
	if err := s.Send(context.Background(), cfg, Message{Title: "标题", Body: "正文"}); err != nil {
		t.Fatal(err)
	}
	for _, v := range []struct {
		status int
		body   string
	}{{200, `{"errcode":310000,"errmsg":"private-token secret"}`}, {200, `{}`}, {200, strings.Repeat("x", maxResponse+1)}, {302, `secret`}, {500, `private-token`}} {
		status = v.status
		response = v.body
		err := s.Send(context.Background(), cfg, Message{Title: "标题"})
		if err == nil {
			t.Fatal("expected error")
		}
		if strings.Contains(err.Error(), "private-token") || strings.Contains(err.Error(), "secret") {
			t.Fatal("secret leaked")
		}
	}
	if calls != 6 {
		t.Fatalf("unexpected redirect: %d calls", calls)
	}
}

const serverURLPlaceholder = "https://oapi.dingtalk.com/robot/send?access_token=redirect"

func TestDingTalkCancellation(t *testing.T) {
	s := &DefaultSender{httpClient: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) { <-r.Context().Done(); return nil, r.Context().Err() })}}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	err := s.Send(ctx, ChannelConfig{Kind: "dingtalk", DingTalk: DingTalkConfig{Webhook: "https://oapi.dingtalk.com/robot/send?access_token=token"}}, Message{})
	if err == nil {
		t.Fatal("expected cancellation")
	}
}
