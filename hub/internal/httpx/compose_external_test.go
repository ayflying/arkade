package httpx

// compose_external_test.go 覆盖 Hub 编排列表里的「服务器已有编排（未纳管）」。
//
// 真实故障：用户看到编排列表全空，而机器上其实有 5 个生产项目
// （act-runner / adinsight-db-lab / arkade-demo / lanet / portainer-agent）。
// 原因是 Agent 只枚举了自己的工作区目录，没看引擎里已有的项目。
//
// 测试方式：起一个**真 WebSocket 假 Agent**，让它应答 compose.list。
// 不 mock Ops 的原因：Hub 要把 Agent 的 JSON 应答解成
// envelope.ComposeListResult，字段映射错位（比如 external 写成 externals）
// 只有走真实序列化才能发现。
//
// 注意：假 Agent 在 server 上先设好应答数据再接受连接，
// 这样 Hub 发出的 compose.list 一定能拿到预期的内容。

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"arkade/hub/internal/agentlink"
	"arkade/protocol/envelope"
)

const composeTestAgentToken = "compose-agent-token-0123456789"

// extProject 构造一个外部项目摘要。
func extProject(name, status string, running, total int, services ...string) envelope.ComposeProjectSummary {
	if services == nil {
		services = []string{}
	}
	return envelope.ComposeProjectSummary{
		Name:     name,
		Status:   status,
		Services: services,
		Running:  running,
		Total:    total,
		Managed:  false,
	}
}

// stackResp 是编排列表响应的解析目标。
type stackResp struct {
	AgentID     string `json:"agent_id"`
	AgentOnline bool   `json:"agent_online"`
	Stacks      []struct {
		Name string `json:"name"`
	} `json:"stacks"`
	// ExternalStacks 是外部项目的完整状态列表。
	ExternalStacks []struct {
		Name     string   `json:"name"`
		Status   string   `json:"status"`
		Services []string `json:"services"`
		Running  int      `json:"running"`
		Total    int      `json:"total"`
		Managed  bool     `json:"managed"`
	} `json:"external_stacks"`
}

