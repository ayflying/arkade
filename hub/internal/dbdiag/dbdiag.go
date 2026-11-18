// Package dbdiag 把数据库连接失败翻译成可操作的中文诊断。
//
// 设计原因：原先所有启动失败都被压成"连接 PostgreSQL 失败（请检查数据库配置和服务状态）"，
// 运维无法区分是网络不通、端口错误、密码错误、库不存在还是权限不足。
// 这里只在启动期使用：错误信息会脱敏（不含密码），并附上实际使用的端点，
// 便于直接对照配置；分类依据 lib/pq 返回的 SQLSTATE 与 Go 网络错误。
package dbdiag

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net"
	"strings"
	"syscall"
	"time"

	"github.com/lib/pq"
)

// Endpoint 是脱敏后的连接目标，只含协议、主机端口、库名与用户名。
type Endpoint struct {
	Host     string
	Port     string
	Database string
	User     string
	SSLMode  string
}

// String 输出可直接对照配置的一行文本，绝不含密码。
func (e Endpoint) String() string {
	var b strings.Builder
	b.WriteString("postgres://")
	if e.User != "" {
		b.WriteString(e.User)
		b.WriteString("@")
	}
	host := e.Host
	if host == "" {
		host = "localhost"
	}
	b.WriteString(host)
	if e.Port != "" {
		b.WriteString(":")
		b.WriteString(e.Port)
	}
	b.WriteString("/")
	b.WriteString(e.Database)
	if e.SSLMode != "" {
		b.WriteString("?sslmode=")
		b.WriteString(e.SSLMode)
	}
	return b.String()
}

// ParseEndpoint 从 URL 或 keyword DSN 提取端点，用于报错展示。
// 解析失败时返回空端点，调用方仍可输出原始错误。
func ParseEndpoint(dsn string) Endpoint {
	var e Endpoint
	trimmed := strings.TrimSpace(dsn)
	if strings.HasPrefix(trimmed, "postgres://") || strings.HasPrefix(trimmed, "postgresql://") {
		rest := trimmed[strings.Index(trimmed, "://")+3:]
		if at := strings.LastIndex(rest, "@"); at >= 0 {
			if ui := strings.LastIndex(rest[:at], "/"); ui >= 0 {
				rest = rest[ui+1:]
			}
			userInfo := rest[:strings.LastIndex(rest, "@")]
			if colon := strings.Index(userInfo, ":"); colon >= 0 {
				e.User = userInfo[:colon]
			} else {
				e.User = userInfo
			}
			rest = rest[strings.LastIndex(rest, "@")+1:]
		}
		hostPort := rest
		if slash := strings.Index(rest, "/"); slash >= 0 {
			hostPort = rest[:slash]
			rest = rest[slash+1:]
		} else {
			rest = ""
		}
		if query := strings.Index(rest, "?"); query >= 0 {
			rest = rest[:query]
		}
		e.Database = rest
		if host, port, err := net.SplitHostPort(hostPort); err == nil {
			e.Host, e.Port = host, port
		} else {
			e.Host = hostPort
		}
	} else {
		fields := strings.Fields(trimmed)
		for i := 0; i < len(fields); i++ {
			field := fields[i]
			key, value, ok := strings.Cut(field, "=")
			if !ok && i+1 < len(fields) && fields[i+1] == "=" {
				// "host = value" 形态：键与等号之间被空格拆开。
				key = field
				if i+2 < len(fields) {
					i += 2
					value = fields[i]
				} else {
					i++
					value = ""
				}
				ok = true
			} else if ok && strings.TrimSpace(value) == "" && i+1 < len(fields) && !strings.Contains(fields[i+1], "=") {
				// "host =value" 与 "port= 5433" 形态。
				i++
				value = fields[i]
			}
			if !ok {
				continue
			}
			value = strings.Trim(value, "'\"")
			switch strings.ToLower(strings.TrimSpace(key)) {
			case "host":
				e.Host = value
			case "port":
				e.Port = value
			case "dbname", "database":
				e.Database = value
			case "user":
				e.User = value
			case "sslmode":
				e.SSLMode = value
			}
		}
	}
	if strings.Contains(trimmed, "sslmode=") {
		for _, field := range append(strings.Fields(trimmed), strings.Split(trimmed, "&")...) {
			if _, value, ok := strings.Cut(field, "sslmode="); ok {
				value = strings.SplitN(value, "&", 2)[0]
				e.SSLMode = strings.Trim(value, "'\"")
				break
			}
		}
	}
	if e.Port == "" {
		e.Port = "5432"
	}
	if e.SSLMode == "" {
		e.SSLMode = "disable"
	}
	return e
}

