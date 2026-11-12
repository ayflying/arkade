package notify

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"net"
	"net/http/httptest"
	"net/mail"
	"net/textproto"
	"strconv"
	"strings"
	"testing"
	"time"
)

// 复用 httptest 的本地证书，但 SMTP 会话只走本地 TCP。
func fakeSMTP(t *testing.T, mode string, auth bool) (EmailConfig, *DefaultSender, <-chan string) {
	t.Helper()
	certServer := httptest.NewTLSServer(nil)
	cert := certServer.TLS.Certificates[0]
	roots := x509.NewCertPool()
	roots.AddCert(certServer.Certificate())
	certServer.Close()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close() })
	result := make(chan string, 1)
	go func() {
		raw, err := listener.Accept()
		if err != nil {
			return
		}
		defer raw.Close()
		raw.SetDeadline(time.Now().Add(3 * time.Second))
		conn := net.Conn(raw)
		secure := false
		tlsConfig := &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}
		if mode == "tls" {
			conn = tls.Server(raw, tlsConfig)
			secure = true
		}
		r := bufio.NewReader(conn)
		fmt.Fprint(conn, "220 local ESMTP\r\n")
		for {
			line, err := r.ReadString('\n')
			if err != nil {
				return
			}
			switch {
			case strings.HasPrefix(line, "EHLO"):
				if !secure && mode == "starttls" {
					fmt.Fprint(conn, "250-local\r\n250 STARTTLS\r\n")
				} else {
					fmt.Fprint(conn, "250-local\r\n250 AUTH PLAIN\r\n")
				}
			case strings.HasPrefix(line, "STARTTLS"):
				fmt.Fprint(conn, "220 ready\r\n")
				conn = tls.Server(raw, tlsConfig)
				r = bufio.NewReader(conn)
				secure = true
			case strings.HasPrefix(line, "AUTH"):
				if !secure {
					result <- "AUTH before TLS"
					return
				}
				fmt.Fprint(conn, "235 authenticated\r\n")
			case strings.HasPrefix(line, "MAIL"), strings.HasPrefix(line, "RCPT"):
				if !secure {
					result <- "mail before TLS"
					return
				}
				fmt.Fprint(conn, "250 ok\r\n")
			case strings.HasPrefix(line, "DATA"):
				fmt.Fprint(conn, "354 send\r\n")
				data, err := textproto.NewReader(r).ReadDotBytes()
				if err != nil {
					return
				}
				result <- string(data)
				fmt.Fprint(conn, "250 queued\r\n")
			case strings.HasPrefix(line, "QUIT"):
				fmt.Fprint(conn, "221 bye\r\n")
				return
			default:
				fmt.Fprint(conn, "500 unsupported\r\n")
			}
		}
	}()
	host, port, _ := net.SplitHostPort(listener.Addr().String())
	p, _ := strconv.Atoi(port)
	cfg := EmailConfig{Host: host, Port: p, TLSMode: mode, From: "发送者 <from@example.com>", To: []string{"收件人 <to@example.com>"}}
	if auth {
		cfg.Username = "user"
		cfg.Password = "private-password"
	}
	return cfg, &DefaultSender{tlsConfig: &tls.Config{RootCAs: roots}}, result
}

func TestEmailTLS(t *testing.T) {
	for _, mode := range []string{"tls", "starttls"} {
		for _, auth := range []bool{false, true} {
			t.Run(fmt.Sprint(mode, auth), func(t *testing.T) {
				cfg, s, result := fakeSMTP(t, mode, auth)
				err := s.Send(context.Background(), ChannelConfig{Kind: "email", Email: cfg}, Message{Title: "测试主题", Body: "第一行\n.第二行"})
				if err != nil {
					t.Fatal(err)
				}
				data := <-result
				m, err := mail.ReadMessage(strings.NewReader(data))
				if err != nil {
					t.Fatal(err)
				}
				if !strings.HasPrefix(m.Header.Get("Subject"), "=?UTF-8?q?") {
					t.Fatalf("subject not encoded: %s", data)
				}
				if strings.Contains(data, "private-password") {
					t.Fatal("password in body")
				}
			})
		}
	}
}

func TestEmailRejectsInvalid(t *testing.T) {
	base := EmailConfig{Host: "localhost", Port: 465, TLSMode: "tls", From: "from@example.com", To: []string{"to@example.com"}}
	tests := []EmailConfig{base, base, base, base, base, base}
	tests[0].TLSMode = "plain"
	tests[1].From = "a@example.com\r\nBcc: victim@example.com"
	tests[2].To = []string{"a@example.com\nb"}
	tests[3].To = nil
	tests[4].Password = "secret"
	tests[5].Host = "localhost\r\n"
	for _, cfg := range tests {
		if err := ValidateChannel(ChannelConfig{Kind: "email", Email: cfg}); err == nil {
			t.Fatalf("accepted invalid config: %+v", cfg)
		}
	}
}

func TestEmailNoDowngradeAndCancellation(t *testing.T) {
	for _, cancelled := range []bool{false, true} {
		t.Run(fmt.Sprint(cancelled), func(t *testing.T) {
			l, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer l.Close()
			done := make(chan string, 1)
			go func() {
				c, err := l.Accept()
				if err != nil {
					return
				}
				defer c.Close()
				c.SetDeadline(time.Now().Add(time.Second))
				if !cancelled {
					fmt.Fprint(c, "220 local\r\n")
					bufio.NewReader(c).ReadString('\n')
					fmt.Fprint(c, "250 no TLS\r\n")
				}
				b, _ := io.ReadAll(c)
				done <- string(b)
			}()
			host, port, _ := net.SplitHostPort(l.Addr().String())
			p, _ := strconv.Atoi(port)
			ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
			defer cancel()
			cfg := ChannelConfig{Kind: "email", Email: EmailConfig{Host: host, Port: p, TLSMode: "starttls", From: "a@example.com", To: []string{"b@example.com"}}}
			if err := New().Send(ctx, cfg, Message{}); err == nil {
				t.Fatal("expected failure")
			}
			if extra := <-done; extra != "" {
				t.Fatalf("continued plaintext SMTP: %q", extra)
			}
		})
	}
}

func TestEmailCertificateVerification(t *testing.T) {
	cfg, _, _ := fakeSMTP(t, "tls", false)
	if err := New().Send(context.Background(), ChannelConfig{Kind: "email", Email: cfg}, Message{}); err == nil {
		t.Fatal("untrusted cert accepted")
	}
}
