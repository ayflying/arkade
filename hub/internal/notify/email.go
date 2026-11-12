package notify

import (
	"bytes"
	"context"
	"crypto/tls"
	"io"
	"mime"
	"mime/quotedprintable"
	"net"
	"net/smtp"
	"strconv"
	"strings"
	"time"

	"github.com/gogf/gf/v2/errors/gerror"
)

// responseConn 限制整个 SMTP 会话读取量，含多行响应与 TLS 握手。
type responseConn struct {
	net.Conn
	remaining int
}

func (c *responseConn) Read(p []byte) (int, error) {
	if c.remaining <= 0 {
		return 0, gerror.New("SMTP 响应过大")
	}
	if len(p) > c.remaining {
		p = p[:c.remaining]
	}
	n, err := c.Conn.Read(p)
	c.remaining -= n
	return n, err
}

func (s *DefaultSender) sendEmail(ctx context.Context, cfg EmailConfig, msg Message) error {
	if hasControl(msg.Title) {
		return gerror.New("邮件主题含控制字符")
	}
	raw, err := (&net.Dialer{}).DialContext(ctx, "tcp", net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port)))
	if err != nil {
		return sendError(ctx, "连接 SMTP")
	}
	defer raw.Close()
	// 关闭底层连接可中断欢迎语、TLS、AUTH、DATA 等任意阻塞阶段。
	stop := context.AfterFunc(ctx, func() { _ = raw.Close() })
	defer stop()
	if deadline, ok := ctx.Deadline(); ok {
		_ = raw.SetDeadline(deadline)
	}
	var conn net.Conn = &responseConn{Conn: raw, remaining: maxResponse}
	tc := &tls.Config{ServerName: cfg.Host, MinVersion: tls.VersionTLS12}
	if s != nil && s.tlsConfig != nil {
		tc = s.tlsConfig.Clone()
		tc.ServerName = cfg.Host
		tc.MinVersion = tls.VersionTLS12
	}
	if cfg.TLSMode == "tls" {
		secure := tls.Client(conn, tc)
		if err = secure.HandshakeContext(ctx); err != nil {
			return sendError(ctx, "SMTP TLS 握手")
		}
		conn = secure
	}
	client, err := smtp.NewClient(conn, cfg.Host)
	if err != nil {
		return sendError(ctx, "SMTP 欢迎语")
	}
	defer client.Close()
	if cfg.TLSMode == "starttls" {
		// StartTLS 在服务器未声明扩展时也不会继续发送明文邮件。
		if ok, _ := client.Extension("STARTTLS"); !ok {
			return gerror.New("SMTP 服务器不支持 STARTTLS")
		}
		if err = client.StartTLS(tc); err != nil {
			return sendError(ctx, "SMTP STARTTLS")
		}
	}
	if state, ok := client.TLSConnectionState(); !ok || !state.HandshakeComplete {
		return gerror.New("SMTP 未建立 TLS")
	}
	if cfg.Username != "" {
		if err = client.Auth(smtp.PlainAuth("", cfg.Username, cfg.Password, cfg.Host)); err != nil {
			return sendError(ctx, "SMTP 认证")
		}
	}
	from, _ := mailbox(cfg.From)
	if err = client.Mail(from.Address); err != nil {
		return sendError(ctx, "SMTP 发件人")
	}
	for _, v := range cfg.To {
		to, _ := mailbox(v)
		if err = client.Rcpt(to.Address); err != nil {
			return sendError(ctx, "SMTP 收件人")
		}
	}
	data, err := client.Data()
	if err != nil {
		return sendError(ctx, "SMTP DATA")
	}
	if _, err = io.Copy(data, bytes.NewReader(emailBody(cfg, msg))); err != nil {
		return sendError(ctx, "写入邮件")
	}
	if err = data.Close(); err != nil {
		return sendError(ctx, "SMTP 接收邮件")
	}
	if err = client.Quit(); err != nil {
		return sendError(ctx, "SMTP 结束会话")
	}
	return nil
}

func emailBody(cfg EmailConfig, msg Message) []byte {
	from, _ := mailbox(cfg.From)
	to := make([]string, 0, len(cfg.To))
	for _, v := range cfg.To {
		a, _ := mailbox(v)
		to = append(to, a.String())
	}
	var b bytes.Buffer
	b.WriteString("From: " + from.String() + "\r\nTo: " + strings.Join(to, ", ") + "\r\n")
	b.WriteString("Subject: " + mime.QEncoding.Encode("UTF-8", msg.Title) + "\r\n")
	b.WriteString("Date: " + time.Now().Format(time.RFC1123Z) + "\r\n")
	b.WriteString("MIME-Version: 1.0\r\nContent-Type: text/plain; charset=UTF-8\r\nContent-Transfer-Encoding: quoted-printable\r\n\r\n")
	w := quotedprintable.NewWriter(&b)
	_, _ = w.Write([]byte(msg.Body))
	_ = w.Close()
	return b.Bytes()
}