// startComposeAgent 起一个应答 compose.list 的假 Agent，返回 agent_id 与 URL。
//
// reply 为 nil 时该 Agent 应答空列表。
func startComposeAgent(t *testing.T, e *testEnv, reply *envelope.ComposeListResult) (string, string) {
	t.Helper()
	reg := agentlink.NewRegistry()
	log := discardLogger()
	agentSrv := agentlink.NewServer(reg, composeTestAgentToken, "test", &agentSink{db: e.db, log: log}, log)

	// ============================ 服务端 Handler 的完成信号 ============================
	//
	// WebSocket 升级后连接被 Hijack，http.Server.Shutdown 与 GF 的 Shutdown
	// 都**不会等**它结束（标准库既有行为）。测试收尾时若只 Close 客户端
	// socket 就返回，服务端 HandleConnect 可能还在跑，并在 DB 已关闭之后
	// 调用 agentSink 的 OnDisconnect/MarkOffline —— 表现为 Windows 上
	// test.db 仍被占用、TempDir RemoveAll 失败。
	//
	// 解决方式是**明确 join**：用 done channel 包一层 handler，
	// cleanup 里先关 socket 再等 Handler 真正返回，不做 sleep/retry。
	connDone := make(chan struct{})

	handler := NewRouter(Deps{
		Log:      log,
		DB:       e.db,
		Auth:     e.auth,
		Registry: reg,
		Ops:      agentlink.NewOps(reg),
		AgentConn: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer close(connDone)
			agentSrv.HandleConnect(w, r)
		}),
		HubVersion: "test",
	})
	srv := newTestServer(t, handler)

	// 拨号并握手。
	wsURL := "ws" + srv[len("http"):] + envelope.AgentConnectPath
	header := http.Header{}
	header.Set(envelope.HeaderAuthorization, envelope.BearerPrefix+composeTestAgentToken)
	header.Set(envelope.HeaderProtoVersion, "1")
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, header)
	if err != nil {
		t.Fatalf("假 Agent 建连失败: %v", err)
	}
	t.Cleanup(func() {
		// ① 先断开客户端 socket，让服务端读循环读到 EOF 后返回。
		_ = conn.Close()
		// ② 再等 HandleConnect 真正退出（服务端 DB 之后才可能被关闭）。
		//    超时视为失败而不是无声放过：join 没做到就是缺陷。
		select {
		case <-connDone:
		case <-time.After(5 * time.Second):
			t.Errorf("服务端 HandleConnect 未在 5s 内退出（数据库关闭前必须 join）")
		}
	})

	hello, err := envelope.NewRequest("hello-1", envelope.MethodHello, "agent", envelope.HelloPayload{
		AgentVersion: "0.1.0-test", ProtoVersion: 1, Hostname: "compose-host",
		OS: "linux", Arch: "amd64",
		Runtime: envelope.RuntimeInfo{Kind: envelope.RuntimeDocker, Version: "27.3.1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := conn.WriteJSON(hello); err != nil {
		t.Fatal(err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	var ack envelope.Envelope
	if err := conn.ReadJSON(&ack); err != nil {
		t.Fatalf("读取 HelloAck 失败: %v", err)
	}
	var ackPayload envelope.HelloAckPayload
	if err := envelope.DecodePayload(&ack, &ackPayload); err != nil {
		t.Fatal(err)
	}

	// 后台循环应答：只处理 compose.list，其余方法返回空结果。
	//
	// loopDone 让测试收尾时能等这个 goroutine 真正结束，
	// 避免它在 conn 关闭后仍写数据、或在 DB 关闭后还在跑。
	loopDone := make(chan struct{})
	go func() {
		defer close(loopDone)
		for {
			var req envelope.Envelope
			if err := conn.ReadJSON(&req); err != nil {
				return
			}
			var payload any
			if req.Method == envelope.MethodComposeList {
				if reply != nil {
					payload = *reply
				} else {
					payload = envelope.ComposeListResult{
						Projects: []envelope.ComposeProjectSummary{},
						External: []envelope.ComposeProjectSummary{},
					}
				}
			} else {
				payload = map[string]any{}
			}
			resp, err := envelope.NewResponse(&req, payload)
			if err != nil {
				return
			}
			_ = conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
			if err := conn.WriteJSON(resp); err != nil {
				return
			}
		}
	}()

	// 收尾时也等这个 goroutine 退出。
	// 注册顺序：t.Cleanup 后注册的先执行，所以这条要在 conn 关闭之后跑——
	// 这里用一个独立 cleanup 并显式等待关闭动作完成。
	t.Cleanup(func() {
		select {
		case <-loopDone:
		case <-time.After(5 * time.Second):
			t.Errorf("假 Agent 读循环未在 5s 内退出")
		}
	})

	// 等握手写入完成。
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if reg.IsOnline(ackPayload.AgentID) {
			return ackPayload.AgentID, srv
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("假 Agent 未出现在在线表中")
	return "", ""
}

// getStacksFrom 向指定 server 请求编排列表并解析。
func getStacksFrom(t *testing.T, srvURL, agentID, username string, e *testEnv) stackResp {
	t.Helper()
	cookies := e.loginAsFor(t, srvURL, username, testPassword)
	resp, body := doHTTP(t, http.MethodGet, srvURL+"/api/agents/"+agentID+"/stacks", "", cookies)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("编排列表应可访问，实际 %d，响应 %s", resp.StatusCode, body)
	}
	var out stackResp
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("解析失败: %v，响应 %s", err, body)
	}
	return out
}

// TestStacksShowsExternalProjects 是本次修复的核心测试。
//
// 用户原本看到空列表；修复后至少能看到「机器上有这些编排」。
func TestStacksShowsExternalProjects(t *testing.T) {
	e := newTestEnv(t)
	// 与真机一致的 5 个外部生产项目。
	agentID, srvURL := startComposeAgent(t, e, &envelope.ComposeListResult{
		Projects: []envelope.ComposeProjectSummary{},
		External: []envelope.ComposeProjectSummary{
			extProject("act-runner", "running", 2, 2, "db", "runner"),
			extProject("adinsight-db-lab", "stopped", 0, 2, "mysql", "redis"),
			extProject("arkade-demo", "running", 1, 1, "web"),
			extProject("lanet", "partial", 1, 2, "cache", "web"),
			extProject("portainer-agent", "running", 1, 1, "agent"),
		},
	})

	got := getStacksFrom(t, srvURL, agentID, "admin", e)

	if !got.AgentOnline {
		t.Fatal("Agent 应显示为在线")
	}
	if len(got.ExternalStacks) != 5 {
		t.Fatalf("应展示 5 个外部项目，实际 %d：%+v", len(got.ExternalStacks), got.ExternalStacks)
	}
	// 按名称排序稳定，前端列表不会每次刷新跳动。
	if got.ExternalStacks[0].Name != "act-runner" {
		t.Errorf("首个应为 act-runner，实际 %q", got.ExternalStacks[0].Name)
	}
	for _, s := range got.ExternalStacks {
		if s.Managed {
			t.Errorf("外部项目的 managed 必须为 false：%+v", s)
		}
		if s.Status == "" {
			t.Errorf("必须给出状态：%+v", s)
		}
	}
	// 已停止的项目也在列表里，且状态正确。
	var stopped bool
	for _, s := range got.ExternalStacks {
		if s.Name == "adinsight-db-lab" {
			stopped = true
			if s.Status != "stopped" || s.Total != 2 || s.Running != 0 {
				t.Errorf("已停止项目状态错误: %+v", s)
			}
		}
	}
	if !stopped {
		t.Error("已停止的外部项目也应展示，否则用户以为它消失了")
	}
}

// 外部项目只用丰富状态字段表达，不再维护重复的名称数组。
func TestStacksExternalUsesRichStateOnly(t *testing.T) {
	e := newTestEnv(t)
	agentID, srvURL := startComposeAgent(t, e, &envelope.ComposeListResult{
		External: []envelope.ComposeProjectSummary{extProject("external-only", "running", 1, 1, "app")},
	})

	cookies := e.loginAsFor(t, srvURL, "admin", testPassword)
	resp, body := doHTTP(t, http.MethodGet, srvURL+"/api/agents/"+agentID+"/stacks", "", cookies)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("实际 %d，响应 %s", resp.StatusCode, body)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil {
		t.Fatal(err)
	}
	if _, exists := raw["orphans"]; exists {
		t.Fatal("响应不应再包含重复的名称数组")
	}
	var rich stackResp
	if err := json.Unmarshal(body, &rich); err != nil {
		t.Fatal(err)
	}
	if len(rich.ExternalStacks) != 1 || rich.ExternalStacks[0].Name != "external-only" || rich.ExternalStacks[0].Running != 1 || len(rich.ExternalStacks[0].Services) != 1 {
		t.Fatalf("外部项目状态丢失: %+v", rich)
	}
}

// TestStacksExcludesManagedFromExternal 验证去重。
//
// 同名项目既是"已纳管"又是"未纳管"是不可能的。若两侧都出现，
// 用户会看到两条同名行，一条能编辑一条不能，无法判断该点哪个。
func TestStacksExcludesManagedFromExternal(t *testing.T) {
	e := newTestEnv(t)
	agentID, srvURL := startComposeAgent(t, e, &envelope.ComposeListResult{
		// Agent 把 web 同时报在 managed 与 external（异常但必须处理好）。
		Projects: []envelope.ComposeProjectSummary{extProject("web", "running", 1, 1, "web")},
		External: []envelope.ComposeProjectSummary{
			extProject("web", "running", 1, 1, "web"),
			extProject("other", "running", 1, 1, "app"),
		},
	})
	// Hub 里已登记 web，Agent 侧也报了 web（说明是我们管理的项目）。
	e.seedStackRecord(t, agentID, "web")

	got := getStacksFrom(t, srvURL, agentID, "admin", e)
	for _, s := range got.ExternalStacks {
		if s.Name == "web" {
			t.Error("已登记的项目不应出现在 external_stacks，否则会出现两条同名行")
		}
	}
	if len(got.ExternalStacks) != 1 || got.ExternalStacks[0].Name != "other" {
		t.Errorf("应只剩 other，实际 %+v", got.ExternalStacks)
	}
}

// TestStacksOfflineHidesExternal 验证 Agent 离线时不展示外部项目。
//
// 离线时没有实时数据，展示上一次的缓存会误导用户——他以为那是当前状态。
func TestStacksOfflineHidesExternal(t *testing.T) {
	e := newTestEnv(t)
	cookies := e.loginAs(t, "admin", testPassword)
	e.seedAgentRecord(t, "offline-1", "host-1")

	rec, _ := e.do(t, http.MethodGet, "/api/agents/offline-1/stacks", "", cookies)
	if rec.Code != http.StatusOK {
		t.Fatalf("实际 %d", rec.Code)
	}
	var got stackResp
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.AgentOnline {
		t.Error("没有活连接的 Agent 应显示为离线")
	}
	if len(got.ExternalStacks) != 0 {
		t.Errorf("离线时不应展示外部项目，实际 %+v", got.ExternalStacks)
	}
}

// TestStacksExternalHasNoEditableFields 锁定外部项目的只读性。
//
// 用类型层面堵住误操作：响应里没有 id / hash / editable，
// 前端就不可能拼出保存或删除请求。
func TestStacksExternalHasNoEditableFields(t *testing.T) {
	e := newTestEnv(t)
	agentID, srvURL := startComposeAgent(t, e, &envelope.ComposeListResult{
		External: []envelope.ComposeProjectSummary{extProject("prod-db", "running", 1, 1, "mysql")},
	})

	cookies := e.loginAsFor(t, srvURL, "admin", testPassword)
	_, body := doHTTP(t, http.MethodGet, srvURL+"/api/agents/"+agentID+"/stacks", "", cookies)
	var raw struct {
		ExternalStacks []map[string]any `json:"external_stacks"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		t.Fatal(err)
	}
	if len(raw.ExternalStacks) != 1 {
		t.Fatalf("应有 1 项，实际 %d，响应 %s", len(raw.ExternalStacks), body)
	}
	item := raw.ExternalStacks[0]
	for _, forbidden := range []string{"id", "hash", "config_hash", "editable", "content"} {
		if _, ok := item[forbidden]; ok {
			t.Errorf("外部项目不应包含可编辑字段 %q（否则前端会渲染出伪入口）", forbidden)
		}
	}
	// 必须显式给出 managed=false，而不是靠字段缺失推断。
	if v, ok := item["managed"]; !ok || v != false {
		t.Errorf("必须显式给出 managed=false，实际 %v（存在=%v）", v, ok)
	}
}
