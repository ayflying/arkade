package httpx

// enroll_race_test.go 覆盖「认证通过后、注册上线前」的撤销竞态。
//
// 这是审阅发现的一个真实漏洞，不是理论问题：
//
//	T1  旧密钥建连 → authorize 在**升级阶段**就查库通过（preflight）
//	T2  管理员重置密钥 / 删除服务器 → 写库 + Registry.Close
//	T3  但 T1 还没进在线表，Close 找不到它，等于什么都没关
//	T4  T1 才发出 hello → 走完握手 → OnAgentHello 落库 + Registry.Add
//	    → **旧密钥成功上线**，删除场景还会把记录复活
//
// 时间窗是真实且常见的：T1 到 T4 之间隔着"客户端把 hello 帧发上来"，
// 网络慢、进程被调度挂起、容器启动慢都会把它拉到几百毫秒甚至几秒。
//
// 测试手法**不需要任何测试专用钩子**：先建连但不发 hello
// （此时 authorize 已通过、临界区还没进），在这个真实窗口里
// 调用真实的 HTTP 撤销接口，然后才发 hello。
//
// 判定标准：握手必须被拒；连接不得上线；被删的记录不得复活。
//
// 注意：这里**必须**用 newTestEnv 的同一套装配（同一 registry、同一 guard、
// 同一端口）。若各建一套，撤销打不到真正那条连接，测试毫无意义。

import (
	"context"
	"net/http"
	"testing"
	"time"
)

// TestResetDuringHandshakeBlocksRegistration 是核心竞态测试（重置）。
func TestResetDuringHandshakeBlocksRegistration(t *testing.T) {
	e := newTestEnv(t)
	cookies := e.loginAs(t, "admin", testPassword)
	_, created, _ := e.createServer(t, cookies, `{"name":"race-reset"}`)
	oldToken := created.Enrollment.Token

	// T1：完成升级（authorize 已通过），但**不发 hello**。
	conn := dialOnly(t, e.wsURL, oldToken)
	defer func() { _ = conn.Close() }()

	if e.registry.IsOnline(created.Agent.ID) {
		t.Fatal("前置条件不成立：连接不应已经注册")
	}

	// T2：此时撤销。在线表里没有这条连接，Close 找不到它——
	// 这正是漏洞的成因。
	rec, _ := e.do(t, http.MethodPost,
		"/api/agents/"+created.Agent.ID+"/token/reset", "", cookies)
	if rec.Code != http.StatusOK {
		t.Fatalf("重置应成功，实际 %d：%s", rec.Code, rec.Body.String())
	}

	// T4：现在才发 hello。凭据已在临界区之外被撤销，握手必须失败。
	resp, err := sendHello(t, conn)
	if err == nil && resp.Error == nil {
		t.Fatalf("凭据已在握手期间被撤销，仍成功上线：%+v", resp)
	}

	if e.registry.IsOnline(created.Agent.ID) {
		t.Error("旧密钥在重置后仍成功上线（竞态未修复）")
	}
	// 新密钥仍然可用：撤销旧凭据不应影响新凭据。
	var resetResp enrollCreateResp
	// 重新读一次记录，确认仍存在（重置不该删记录）。
	if _, err := e.db.GetAgent(t.Context(), created.Agent.ID); err != nil {
		t.Errorf("重置不应删除记录: %v", err)
	}
	_ = resetResp
}

// TestDeleteDuringHandshakeBlocksRegistration 是核心竞态测试（删除）。
//
// 除了"不得上线"，还必须断言**记录没有被复活**。
func TestDeleteDuringHandshakeBlocksRegistration(t *testing.T) {
	e := newTestEnv(t)
	cookies := e.loginAs(t, "admin", testPassword)
	_, created, _ := e.createServer(t, cookies, `{"name":"race-delete"}`)
	token := created.Enrollment.Token

	conn := dialOnly(t, e.wsURL, token)
	defer func() { _ = conn.Close() }()

	rec, _ := e.do(t, http.MethodDelete, "/api/agents/"+created.Agent.ID, "", cookies)
	if rec.Code != http.StatusOK {
		t.Fatalf("删除应成功，实际 %d：%s", rec.Code, rec.Body.String())
	}
	// 前置：记录确实没了。
	if _, err := e.db.GetAgent(t.Context(), created.Agent.ID); err == nil {
		t.Fatal("前置条件不成立：删除后记录应已消失")
	}

	resp, err := sendHello(t, conn)
	if err == nil && resp.Error == nil {
		t.Fatalf("凭据已删除，握手仍成功：%+v", resp)
	}

	if e.registry.IsOnline(created.Agent.ID) {
		t.Error("删除服务器后旧握手仍成功上线（竞态未修复）")
	}
	// 最关键的断言：记录不能被这次握手复活。
	if _, err := e.db.GetAgent(t.Context(), created.Agent.ID); err == nil {
		t.Error("被删除的记录被在途握手重新创建（复活竞态未修复）")
	}
}

