package httpx

// 本文件从生产 NewApp 装配入口验证 HTTP 契约，而不是自行组装一套
// 测试专用 Deps。框架替换时只测路由很容易漏掉真实启动的依赖和生命周期。

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"arkade/hub/internal/config"
	"arkade/hub/internal/dbbackup"
)

func TestRuntimeSnapshotRestoreStartsWithExistingAccount(t *testing.T) {
	ctx := context.Background()
	cfg := config.Default()
	cfg.Database.Path = filepath.Join(t.TempDir(), "original.db")
	cfg.Auth.AgentToken = "restore-test-token"
	cfg.Auth.InitialPass = "RestoreTestPass123!"
	cfg.Web.Dist = ""
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	original, err := NewApp(ctx, cfg, log, "snapshot-test")
	if err != nil {
		t.Fatal(err)
	}
	restored := filepath.Join(t.TempDir(), "restored.db")
	err = dbbackup.Snapshot(ctx, cfg.Database.Path, restored)
	closeErr := original.Shutdown(ctx)
	if err != nil || closeErr != nil {
		t.Fatalf("snapshot=%v close=%v", err, closeErr)
	}
	cfg.Database.Path = restored
	cfg.Auth.InitialPass = "MustNotReplaceExistingPass123!"
	app, err := NewApp(ctx, cfg, log, "restore-test")
	if err != nil {
		t.Fatalf("restore rejected by real migration/startup checks: %v", err)
	}
	t.Cleanup(func() { _ = app.Shutdown(ctx) })
	app.server.SetAddr("127.0.0.1:0")
	if err := app.server.Start(); err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Post("http://"+app.server.GetListenedAddress()+"/api/auth/login", "application/json", strings.NewReader(`{"username":"admin","password":"RestoreTestPass123!"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("restored account login status=%d body=%s", resp.StatusCode, body)
	}
}

func TestRuntimeAppHTTPContract(t *testing.T) {
	cfg := config.Default()
	cfg.Database.Path = filepath.Join(t.TempDir(), "runtime.db")
	cfg.Auth.AgentToken = "runtime-test-agent-token-only"
	cfg.Auth.InitialPass = "RuntimeTestPass123!"
	cfg.Web.Dist = ""
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	app, err := NewApp(context.Background(), cfg, log, "gf-runtime-test")
	if err != nil {
		t.Fatalf("真实启动装配失败: %v", err)
	}
	// 真实 Start 初始化框架请求生命周期；不能由外层 httptest 掩盖清理 panic。
	app.server.SetAddr("127.0.0.1:0")
	if err := app.server.Start(); err != nil {
		_ = app.Shutdown(context.Background())
		t.Fatal(err)
	}
	baseURL := "http://" + app.server.GetListenedAddress()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := app.Shutdown(ctx); err != nil {
			t.Errorf("关闭真实装配失败: %v", err)
		}
	})
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Jar: jar, Timeout: 5 * time.Second}

	response, err := client.Get(baseURL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	var health struct {
		Status  string `json:"status"`
		Version string `json:"version"`
	}
	err = json.NewDecoder(response.Body).Decode(&health)
	response.Body.Close()
	if err != nil || response.StatusCode != http.StatusOK || health.Status != "ok" || health.Version != "gf-runtime-test" {
		t.Fatalf("健康检查契约改变: HTTP %d %+v err=%v", response.StatusCode, health, err)
	}

	response, err = client.Post(baseURL+"/api/auth/login", "application/json", strings.NewReader(`{"username":"admin","password":"RuntimeTestPass123!"}`))
	if err != nil {
		t.Fatal(err)
	}
	var login userEnvelope
	err = json.NewDecoder(response.Body).Decode(&login)
	cookies := response.Cookies()
	response.Body.Close()
	if err != nil || response.StatusCode != http.StatusOK || login.User.Username != "admin" {
		t.Fatalf("登录契约改变: HTTP %d %+v err=%v", response.StatusCode, login, err)
	}
	if len(cookies) == 0 || !cookies[0].HttpOnly || cookies[0].SameSite != http.SameSiteLaxMode {
		t.Fatalf("登录安全 Cookie 属性缺失: %+v", cookies)
	}
	response, err = client.Get(baseURL + "/api/auth/me")
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("登录后会话未生效: HTTP %d", response.StatusCode)
	}

	// Agent 认证独立于浏览器会话：即使已登录，没有 Agent Bearer 仍应拒绝。
	response, err = client.Get(baseURL + "/agent/connect")
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("Agent 连接错误地使用了浏览器会话: HTTP %d", response.StatusCode)
	}

	request, err := http.NewRequest(http.MethodPost, baseURL+"/api/auth/logout", nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err = client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	logoutBody, err := io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil || response.StatusCode != http.StatusNoContent || len(logoutBody) != 0 {
		t.Fatalf("退出契约改变: status=%d body=%q err=%v", response.StatusCode, logoutBody, err)
	}
	response, err = client.Get(baseURL + "/api/auth/me")
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("退出后仍能使用会话: HTTP %d", response.StatusCode)
	}
}
