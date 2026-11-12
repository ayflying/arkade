package notify

import (
	"net"
	"net/mail"
	"net/url"
	"strings"
	"unicode"

	"github.com/gogf/gf/v2/errors/gerror"
)

// ValidateChannel 可在保存配置时调用；错误不包含配置原文。
func ValidateChannel(cfg ChannelConfig) error {
	switch cfg.Kind {
	case "dingtalk":
		if _, err := webhookURL(cfg.DingTalk.Webhook); err != nil {
			return err
		}
	case "email":
		c := cfg.Email
		if c.Host == "" || len(c.Host) > 253 || strings.ContainsAny(c.Host, "/\\:@[]") || hasControl(c.Host) || strings.TrimSpace(c.Host) != c.Host {
			// IPv6 使用不带方括号的地址，由 JoinHostPort 处理。
			if net.ParseIP(c.Host) == nil {
				return gerror.New("SMTP 主机无效")
			}
		}
		if c.Port < 1 || c.Port > 65535 {
			return gerror.New("SMTP 端口无效")
		}
		if c.TLSMode != "starttls" && c.TLSMode != "tls" {
			return gerror.New("SMTP 必须使用 starttls 或 tls")
		}
		if (c.Username == "") != (c.Password == "") {
			return gerror.New("SMTP 用户名与密码须同时配置或同时留空")
		}
		if hasControl(c.Username) {
			return gerror.New("SMTP 用户名无效")
		}
		if _, err := mailbox(c.From); err != nil {
			return gerror.New("发件地址无效")
		}
		if len(c.To) == 0 || len(c.To) > 100 {
			return gerror.New("收件地址数量无效")
		}
		for _, v := range c.To {
			if _, err := mailbox(v); err != nil {
				return gerror.New("收件地址无效")
			}
		}
	default:
		return gerror.New("通知渠道类型无效")
	}
	return nil
}

func hasControl(s string) bool {
	return strings.IndexFunc(s, unicode.IsControl) >= 0
}

func mailbox(s string) (*mail.Address, error) {
	if hasControl(s) {
		return nil, gerror.New("邮件地址含控制字符")
	}
	a, err := mail.ParseAddress(s)
	if err != nil || a.Address == "" || hasControl(a.Address) || hasControl(a.Name) {
		return nil, gerror.New("邮件地址无效")
	}
	return a, nil
}

func webhookURL(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host != "oapi.dingtalk.com" || u.User != nil || u.Opaque != "" || u.Path != "/robot/send" || u.RawPath != "" || u.Fragment != "" || strings.Contains(raw, "#") || u.ForceQuery {
		return nil, gerror.New("钉钉 Webhook 必须为官方 HTTPS 机器人地址")
	}
	q, err := url.ParseQuery(u.RawQuery)
	if err != nil || len(q) != 1 || len(q["access_token"]) != 1 || q.Get("access_token") == "" {
		return nil, gerror.New("钉钉 Webhook 参数无效")
	}
	token := q.Get("access_token")
	if len(token) > 1024 || strings.IndexFunc(token, func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-')
	}) >= 0 {
		return nil, gerror.New("钉钉 Webhook token 无效")
	}
	return u, nil
}
