package monitor

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"sync"

	"arkade/hub/internal/notify"
	"arkade/hub/internal/store"
	"github.com/gogf/gf/v2/errors/gerror"
)

type Service struct {
	db     *store.DB
	aead   cipher.AEAD
	sender notify.Sender
	mu     sync.Mutex
}

func NewService(db *store.DB, key []byte, sender notify.Sender) (*Service, error) {
	if len(key) != 32 {
		return nil, gerror.New("监控配置加密主密钥必须为32字节")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	if sender == nil {
		sender = notify.New()
	}
	return &Service{db: db, aead: aead, sender: sender}, nil
}
func (s *Service) seal(value credentials) (string, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, s.aead.NonceSize())
	if _, err = rand.Read(nonce); err != nil {
		return "", err
	}
	return "v1:" + base64.RawStdEncoding.EncodeToString(s.aead.Seal(nonce, nonce, raw, []byte("arkade.monitor.channels.v1"))), nil
}
func (s *Service) unseal(value string) (credentials, error) {
	var out credentials
	if value == "" {
		return out, nil
	}
	if len(value) < 3 || value[:3] != "v1:" {
		return out, gerror.New("监控通知凭据格式无效")
	}
	raw, err := base64.RawStdEncoding.DecodeString(value[3:])
	if err != nil || len(raw) < s.aead.NonceSize() {
		return out, gerror.New("监控通知凭据格式无效")
	}
	n := s.aead.NonceSize()
	plain, err := s.aead.Open(nil, raw[:n], raw[n:], []byte("arkade.monitor.channels.v1"))
	if err != nil {
		return out, gerror.New("监控通知凭据无法解密，请恢复原主密钥")
	}
	if json.Unmarshal(plain, &out) != nil {
		return out, gerror.New("监控通知凭据内容无效")
	}
	return out, nil
}
func (s *Service) load(ctx context.Context) (store.MonitorConfig, storedConfig, credentials, error) {
	row, err := s.db.GetMonitorConfig(ctx)
	data := storedConfig{Config: DefaultConfig(), Email: EmailView{Port: 587, TLSMode: "starttls", To: []string{}}}
	if err != nil {
		return row, data, credentials{}, err
	}
	if row.ConfigJSON != "" && row.ConfigJSON != "{}" {
		if json.Unmarshal([]byte(row.ConfigJSON), &data) != nil {
			return row, data, credentials{}, gerror.New("监控配置内容损坏")
		}
	}
	secrets, err := s.unseal(row.SecretsCiphertext)
	return row, data, secrets, err
}
func configView(version int64, data storedConfig, secrets credentials) ConfigView {
	email := data.Email
	email.HasPassword = secrets.EmailPassword != ""
	return ConfigView{Version: version, Config: data.Config, Channels: ChannelsView{DingTalk: DingTalkView{HasWebhook: secrets.DingTalk.Webhook != "", HasSecret: secrets.DingTalk.Secret != ""}, Email: email}}
}
func (s *Service) Get(ctx context.Context) (ConfigView, error) {
	row, data, secrets, err := s.load(ctx)
	if err != nil {
		return ConfigView{}, err
	}
	return configView(row.Version, data, secrets), nil
}
func (s *Service) Config(ctx context.Context) (Config, error) {
	_, data, _, err := s.load(ctx)
	return data.Config, err
}
func emailConfig(view EmailView, password string) notify.EmailConfig {
	return notify.EmailConfig{Host: view.Host, Port: view.Port, TLSMode: view.TLSMode, Username: view.Username, Password: password, From: view.From, To: view.To}
}
func (s *Service) Update(ctx context.Context, input Update) (ConfigView, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ValidateConfig(input.Config); err != nil {
		return ConfigView{}, err
	}
	row, previous, secrets, err := s.load(ctx)
	if err != nil {
		return ConfigView{}, err
	}
	if input.Version != row.Version {
		return ConfigView{}, store.ErrConflict
	}
	d := input.Channels.DingTalk
	e := input.Channels.Email
	if d.ClearWebhook {
		secrets.DingTalk.Webhook = ""
	}
	if d.ClearSecret {
		secrets.DingTalk.Secret = ""
	}
	if d.Webhook != "" {
		if d.Webhook != secrets.DingTalk.Webhook && d.Secret == "" {
			secrets.DingTalk.Secret = ""
		}
		secrets.DingTalk.Webhook = d.Webhook
	}
	if d.Secret != "" {
		secrets.DingTalk.Secret = d.Secret
	}
	// 密码绑定SMTP认证目标；不得把旧密码发送给管理员新指定的主机。
	if secrets.EmailPassword != "" && !e.ClearPassword && e.Password == "" && (e.Host != previous.Email.Host || e.Port != previous.Email.Port || e.TLSMode != previous.Email.TLSMode || e.Username != previous.Email.Username) {
		return ConfigView{}, gerror.New("更换SMTP主机、端口、安全模式或账号时必须重新填写密码或明确清除")
	}
	if e.ClearPassword {
		secrets.EmailPassword = ""
	}
	if e.Password != "" {
		secrets.EmailPassword = e.Password
	}
	data := storedConfig{Config: input.Config, Email: EmailView{Host: e.Host, Port: e.Port, TLSMode: e.TLSMode, Username: e.Username, From: e.From, To: e.To}}
	if secrets.DingTalk.Webhook != "" || input.Config.DingTalkEnabled {
		if err := notify.ValidateChannel(notify.ChannelConfig{Kind: "dingtalk", DingTalk: secrets.DingTalk}); err != nil {
			return ConfigView{}, err
		}
	}
	if e.Host != "" || input.Config.EmailEnabled {
		if err := notify.ValidateChannel(notify.ChannelConfig{Kind: "email", Email: emailConfig(data.Email, secrets.EmailPassword)}); err != nil {
			return ConfigView{}, err
		}
	}
	raw, err := json.Marshal(data)
	if err != nil {
		return ConfigView{}, err
	}
	encrypted, err := s.seal(secrets)
	if err != nil {
		return ConfigView{}, err
	}
	if err = s.db.UpdateMonitorConfig(ctx, store.MonitorConfig{ConfigJSON: string(raw), SecretsCiphertext: encrypted}, input.Version); err != nil {
		return ConfigView{}, err
	}
	return configView(input.Version+1, data, secrets), nil
}
