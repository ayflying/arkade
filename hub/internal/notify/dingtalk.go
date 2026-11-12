package notify

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/gogf/gf/v2/errors/gerror"
)

func (s *DefaultSender) sendDingTalk(ctx context.Context, cfg DingTalkConfig, msg Message) error {
	u, err := webhookURL(cfg.Webhook)
	if err != nil {
		return err
	}
	if cfg.Secret != "" {
		timestamp := strconv.FormatInt(time.Now().UnixMilli(), 10)
		mac := hmac.New(sha256.New, []byte(cfg.Secret))
		_, _ = mac.Write([]byte(timestamp + "\n" + cfg.Secret))
		q := u.Query()
		q.Set("timestamp", timestamp)
		q.Set("sign", base64.StdEncoding.EncodeToString(mac.Sum(nil)))
		u.RawQuery = q.Encode()
	}
	payload, err := json.Marshal(struct {
		MsgType  string `json:"msgtype"`
		Markdown struct {
			Title string `json:"title"`
			Text  string `json:"text"`
		} `json:"markdown"`
	}{MsgType: "markdown", Markdown: struct {
		Title string `json:"title"`
		Text  string `json:"text"`
	}{msg.Title, msg.Body}})
	if err != nil {
		return sendError(ctx, "构造钉钉消息")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), bytes.NewReader(payload))
	if err != nil {
		return sendError(ctx, "构造钉钉请求")
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	// 克隆客户端，强制禁止重定向；仅未导出的 transport 注入支持本地测试。
	client := http.Client{Timeout: sendTimeout}
	if s != nil && s.httpClient != nil {
		client = *s.httpClient
		client.Timeout = sendTimeout
	}
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := client.Do(req)
	if err != nil {
		return sendError(ctx, "钉钉请求")
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponse+1))
	if err != nil {
		return sendError(ctx, "读取钉钉响应")
	}
	if len(body) > maxResponse {
		return gerror.New("钉钉响应过大")
	}
	if resp.StatusCode != http.StatusOK {
		return gerror.Newf("钉钉 HTTP 状态异常（%d）", resp.StatusCode)
	}
	var result struct {
		ErrCode *int `json:"errcode"`
	}
	if json.Unmarshal(body, &result) != nil || result.ErrCode == nil {
		return gerror.New("钉钉响应格式无效")
	}
	// 不回显 errmsg：服务器可能将 token 或完整请求带回。
	if *result.ErrCode != 0 {
		return gerror.Newf("钉钉业务错误（%d）", *result.ErrCode)
	}
	return nil
}
