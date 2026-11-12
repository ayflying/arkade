package monitor

import "arkade/hub/internal/notify"

// 凭据仅在写接口接受；读取只提供是否设置，不回显Webhook的access_token。
type DingTalkUpdate struct {
	Webhook      string `json:"webhook"`
	Secret       string `json:"secret"`
	ClearWebhook bool   `json:"clear_webhook"`
	ClearSecret  bool   `json:"clear_secret"`
}
type EmailUpdate struct {
	Host          string   `json:"host"`
	Port          int      `json:"port"`
	TLSMode       string   `json:"tls_mode"`
	Username      string   `json:"username"`
	From          string   `json:"from"`
	To            []string `json:"to"`
	Password      string   `json:"password"`
	ClearPassword bool     `json:"clear_password"`
}
type ChannelsUpdate struct {
	DingTalk DingTalkUpdate `json:"dingtalk"`
	Email    EmailUpdate    `json:"email"`
}
type Update struct {
	Version  int64          `json:"version"`
	Config   Config         `json:"config"`
	Channels ChannelsUpdate `json:"channels"`
}
type DingTalkView struct {
	Webhook    string `json:"webhook"`
	HasWebhook bool   `json:"has_webhook"`
	HasSecret  bool   `json:"has_secret"`
}
type EmailView struct {
	Host        string   `json:"host"`
	Port        int      `json:"port"`
	TLSMode     string   `json:"tls_mode"`
	Username    string   `json:"username"`
	From        string   `json:"from"`
	To          []string `json:"to"`
	HasPassword bool     `json:"has_password"`
}
type ChannelsView struct {
	DingTalk DingTalkView `json:"dingtalk"`
	Email    EmailView    `json:"email"`
}
type ConfigView struct {
	Version  int64        `json:"version"`
	Config   Config       `json:"config"`
	Channels ChannelsView `json:"channels"`
}
type storedConfig struct {
	Config Config    `json:"config"`
	Email  EmailView `json:"email"`
}
type credentials struct {
	DingTalk      notify.DingTalkConfig `json:"dingtalk"`
	EmailPassword string                `json:"email_password"`
}
