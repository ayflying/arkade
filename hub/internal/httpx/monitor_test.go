package httpx

import (
	"arkade/hub/internal/config"
	"arkade/hub/internal/monitor"
	"arkade/hub/internal/testdb"
	"arkade/hub/internal/user"
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
)

// 从生产装配入口验证管理员边界、凭据脱敏及配置版本冲突。
func TestMonitorAdminHTTP(t *testing.T) {
	ctx := context.Background()
	cfg := config.Default()
	cfg.Database.DSN = testdb.New(t)
	cfg.Auth.SettingsKeyFile = filepath.Join(t.TempDir(), "settings.key")
	cfg.Auth.InitialPass = "1"
	cfg.Web.Dist = ""
	app, err := NewApp(ctx, cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), "monitor-test")
	if err != nil {
		t.Fatal(err)
	}
	defer app.Shutdown(ctx)
	app.server.SetAddr("127.0.0.1:0")
	if err = app.server.Start(); err != nil {
		t.Fatal(err)
	}
	base := "http://" + app.server.GetListenedAddress()
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar, Timeout: 10 * time.Second}
	response, err := client.Get(base + "/api/monitor/config")
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("匿名访问=%d", response.StatusCode)
	}
	response, err = client.Post(base+"/api/auth/login", "application/json", strings.NewReader(`{"username":"admin","password":"1"}`))
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != 200 {
		t.Fatal(response.StatusCode)
	}
	response, err = client.Get(base + "/api/monitor/config")
	if err != nil {
		t.Fatal(err)
	}
	var view monitor.ConfigView
	err = json.NewDecoder(response.Body).Decode(&view)
	response.Body.Close()
	if err != nil || view.Version != 1 || view.Config.Enabled {
		t.Fatal(view, err)
	}
	input := monitor.Update{Version: view.Version, Config: view.Config, Channels: monitor.ChannelsUpdate{DingTalk: monitor.DingTalkUpdate{Webhook: "https://oapi.dingtalk.com/robot/send?access_token=private-notify-token"}, Email: monitor.EmailUpdate{Port: 587, TLSMode: "starttls", To: []string{}}}}
	raw, _ := json.Marshal(input)
	for _, want := range []int{http.StatusOK, http.StatusConflict} {
		req, _ := http.NewRequest(http.MethodPut, base+"/api/monitor/config", strings.NewReader(string(raw)))
		req.Header.Set("Content-Type", "application/json")
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != want {
			t.Fatalf("保存监控配置=%d want=%d body=%s", resp.StatusCode, want, body)
		}
		if strings.Contains(string(body), "private-notify-token") {
			t.Fatal("响应泄漏通知凭据")
		}
	}
	response, err = client.Get(base + "/api/monitor/status")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != 200 || !strings.Contains(string(body), `"metrics":[]`) {
		t.Fatalf("监控状态响应异常=%d %s", response.StatusCode, body)
	}
	hash, _ := user.HashPassword("ViewerPass123!")
	if err = app.db.CreateUser(ctx, user.User{ID: "monitor-viewer", Username: "monitor-viewer", DisplayName: "只读用户", Role: user.RoleUser, PasswordHash: hash, CreatedAt: time.Now(), UpdatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	viewerJar, _ := cookiejar.New(nil)
	viewer := &http.Client{Jar: viewerJar, Timeout: 10 * time.Second}
	resp, err := viewer.Post(base+"/api/auth/login", "application/json", strings.NewReader(`{"username":"monitor-viewer","password":"ViewerPass123!"}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatal(resp.StatusCode)
	}
	for _, path := range []string{"/api/monitor/config", "/api/monitor/status"} {
		resp, err := viewer.Get(base + path)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden {
			t.Fatalf("普通用户访问%s=%d", path, resp.StatusCode)
		}
	}
}
