package httpx

// enroll_deny_test.go 覆盖「谁能连、谁不能连」的拒绝矩阵。
//
// 与 enroll_test.go 分开的原因：这个文件里的每一条都在测**拒绝**，
// 拒绝理由的文案本身是产品的一部分（Agent 日志里会显示给运维），
// 所以这里既断言"被拒"，也断言"理由说得对"。

import (
	"net/http"
	"strings"
	"testing"

	"arkade/hub/internal/store"
)

// TestSharedTokenCannotClaimEnrolled 是最关键的越权测试。
//
// 场景：攻击者持有共享 token，在 Header 里写
// X-Arkade-Agent-Id: <某台专属服务器的 id>，试图顶替它上线。
// 必须被拒绝，否则专属密钥带来的隔离形同虚设。
func TestSharedTokenCannotClaimEnrolled(t *testing.T) {
	e := newTestEnv(t)
	cookies := e.loginAs(t, "admin", testPassword)
	_, created, _ := e.createServer(t, cookies, `{"name":"exclusive"}`)

	// 共享 token + 专属服务器的 ID，并声称同样的主机名。
	wsURL := "ws" + e.wsURL[len("http"):] + "/agent/connect"
	_ = wsURL
	resp, err := dialHelloWithHeaders(t, e.wsURL, sharedTestToken, created.Agent.ID)
	if err == nil && resp.Error == nil {
		t.Fatalf("共享 token 不应能冒充专属服务器上线，实际 %+v", resp)
	}
	// 理由必须能让人明白该怎么做。
	if err == nil && resp.Error != nil && !strings.Contains(resp.Error.Message, "专属") {
		t.Errorf("拒绝原因应说明必须使用专属密钥，实际 %q", resp.Error.Message)
	}

	if e.registry.IsOnline(created.Agent.ID) {
		t.Error("冒充者不应出现在在线表里")
	}
}

// TestSharedTokenStillWorksForLegacy 验证共享 token 的旧行为未被破坏。
//
// 没有专属凭据的新机器，仍应能用共享 token 正常接入。
// 这条挂了说明改动把既有部署弄坏了——那是不可接受的回归。
func TestSharedTokenStillWorksForLegacy(t *testing.T) {
	e := newTestEnv(t)

	conn, ack := dialWith(t, e.wsURL, sharedTestToken, "", "legacy-host")
	defer func() { _ = conn.Close() }()
	if ack.AgentID == "" {
		t.Fatal("共享 token 的旧路径应仍能接入并拿到 ID")
	}
	if e.registry.Count() != 1 {
		t.Fatalf("共享 token 的旧路径应仍能接入，实际在线 %d", e.registry.Count())
	}
}

// TestEnrolledTokenCannotClaimOtherEnrolled 验证专属密钥不能冒领另一条记录。
//
// 即使两台机器各有专属密钥，A 的密钥也不能上线成 B——
// 身份只看摘要绑定，Header 里的任何 ID 都不参与决策。
func TestEnrolledTokenCannotClaimOtherEnrolled(t *testing.T) {
	e := newTestEnv(t)
	cookies := e.loginAs(t, "admin", testPassword)
	_, a, _ := e.createServer(t, cookies, `{"name":"machine-a"}`)
	_, b, _ := e.createServer(t, cookies, `{"name":"machine-b"}`)

	// 用 A 的密钥，Header 里写 B 的 ID。
	conn, ack := dialWith(t, e.wsURL, a.Enrollment.Token, b.Agent.ID, "host-a")
	defer func() { _ = conn.Close() }()

	if ack.AgentID != a.Agent.ID {
		t.Errorf("A 的密钥应绑定 A 的记录 %q，实际 %q", a.Agent.ID, ack.AgentID)
	}
	if e.registry.IsOnline(b.Agent.ID) {
		t.Error("A 的连接不应让 B 变成在线")
	}
	if !e.registry.IsOnline(a.Agent.ID) {
		t.Error("A 应正常在线")
	}
}

// TestSharedTokenCannotClaimPreprovisioned 验证共享 token 不能占用预建记录。
//
// 预建记录是"已经分给某台机器、但还没部署"的。若共享 token 能占用它，
// 任何人拿到共享 token 就能先一步把预建身份抢走，
// 真正的机器再接入时就会变成"自己顶替自己"的混乱。
func TestSharedTokenCannotClaimPreprovisioned(t *testing.T) {
	e := newTestEnv(t)
	cookies := e.loginAs(t, "admin", testPassword)
	_, created, _ := e.createServer(t, cookies, `{"name":"reserved"}`)

	resp, err := dialHelloWithHeaders(t, e.wsURL, sharedTestToken, created.Agent.ID)
	if err == nil && resp.Error == nil {
		t.Fatalf("共享 token 不应能占用预建记录，实际 %+v", resp)
	}
	if e.registry.Count() != 0 {
		t.Errorf("不应有任何连接上线，实际 %d", e.registry.Count())
	}
	// 记录必须保持 pending，不能被这次尝试改成别的状态。
	rec, gerr := e.db.GetAgent(t.Context(), created.Agent.ID)
	if gerr != nil {
		t.Fatalf("记录不应被删除: %v", gerr)
	}
	if rec.Status != store.StatusPending {
		t.Errorf("记录应仍为 pending，实际 %q", rec.Status)
	}
}

// TestDisabledRejectsAllModes 验证禁用对两种凭据都生效。
//
// 只看一种会留下缺口：禁用一台用专属密钥的机器后，
// 若专属路径漏检 disabled，它照样能连。
func TestDisabledRejectsAllModes(t *testing.T) {
	e := newTestEnv(t)
	cookies := e.loginAs(t, "admin", testPassword)
	_, created, _ := e.createServer(t, cookies, `{"name":"dis-both"}`)

	rec, _ := e.do(t, http.MethodPost, "/api/agents/"+created.Agent.ID+"/disable", "", cookies)
	if rec.Code != http.StatusOK {
		t.Fatalf("禁用应成功，实际 %d", rec.Code)
	}

	// 专属密钥被拒。
	resp, err := dialHelloWithHeaders(t, e.wsURL, created.Enrollment.Token, "")
	if err == nil && resp.Error == nil {
		t.Errorf("被禁用后专属密钥仍能接入：%+v", resp)
	}

	// 共享 token 认领同一个 ID 也被拒（本来就该拒，双重保险）。
	resp2, err2 := dialHelloWithHeaders(t, e.wsURL, sharedTestToken, created.Agent.ID)
	if err2 == nil && resp2.Error == nil {
		t.Errorf("被禁用后共享 token 仍能认领：%+v", resp2)
	}

	if e.registry.Count() != 0 {
		t.Errorf("被禁用的服务器不应上线，实际在线 %d", e.registry.Count())
	}
}
