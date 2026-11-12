package monitor

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	"arkade/hub/internal/notify"
	"arkade/hub/internal/store"
	"arkade/hub/internal/testdb"
)

func securityService(t *testing.T, sender notify.Sender) (*Service, *store.DB, []byte) {
	t.Helper()
	db, err := store.Open(context.Background(), testdb.New(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	key := []byte(strings.Repeat("s", 32))
	s, err := NewService(db, key, sender)
	if err != nil {
		t.Fatal(err)
	}
	return s, db, key
}

func securityEmailUpdate(version int64) Update {
	return Update{Version: version, Config: DefaultConfig(), Channels: ChannelsUpdate{Email: EmailUpdate{
		Host: "smtp.example.com", Port: 587, TLSMode: "starttls", Username: "monitor", Password: "email-password-private-123",
		From: "from@example.com", To: []string{"to@example.com"},
	}}}
}

func TestServiceEmailSecretsEncryptedPreservedAndCleared(t *testing.T) {
	ctx := context.Background()
	s, db, _ := securityService(t, &testSender{})
	input := securityEmailUpdate(1)
	saved, err := s.Update(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range []ConfigView{saved, mustSecurityView(t, s)} {
		raw, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(raw), input.Channels.Email.Password) || !value.Channels.Email.HasPassword {
			t.Fatal("邮件密码读取脱敏或存在标记错误")
		}
	}
	row, err := db.GetMonitorConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(row.ConfigJSON, input.Channels.Email.Password) || strings.Contains(row.SecretsCiphertext, input.Channels.Email.Password) || !strings.HasPrefix(row.SecretsCiphertext, "v1:") {
		t.Fatal("邮件密码未独立加密存储")
	}
	_, _, secret, err := s.load(ctx)
	if err != nil || secret.EmailPassword != input.Channels.Email.Password {
		t.Fatalf("密码解密错误: %v", err)
	}
	input.Version = saved.Version
	input.Channels.Email.Password = ""
	retained, err := s.Update(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	_, _, secret, err = s.load(ctx)
	if err != nil || secret.EmailPassword != "email-password-private-123" || !retained.Channels.Email.HasPassword {
		t.Fatalf("空密码必须保留: %v", err)
	}
	input.Version = retained.Version
	input.Channels.Email.ClearPassword = true
	// 清除密码后同时清除账号，成为合法的无认证TLS relay。
	input.Channels.Email.Username = ""
	cleared, err := s.Update(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	_, _, secret, err = s.load(ctx)
	if err != nil || secret.EmailPassword != "" || cleared.Channels.Email.HasPassword {
		t.Fatalf("显式清除密码失败: %v", err)
	}
}

func mustSecurityView(t *testing.T, s *Service) ConfigView {
	t.Helper()
	v, err := s.Get(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestServiceEmailTargetChangeRequiresPassword(t *testing.T) {
	for _, field := range []string{"host", "port", "tls_mode", "username"} {
		t.Run(field, func(t *testing.T) {
			ctx := context.Background()
			s, _, _ := securityService(t, &testSender{})
			input := securityEmailUpdate(1)
			saved, err := s.Update(ctx, input)
			if err != nil {
				t.Fatal(err)
			}
			input.Version = saved.Version
			input.Channels.Email.Password = ""
			switch field {
			case "host":
				input.Channels.Email.Host = "replacement.example.com"
			case "port":
				input.Channels.Email.Port = 465
			case "tls_mode":
				input.Channels.Email.TLSMode = "tls"
			case "username":
				input.Channels.Email.Username = "replacement-user"
			}
			if _, err = s.Update(ctx, input); err == nil {
				t.Fatal("更换SMTP认证目标不应静默复用旧密码")
			}
			row, data, secret, err := s.load(ctx)
			if err != nil || row.Version != saved.Version || data.Email.Host != "smtp.example.com" || data.Email.Port != 587 || data.Email.TLSMode != "starttls" || data.Email.Username != "monitor" || secret.EmailPassword != "email-password-private-123" {
				t.Fatalf("拒绝后配置应原样保留: %v", err)
			}
			input.Channels.Email.Password = "replacement-password-private"
			updated, err := s.Update(ctx, input)
			if err != nil {
				t.Fatal(err)
			}
			_, data, secret, err = s.load(ctx)
			if err != nil || updated.Version != saved.Version+1 || secret.EmailPassword != input.Channels.Email.Password || data.Email.Host != input.Channels.Email.Host || data.Email.Port != input.Channels.Email.Port || data.Email.TLSMode != input.Channels.Email.TLSMode || data.Email.Username != input.Channels.Email.Username {
				t.Fatalf("携带新密码应允许更新目标: %v", err)
			}
		})
	}
}

func TestServiceIndependentInstancesConcurrentCAS(t *testing.T) {
	s, db, key := securityService(t, &testSender{})
	other, err := NewService(db, key, &testSender{})
	if err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	results := make(chan error, 2)
	var wg sync.WaitGroup
	for _, instance := range []*Service{s, other} {
		wg.Add(1)
		go func(instance *Service) {
			defer wg.Done()
			<-start
			_, err := instance.Update(context.Background(), securityEmailUpdate(1))
			results <- err
		}(instance)
	}
	close(start)
	wg.Wait()
	close(results)
	wins, conflicts := 0, 0
	for err := range results {
		if err == nil {
			wins++
		} else if errors.Is(err, store.ErrConflict) {
			conflicts++
		} else {
			t.Fatalf("非预期CAS错误: %v", err)
		}
	}
	if wins != 1 || conflicts != 1 {
		t.Fatalf("wins=%d conflicts=%d", wins, conflicts)
	}
	view := mustSecurityView(t, s)
	if view.Version != 2 {
		t.Fatalf("version=%d", view.Version)
	}
}

type cancellingSecuritySender struct {
	cancel context.CancelFunc
	result error
	calls  int
}

func (s *cancellingSecuritySender) Send(ctx context.Context, _ notify.ChannelConfig, _ notify.Message) error {
	s.calls++
	if ctx.Err() != nil {
		return errors.New("测试开始前上下文不应取消")
	}
	s.cancel()
	return s.result
}

func TestServiceCancelledTestStillRecordsDelivery(t *testing.T) {
	for _, failed := range []bool{false, true} {
		name := "success"
		if failed {
			name = "failure"
		}
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			sender := &cancellingSecuritySender{cancel: cancel}
			if failed {
				sender.result = errors.New("private transport diagnostic")
			}
			s, db, _ := securityService(t, sender)
			if _, err := s.Update(ctx, securityEmailUpdate(1)); err != nil {
				t.Fatal(err)
			}
			err := s.Test(ctx, "email")
			if failed && err == nil {
				t.Fatal("发送失败应返回错误")
			}
			if !failed && err != nil {
				t.Fatalf("外部发送成功且记录成功不应返回取消: %v", err)
			}
			if sender.calls != 1 || ctx.Err() == nil {
				t.Fatal("fake未在发送期间取消上下文")
			}
			deliveries, err := db.ListMonitorDeliveries(context.Background(), 10)
			if err != nil || len(deliveries) != 1 {
				t.Fatalf("取消后仍须记录投递: count=%d err=%v", len(deliveries), err)
			}
			delivery := deliveries[0]
			want := "sent"
			if failed {
				want = "failed"
			}
			if delivery.Event != "test" || delivery.Channel != "email" || delivery.Status != want || strings.Contains(delivery.Error, "private transport diagnostic") {
				t.Fatalf("投递记录错误或泄漏原始错误: %+v", delivery)
			}
		})
	}
}