// Probe 拨号并用真实凭据做一次认证级连通检查，返回分类后的中文诊断。
// 只做只读探测（SELECT 1），不会修改数据库。
func Probe(ctx context.Context, dsn string) error {
	ep := ParseEndpoint(dsn)
	timeout := 5 * time.Second
	if deadline, ok := ctx.Deadline(); ok {
		if remaining := time.Until(deadline); remaining > 0 && remaining < timeout {
			timeout = remaining
		}
	}
	if ep.Host != "" {
		conn, err := net.DialTimeout("tcp", net.JoinHostPort(ep.Host, ep.Port), timeout)
		if err == nil {
			_ = conn.Close()
		} else {
			return fmt.Errorf("网络层无法连到 %s 的 %s 端口：%s\n"+
				"这说明是网络或端口问题，与用户名密码无关。请依次确认：\n"+
				"  1. 数据库服务已启动，且监听该端口；\n"+
				"  2. 防火墙、安全组、路由放通本机到 %s:%s；\n"+
				"  3. 配置里的地址端口填写正确（当前为 %s）",
				ep.Host, ep.Port, describeNetError(err, timeout), ep.Host, ep.Port, ep.String())
		}
	}
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		return fmt.Errorf("DSN 格式无法被驱动接受（当前 %s）：%s；密码等敏感参数不显示", ep.String(), sanitize(err, dsn))
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	pingCtx, cancel := context.WithTimeout(ctx, timeout+5*time.Second)
	defer cancel()
	if err = db.PingContext(pingCtx); err != nil {
		return fmt.Errorf("TCP 已连通 %s，但 PostgreSQL 认证或建库阶段失败：%s\n%s",
			ep.String(), sanitize(err, dsn), authHint(err, ep))
	}
	var one int
	if err = db.QueryRowContext(pingCtx, "SELECT 1").Scan(&one); err != nil {
		return fmt.Errorf("认证成功但执行 SELECT 1 失败：%s\n请检查账号是否有该库的查询权限", sanitize(err, dsn))
	}
	return nil
}

// authHint 依据 SQLSTATE 给出可执行的处理建议。
func authHint(err error, ep Endpoint) string {
	code, message := sqlState(err)
	text := strings.ToLower(message)
	switch {
	case code == "28P01" || strings.Contains(text, "password authentication failed"):
		return "→ 用户名或密码错误（SQLSTATE 28P01）。请核对配置里的密码，注意特殊字符 @ # / 需要 URL 编码。"
	case code == "28000":
		return "→ 账号认证方式被拒绝（SQLSTATE 28000）。请检查 pg_hba.conf 的 host 规则与账号状态。"
	case code == "3D000":
		return "→ 目标数据库不存在（SQLSTATE 3D000）：库 " + ep.Database + " 未创建。"
	case code == "42501":
		return "→ 账号权限不足（SQLSTATE 42501）。请给 " + ep.User + " 授予该库的 CONNECT 权限。"
	case code == "53300":
		return "→ 连接数已满（SQLSTATE 53300）。请检查 max_connections 或释放空闲连接。"
	case code == "57P03":
		return "→ 数据库正在启动或处于恢复状态（SQLSTATE 57P03），请稍后重试。"
	case strings.Contains(text, "no pg_hba.conf entry"):
		return "→ pg_hba.conf 未放通本机来源，需要为该地址添加 host 规则并从库侧 reload。"
	case strings.Contains(text, "tls") || strings.Contains(text, "ssl"):
		return "→ SSL/TLS 协商失败，请核对 sslmode（当前 " + ep.SSLMode + "）。"
	}
	return "→ 未识别的认证失败，请对照上面的原始错误与数据库侧日志排查。"
}

// describeNetError 把网络错误翻译成中文原因。
func describeNetError(err error, timeout time.Duration) string {
	switch {
	case errors.Is(err, syscall.ECONNREFUSED):
		return "连接被拒绝，端口没有服务在监听（可能端口写错或数据库只监听本机）"
	case errors.Is(err, syscall.EHOSTUNREACH), errors.Is(err, syscall.ENETUNREACH):
		return "网络不可达，本机到目标主机没有路由或未放通"
	case errors.Is(err, context.DeadlineExceeded):
		return "连接超时（" + timeout.String() + "），多为防火墙丢包或地址错误"
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return "连接超时（" + timeout.String() + "），多为防火墙丢包或地址错误"
	}
	return err.Error()
}

// sanitize 移除错误文本中可能出现的密码。
func sanitize(err error, dsn string) string {
	message := err.Error()
	ep := ParseEndpoint(dsn)
	_ = ep
	lowered := strings.ToLower(dsn)
	if i := strings.Index(lowered, "password="); i >= 0 {
		rest := dsn[i+len("password="):]
		value := rest
		if delimiter := strings.IndexAny(rest, " \t\r\n"); delimiter >= 0 {
			value = rest[:delimiter]
		}
		value = strings.Trim(value, "'\"")
		if len(value) >= 3 {
			message = strings.ReplaceAll(message, value, "***")
		}
	}
	if u := strings.Index(dsn, "://"); u >= 0 {
		rest := dsn[u+3:]
		if at := strings.Index(rest, "@"); at >= 0 {
			userInfo := rest[:at]
			if colon := strings.Index(userInfo, ":"); colon >= 0 {
				if password := userInfo[colon+1:]; len(password) >= 3 {
					message = strings.ReplaceAll(message, password, "***")
				}
			}
		}
	}
	return message
}

// sqlState 提取 pg 错误码，非 pg 错误返回空。
func sqlState(err error) (string, string) {
	var pgErr *pq.Error
	if errors.As(err, &pgErr) {
		return string(pgErr.Code), pgErr.Message
	}
	return "", err.Error()
}
