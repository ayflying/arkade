// Package notify 提供独立的通知发送与配置校验，不持久化任何凭据。
package notify

import (
	"context"
	"crypto/tls"
	"net/http"
	"time"

	"github.com/gogf/gf/v2/errors/gerror"
)

// ChannelConfig 为单个通知渠道的配置。
type ChannelConfig struct {
	Kind     string         `json:"kind"`
	DingTalk DingTalkConfig `json:"dingtalk"`
	Email    EmailConfig    `json:"email"`
}

type DingTalkConfig struct {
	Webhook string `json:"webhook"`
	Secret  string `json:"secret"`
}

type EmailConfig struct {
	Host     string   `json:"host"`
	Port     int      `json:"port"`
	TLSMode  string   `json:"tls_mode"`
	Username string   `json:"username"`
	Password string   `json:"password"`
	From     string   `json:"from"`
	To       []string `json:"to"`
}

type Message struct{ Title, Body string }

type Sender interface {
	Send(context.Context, ChannelConfig, Message) error
}

// DefaultSender 的零值也可使用；测试注入点不对生产调用者开放。
type DefaultSender struct {
	httpClient *http.Client
	tlsConfig  *tls.Config
}

func New() *DefaultSender { return &DefaultSender{} }

const (
	sendTimeout = 15 * time.Second
	maxResponse = 64 << 10
	maxMessage  = 1 << 20
)

func (s *DefaultSender) Send(ctx context.Context, cfg ChannelConfig, msg Message) error {
	if err := ValidateChannel(cfg); err != nil {
		return err
	}
	if len(msg.Title)+len(msg.Body) > maxMessage {
		return gerror.New("通知内容过大")
	}
	ctx, cancel := context.WithTimeout(ctx, sendTimeout)
	defer cancel()
	if ctx.Err() != nil {
		return gerror.Wrap(ctx.Err(), "通知已取消")
	}
	if cfg.Kind == "dingtalk" {
		return s.sendDingTalk(ctx, cfg.DingTalk, msg)
	}
	return s.sendEmail(ctx, cfg.Email, msg)
}

// sendError 不携带原始网络/远端错误，防止 URL、口令或服务器回显泄漏。
func sendError(ctx context.Context, stage string) error {
	if ctx.Err() != nil {
		return gerror.Wrap(ctx.Err(), "通知已取消")
	}
	return gerror.New("通知发送失败：" + stage)
}
