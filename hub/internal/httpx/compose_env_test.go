package httpx

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// .env即使Agent离线也应保存，旧客户端省略不能意外清掉，过期版本不能覆盖。
func TestComposeEnvLifecycleOffline(t *testing.T) {
	e := newTestEnv(t)
	cookies := e.login(t)
	e.seedAgent(t, "env-agent")
	base := "/api/agents/env-agent/stacks"
	envText := "PASSWORD=do-not-log-test-secret\nPORT=18081\n"
	payload, _ := json.Marshal(map[string]any{"name": "env-test", "content": sampleCompose, "env_content": envText})
	rec, _ := e.do(t, http.MethodPost, base, string(payload), cookies)
	if rec.Code != http.StatusOK {
		t.Fatalf("创建失败: %d", rec.Code)
	}
	saved, err := e.db.GetComposeStack(t.Context(), "env-agent", "env-test")
	if err != nil || saved.EnvContent != envText || saved.ConfigHash != composeProjectHash(sampleCompose, envText) {
		t.Fatal("环境文件或联合哈希未保存")
	}
	rec, _ = e.do(t, http.MethodGet, base, "", cookies)
	if strings.Contains(rec.Body.String(), "do-not-log-test-secret") || strings.Contains(rec.Body.String(), "env_content") {
		t.Fatal("列表泄露环境配置")
	}
	rec, _ = e.do(t, http.MethodGet, base+"/env-test/config", "", cookies)
	var cfg struct {
		EnvContent string `json:"env_content"`
		Version    int64  `json:"version"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.EnvContent != envText || !strings.Contains(rec.Header().Get("Cache-Control"), "no-store") {
		t.Fatal("配置读取不匹配或可缓存")
	}
	update, _ := json.Marshal(map[string]any{"content": sampleCompose + "\n# updated", "version": cfg.Version})
	rec, _ = e.do(t, http.MethodPut, base+"/env-test", string(update), cookies)
	if rec.Code != http.StatusOK {
		t.Fatalf("旧客户端更新失败: %d", rec.Code)
	}
	saved, _ = e.db.GetComposeStack(t.Context(), "env-agent", "env-test")
	if saved.EnvContent != envText {
		t.Fatal("省略env_content误清环境文件")
	}
	stale, _ := json.Marshal(map[string]any{"content": sampleCompose, "env_content": "CHANGED=yes", "version": cfg.Version})
	rec, _ = e.do(t, http.MethodPut, base+"/env-test", string(stale), cookies)
	if rec.Code != http.StatusConflict {
		t.Fatal("过期版本未被拒绝")
	}
	clear, _ := json.Marshal(map[string]any{"content": sampleCompose, "env_content": "", "version": saved.Version})
	rec, _ = e.do(t, http.MethodPut, base+"/env-test", string(clear), cookies)
	if rec.Code != http.StatusOK {
		t.Fatalf("清空env失败: %d", rec.Code)
	}
	saved, _ = e.db.GetComposeStack(t.Context(), "env-agent", "env-test")
	if saved.EnvContent != "" || saved.ConfigHash != composeConfigHash(sampleCompose) {
		t.Fatal("显式空串未清空或空env项目哈希不符")
	}
}

func TestComposeConfigRequiresManage(t *testing.T) {
	e := newTestEnv(t)
	admin := e.login(t)
	e.seedAgent(t, "env-permission")
	e.seedStack(t, "env-permission", "web", sampleCompose)
	uid := e.createUser(t, admin, "configviewer", "ViewerPass123", "user")
	group := e.createGroup(t, admin, "config-readers")
	for _, req := range []struct{ path, body string }{
		{"/api/groups/" + group.ID + "/members", `{"user_ids":["` + uid + `"]}`},
		{"/api/groups/" + group.ID + "/grants", `{"grants":[{"agent_id":"env-permission","permission":"view"}]}`},
	} {
		rec, _ := e.do(t, http.MethodPut, req.path, req.body, admin)
		if rec.Code != 200 {
			t.Fatal("设置测试权限失败")
		}
	}
	viewer := e.loginAs(t, "configviewer", "ViewerPass123")
	rec, _ := e.do(t, http.MethodGet, "/api/agents/env-permission/stacks/web", "", viewer)
	if rec.Code != 200 || strings.Contains(rec.Body.String(), "content") {
		t.Fatal("查看者应能读取运行元数据但不能读配置")
	}
	rec, _ = e.do(t, http.MethodGet, "/api/agents/env-permission/stacks/web/config", "", viewer)
	if rec.Code != 403 {
		t.Fatal("非管理者不能读取包含.env的配置")
	}
}

func TestComposeProjectHashBoundaries(t *testing.T) {
	if composeProjectHash("ab", "c") == composeProjectHash("a", "bc") {
		t.Fatal("文件边界未参与哈希")
	}
	if composeProjectHash(sampleCompose, "") != composeConfigHash(sampleCompose) {
		t.Fatal("空env项目哈希不一致")
	}
}
