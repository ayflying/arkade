package notify

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestNetworkErrorsRedacted(t *testing.T) {
	s := &DefaultSender{httpClient: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("https://oapi.dingtalk.com/robot/send?access_token=do-not-print password")
	})}}
	err := s.Send(context.Background(), ChannelConfig{Kind: "dingtalk", DingTalk: DingTalkConfig{Webhook: "https://oapi.dingtalk.com/robot/send?access_token=do-not-print"}}, Message{})
	if err == nil || strings.Contains(fmt.Sprintf("%+v", err), "do-not-print") || strings.Contains(fmt.Sprintf("%+v", err), "password") {
		t.Fatalf("network error not redacted: %+v", err)
	}
}

func TestSMTPResponseBoundAndRedaction(t *testing.T) {
	for _, oversize := range []bool{false, true} {
		t.Run(fmt.Sprint(oversize), func(t *testing.T) {
			l, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer l.Close()
			done := make(chan struct{})
			go func() {
				defer close(done)
				c, err := l.Accept()
				if err != nil {
					return
				}
				defer c.Close()
				c.SetDeadline(time.Now().Add(time.Second))
				response := "554 private-password token Webhook\r\n"
				if oversize {
					response = "220 " + strings.Repeat("x", maxResponse+1) + "\r\n"
				}
				fmt.Fprint(c, response)
			}()
			host, port, _ := net.SplitHostPort(l.Addr().String())
			var p int
			fmt.Sscan(port, &p)
			cfg := ChannelConfig{Kind: "email", Email: EmailConfig{Host: host, Port: p, TLSMode: "starttls", From: "a@example.com", To: []string{"b@example.com"}, Username: "user", Password: "private-password"}}
			err = New().Send(context.Background(), cfg, Message{})
			if err == nil || strings.Contains(fmt.Sprintf("%+v", err), "private-password") {
				t.Fatalf("invalid error: %+v", err)
			}
			<-done
		})
	}
}

func TestEmailHeaderInjection(t *testing.T) {
	cfg := ChannelConfig{Kind: "email", Email: EmailConfig{Host: "localhost", Port: 465, TLSMode: "tls", From: "a@example.com", To: []string{"b@example.com"}}}
	for _, title := range []string{"title\r\nBcc: x@example.com", "title\nX-Test: injected", "title\x00"} {
		if err := New().Send(context.Background(), cfg, Message{Title: title}); err == nil || !strings.Contains(err.Error(), "主题") {
			t.Fatalf("header injection not rejected: %v", err)
		}
	}
}
