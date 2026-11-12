package monitor

import (
	"arkade/hub/internal/notify"
	"arkade/hub/internal/store"
	"arkade/hub/internal/testdb"
	"context"
	"encoding/json"
	"strings"
	"testing"
)

type testSender struct{ calls int }

func (s *testSender) Send(_ context.Context, _ notify.ChannelConfig, _ notify.Message) error {
	s.calls++
	return nil
}
func TestServiceEncryptedConfigCASAndTest(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, testdb.New(t))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	sender := &testSender{}
	key := []byte(strings.Repeat("k", 32))
	s, err := NewService(db, key, sender)
	if err != nil {
		t.Fatal(err)
	}
	initial, err := s.Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if initial.Config.Enabled || initial.Version != 1 {
		t.Fatal(initial)
	}
	input := Update{Version: initial.Version, Config: DefaultConfig(), Channels: ChannelsUpdate{DingTalk: DingTalkUpdate{Webhook: "https://oapi.dingtalk.com/robot/send?access_token=secret-access-token", Secret: "SEC-private"}, Email: EmailUpdate{Port: 587, TLSMode: "starttls", To: []string{}}}}
	saved, err := s.Update(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(saved)
	if strings.Contains(string(raw), "secret-access-token") || strings.Contains(string(raw), "SEC-private") {
		t.Fatal("读取接口泄漏凭据")
	}
	if !saved.Channels.DingTalk.HasWebhook || !saved.Channels.DingTalk.HasSecret {
		t.Fatal("未返回凭据存在标记")
	}
	record, err := db.GetMonitorConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(record.ConfigJSON, "secret-access-token") || strings.Contains(record.SecretsCiphertext, "secret-access-token") {
		t.Fatal("数据库泄漏明文凭据")
	}
	if _, err := s.Update(ctx, input); err == nil {
		t.Fatal("旧版本必须冲突")
	}
	input.Version = saved.Version
	input.Channels.DingTalk = DingTalkUpdate{}
	again, err := s.Update(ctx, input)
	if err != nil || !again.Channels.DingTalk.HasWebhook {
		t.Fatalf("空输入必须保留: %v", err)
	}
	if err := s.Test(ctx, "dingtalk"); err != nil {
		t.Fatal(err)
	}
	if sender.calls != 1 {
		t.Fatal(sender.calls)
	}
	deliveries, err := db.ListMonitorDeliveries(ctx, 10)
	if err != nil || len(deliveries) != 1 || deliveries[0].Event != "test" {
		t.Fatal(deliveries, err)
	}
	wrong, _ := NewService(db, []byte(strings.Repeat("z", 32)), sender)
	if _, err := wrong.Get(ctx); err == nil {
		t.Fatal("错误密钥应拒绝解密")
	}
	input.Version = again.Version
	input.Channels.DingTalk.ClearWebhook = true
	input.Channels.DingTalk.ClearSecret = true
	cleared, err := s.Update(ctx, input)
	if err != nil || cleared.Channels.DingTalk.HasWebhook || cleared.Channels.DingTalk.HasSecret {
		t.Fatal(cleared, err)
	}
}