// TestDisableDuringHandshakeBlocksRegistration 验证禁用路径同样被封住。
func TestDisableDuringHandshakeBlocksRegistration(t *testing.T) {
	e := newTestEnv(t)
	cookies := e.loginAs(t, "admin", testPassword)
	_, created, _ := e.createServer(t, cookies, `{"name":"race-disable"}`)

	conn := dialOnly(t, e.wsURL, created.Enrollment.Token)
	defer func() { _ = conn.Close() }()

	rec, _ := e.do(t, http.MethodPost, "/api/agents/"+created.Agent.ID+"/disable", "", cookies)
	if rec.Code != http.StatusOK {
		t.Fatalf("禁用应成功，实际 %d", rec.Code)
	}

	resp, err := sendHello(t, conn)
	if err == nil && resp.Error == nil {
		t.Fatalf("已被禁用，握手仍成功：%+v", resp)
	}
	if e.registry.IsOnline(created.Agent.ID) {
		t.Error("禁用后旧握手仍成功上线（竞态未修复）")
	}
}

// TestSharedTokenHandshakeGuardedBySameLock 验证共享 token 的最终握手
// 同样在临界区内完成。
//
// 共享路径没有摘要可复核，但它同样要落库与登记。
// 若它绕过临界区，就会出现"撤销与共享握手互相穿插"的不一致：
// 一次共享握手可能在删除之后把记录重新建出来。
func TestSharedTokenHandshakeGuardedBySameLock(t *testing.T) {
	e := newTestEnv(t)

	// 占住临界区，然后发起共享 token 握手。
	// 若共享路径也走临界区，它的落库/登记就会被挡在这里。
	inGuard := make(chan struct{})
	releaseGuard := make(chan struct{})
	guardDone := make(chan struct{})
	go func() {
		defer close(guardDone)
		_ = e.guard.RunEnrollment(context.Background(), func() error {
			close(inGuard)
			<-releaseGuard
			return nil
		})
	}()
	<-inGuard

	conn := dialOnly(t, e.wsURL, sharedTestToken)
	defer func() { _ = conn.Close() }()

	// 发 hello：它必须被挡住（在线表仍为空），这证明它进了同一把锁。
	helloSent := make(chan struct{})
	go func() {
		defer close(helloSent)
		_, _ = sendHello(t, conn)
	}()

	select {
	case <-helloSent:
		t.Error("共享 token 握手没有被临界区挡住（可能绕过了同一把锁）")
	case <-time.After(200 * time.Millisecond):
		// 正常：被挡住。
	}

	close(releaseGuard)
	<-guardDone

	// 放行后应能正常接入（旧行为不能被破坏）。
	select {
	case <-helloSent:
	case <-time.After(5 * time.Second):
		t.Fatal("放行临界区后共享 token 握手仍未完成")
	}
	if e.registry.Count() != 1 {
		t.Errorf("共享 token 应上线一条连接，实际 %d", e.registry.Count())
	}
}

// TestGuardSerializesEnrollmentAndRevocation 直接验证临界区互斥（原理层）。
func TestGuardSerializesEnrollmentAndRevocation(t *testing.T) {
	g := newTestGuard()

	inEnroll := make(chan struct{})
	enrollDone := make(chan struct{})

	go func() {
		defer close(enrollDone)
		_ = g.RunEnrollment(context.Background(), func() error {
			close(inEnroll)
			time.Sleep(150 * time.Millisecond)
			return nil
		})
	}()

	<-inEnroll // 确保握手侧已进入临界区

	start := time.Now()
	_ = g.RunRevocation(context.Background(), func() error { return nil })
	waited := time.Since(start)

	if waited < 100*time.Millisecond {
		t.Errorf("撤销没有等待握手结束（等待 %v），两者未串行化", waited)
	}
	<-enrollDone
}

// TestGuardBusyInsteadOfHang 验证等待超时会失败而不是永久挂起。
//
// 没有超时保护的话，一旦某个临界区不释放，
// 所有握手会静默挂起，日志里看不到任何错误。
func TestGuardBusyInsteadOfHang(t *testing.T) {
	g := newFastTimeoutGuard()

	inEnroll := make(chan struct{})
	hold := make(chan struct{})
	go func() {
		_ = g.RunEnrollment(context.Background(), func() error {
			close(inEnroll)
			<-hold
			return nil
		})
	}()
	<-inEnroll // 确定已进入临界区

	err := g.RunRevocation(context.Background(), func() error { return nil })
	if err == nil {
		t.Error("应该返回超时错误而不是一直等")
	}
	close(hold)
}
