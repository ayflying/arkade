package httpx

// gate_log_test.go 验证提前拒绝的请求在访问日志里状态码正确。
//
// ## 为什么单独测这条
//
// 认证守卫在链**中间**提前写出 401，此时链底尚未把 GF 的
// *response.Writer 换成当前 statusWriter。若守卫直接用 GF writer 写，
// 响应 JSON 是对的，但 requestLogger 包在外层的 statusWriter
// 根本看不到这次写入，日志会记成默认的 200。
//
// 只断言"响应体是 401"抓不到这个问题——必须断言**日志里的状态**。

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"testing"
)

// logCapture 收集日志行，供断言使用。
type logCapture struct {
	mu   sync.Mutex
	buf  bytes.Buffer
	line []string
}

func (c *logCapture) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.buf.Write(p)
	c.line = append(c.line, string(p))
	return len(p), nil
}

// find 返回包含全部 fragment 的第一行日志。
func (c *logCapture) find(fragments ...string) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, l := range c.line {
		ok := true
		for _, f := range fragments {
			if !strings.Contains(l, f) {
				ok = false
				break
			}
		}
		if ok {
			return l
		}
	}
	return ""
}

// TestGateRejectionLogsStatus 验证未认证请求的访问日志状态码是 401。
func TestGateRejectionLogsStatus(t *testing.T) {
	cap := &logCapture{}
	log := slog.New(slog.NewTextHandler(cap, &slog.HandlerOptions{Level: slog.LevelInfo}))

	// 用真实依赖启动一台独立服务，只为拿到可捕获的日志。
	e := newTestEnvWithLogger(t, log)

	req, err := http.NewRequest(http.MethodGet, e.wsURL+"/api/auth/me", bytes.NewReader(nil))
	if err != nil {
		t.Fatalf("构造请求失败: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("请求失败: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("未认证请求应 401，实际 %d（body=%q）", resp.StatusCode, body)
	}

	// 访问日志必须记录 401，而不是默认的 200。
	line := cap.find("/api/auth/me", "status=")
	if line == "" {
		t.Fatalf("未找到 /api/auth/me 的访问日志")
	}
	if !strings.Contains(line, "status=401") {
		t.Fatalf("访问日志状态码错误（守卫提前写出时 writer 未接上 statusWriter）：%s", line)
	}
	_ = context.Background()
}
