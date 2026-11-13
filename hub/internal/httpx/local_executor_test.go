package httpx

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"arkade/hub/internal/agentlink"
	"arkade/hub/internal/config"
	"arkade/hub/internal/store"
	"arkade/hub/internal/testdb"
	"arkade/protocol/envelope"
)

func embeddedLocalTestConfig(t *testing.T) config.Config {
	t.Helper()
	cfg := config.Default()
	cfg.Database.DSN = testdb.New(t)
	cfg.Local.Enabled = true
	cfg.Local.Runtime = "none"
	cfg.Local.DataDir = t.TempDir()
	cfg.Auth.SettingsKeyFile = filepath.Join(t.TempDir(), "settings.key")
	cfg.Auth.InitialPass = "local-executor-test"
	cfg.Web.Dist = ""
	return cfg
}
func embeddedLocalTestApp(t *testing.T, cfg config.Config) *App {
	t.Helper()
	app, err := NewApp(context.Background(), cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), "embedded-production-test")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := app.Shutdown(ctx); err != nil {
			t.Error(err)
		}
	})
	return app
}

// 从 NewApp 生产装配出发，不握手、不启动 WebSocket 即可访问内置本机。
func TestProductionLocalExecutorNone(t *testing.T) {
	cfg := embeddedLocalTestConfig(t)
	app := embeddedLocalTestApp(t, cfg)
	ctx := context.Background()
	if app.localExecutor == nil || !app.registry.IsLocal(store.LocalAgentID) || !app.registry.IsOnline(store.LocalAgentID) || !app.registry.IsReservedLocal(store.LocalAgentID) {
		t.Fatal("本机执行器未被生产入口注册")
	}
	if app.registry.Count() != 1 {
		t.Fatalf("在线数量=%d", app.registry.Count())
	}
	if _, remote := app.registry.Get(store.LocalAgentID); remote {
		t.Fatal("内置本机伪造了远程 WebSocket 连接")
	}
	hasToken, err := app.db.HasAgentToken(ctx, store.LocalAgentID)
	if err != nil || hasToken {
		t.Fatalf("内置本机不得创建网络令牌: %v %v", hasToken, err)
	}
	rec, err := app.db.GetAgent(ctx, store.LocalAgentID)
	if err != nil || rec.Runtime != "none" || rec.LastSeen == 0 {
		t.Fatalf("本机运行快照不完整: %+v %v", rec, err)
	}
	// none 的只读列表按既有契约返回空集合，操作和监控能力才返回 unsupported。
	containers, callErr := agentlink.NewOps(app.registry).ListContainers(ctx, store.LocalAgentID)
	if callErr != nil || len(containers.Containers) != 0 {
		t.Fatalf("none 运行时应成功返回空容器列表: %+v %v", containers, callErr)
	}
	source := &monitorSource{db: app.db, registry: app.registry}
	agents, err := source.Agents(ctx)
	if err != nil || len(agents) != 1 || agents[0].ID != store.LocalAgentID || !agents[0].Online {
		t.Fatalf("监控源未看到在线本机: %+v %v", agents, err)
	}
	snapshot, err := source.Snapshot(ctx, store.LocalAgentID)
	if err == nil && snapshot.Error == "" {
		t.Fatalf("none 监控必须明确不可用: %+v", snapshot)
	}

	// 同一个生产 Server 验证匿名边界和登录后的在线列表展示。
	app.server.SetAddr("127.0.0.1:0")
	if err = app.server.Start(); err != nil {
		t.Fatal(err)
	}
	base := "http://" + app.server.GetListenedAddress()
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar, Timeout: 10 * time.Second}
	resp, err := client.Get(base + "/api/agents")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("匿名服务器列表=%d", resp.StatusCode)
	}
	resp, err = client.Post(base+"/api/auth/login", "application/json", strings.NewReader(`{"username":"admin","password":"local-executor-test"}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("登录失败=%d", resp.StatusCode)
	}
	resp, err = client.Get(base + "/api/agents")
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil || resp.StatusCode != http.StatusOK || !strings.Contains(string(body), `"agent-local"`) || !strings.Contains(string(body), `"status":"online"`) {
		t.Fatalf("生产列表未展示在线本机: %d %s %v", resp.StatusCode, body, err)
	}
	if strings.Contains(string(body), "token_hash") {
		t.Fatal("服务器列表泄漏令牌摘要")
	}
	if err = app.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	if app.registry.IsOnline(store.LocalAgentID) || app.registry.IsLocal(store.LocalAgentID) || !app.registry.IsReservedLocal(store.LocalAgentID) {
		t.Fatal("关闭后必须离线且保留本机身份")
	}
	_, callErr = app.registry.Call(ctx, store.LocalAgentID, envelope.MethodContainerList, nil)
	if callErr == nil || callErr.Code != envelope.CodeAgentOffline {
		t.Fatalf("关闭本机后仍可调用: %v", callErr)
	}
}

func TestProductionLocalExecutorDisabledConfiguration(t *testing.T) {
	cfg := embeddedLocalTestConfig(t)
	cfg.Local.Enabled = false
	app := embeddedLocalTestApp(t, cfg)
	if app.localExecutor != nil || app.registry.IsOnline(store.LocalAgentID) || !app.registry.IsReservedLocal(store.LocalAgentID) {
		t.Fatal("禁用配置仍启动本机或释放了保留身份")
	}
	_, err := app.db.GetAgent(context.Background(), store.LocalAgentID)
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("禁用配置不应创建本机记录: %v", err)
	}
	source := &monitorSource{db: app.db, registry: app.registry}
	agents, err := source.Agents(context.Background())
	if err != nil || len(agents) != 0 {
		t.Fatalf("禁用配置仍进入监控清单: %+v %v", agents, err)
	}
}

// 关闭配置不删除已有身份，也不能把保留的本机误当成远程失联节点。
func TestProductionLocalExecutorDisabledConfigurationPreservesIdentity(t *testing.T) {
	ctx := context.Background()
	cfg := embeddedLocalTestConfig(t)
	db, err := store.Open(ctx, cfg.Database.DSN)
	if err != nil {
		t.Fatal(err)
	}
	if err = db.EnsureEmbeddedLocalAgent(ctx); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err = db.UpdateEmbeddedLocalAgentRuntime(ctx, store.AgentRecord{ID: store.LocalAgentID, Status: "online", Runtime: "none", LastSeen: time.Now().Unix(), UpdatedAt: time.Now().Unix()}); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	cfg.Local.Enabled = false
	app := embeddedLocalTestApp(t, cfg)
	rec, err := app.db.GetAgent(ctx, store.LocalAgentID)
	if err != nil || rec.Disabled {
		t.Fatalf("配置关闭不应删除或禁用已有本机记录: %+v %v", rec, err)
	}
	if app.localExecutor != nil || app.registry.IsOnline(store.LocalAgentID) || !app.registry.IsReservedLocal(store.LocalAgentID) {
		t.Fatal("配置关闭仍启动本机或释放身份")
	}
	source := &monitorSource{db: app.db, registry: app.registry}
	agents, err := source.Agents(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, agent := range agents {
		if agent.ID == store.LocalAgentID {
			t.Fatalf("配置关闭的保留本机被误纳入失联监控: %+v", agent)
		}
	}
}

func TestProductionLocalExecutorDisabledIdentityFailsClosed(t *testing.T) {
	ctx := context.Background()
	cfg := embeddedLocalTestConfig(t)
	db, err := store.Open(ctx, cfg.Database.DSN)
	if err != nil {
		t.Fatal(err)
	}
	// 模拟数据库被人为设为禁用；启动必须安全拒绝，而不是自行恢复。
	if err = db.EnsureEmbeddedLocalAgent(ctx); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err = db.SetAgentDisabled(ctx, store.LocalAgentID, true, time.Now().Unix()); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	app := embeddedLocalTestApp(t, cfg)
	rec, err := app.db.GetAgent(ctx, store.LocalAgentID)
	if err != nil || !rec.Disabled {
		t.Fatalf("启动不应重新启用数据库中的禁用身份: %+v %v", rec, err)
	}
	if app.localExecutor != nil || app.registry.IsLocal(store.LocalAgentID) || app.registry.IsOnline(store.LocalAgentID) || !app.registry.IsReservedLocal(store.LocalAgentID) {
		t.Fatal("禁用身份不得启动执行器，但仍需防远程冒名")
	}
	hasToken, err := app.db.HasAgentToken(ctx, store.LocalAgentID)
	if err != nil || hasToken {
		t.Fatalf("内置本机不应绑定网络令牌: %v %v", hasToken, err)
	}
	source := &monitorSource{db: app.db, registry: app.registry}
	agents, err := source.Agents(ctx)
	if err != nil || len(agents) != 0 {
		t.Fatalf("禁用身份仍进入监控清单: %+v %v", agents, err)
	}
}
