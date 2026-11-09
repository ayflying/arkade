package httpx

// drift_test.go 专门测「Hub 记录 + Agent 状态」合并逻辑（toStackView）。
//
// 为什么单独成文件而不走 HTTP：drift 只有在 Agent 在线且本机哈希与 Hub 不一致时
// 才会出现，构造那条链路要起 WebSocket 连接、模拟 Agent 上报。
// 而合并逻辑本身是纯函数，直接喂结构体就能把全部分支覆盖干净——
// 这类「纯函数 + 多个分支」的用例走单元测试比走集成测试更可靠、更快。
//
// 覆盖的分支：
//   - Agent 离线（remote == nil）→ offline，不报 drift
//   - 两边哈希一致 → 不 drift
//   - 两边哈希不一致 → drift（用户要求的"偏离必须可见"）
//   - Agent 上根本没有这个项目（remote hash 为空）→ 不误报 drift

import (
	"testing"

	"arkade/hub/internal/store"
	"arkade/protocol/envelope"
)

// mkRecord 构造一条 Hub 侧记录。
func mkRecord(agentID, name, hash string) store.ComposeStack {
	rec := store.ComposeStack{
		ID: "stk-1", AgentID: agentID, Name: name,
		Content: "services: {}", ConfigHash: hash, Version: 3,
	}
	return rec
}

// TestToStackViewOffline 验证 Agent 离线时的视图。
func TestToStackViewOffline(t *testing.T) {
	rec := mkRecord("a1", "web", "hash-a")
	v := toStackView(rec, nil)

	if v.Status != composeProjectStatusOffline {
		t.Errorf("离线状态应为 %q，实际 %q", composeProjectStatusOffline, v.Status)
	}
	// 离线时不知道远端是否有文件，绝不能报 drift —— 那会让用户以为编排被篡改。
	if v.Drift {
		t.Error("Agent 离线时不应报 drift")
	}
	if v.RemoteHash != "" {
		t.Errorf("离线时远端哈希应为空，实际 %q", v.RemoteHash)
	}
	// 版本号与哈希来自 Hub，离线也必须照常返回。
	if v.Version != 3 {
		t.Errorf("版本应来自 Hub 记录（3），实际 %d", v.Version)
	}
	if v.ConfigHash != "hash-a" {
		t.Errorf("哈希应来自 Hub 记录，实际 %q", v.ConfigHash)
	}
	if v.Services == nil {
		t.Error("services 应为空数组而不是 nil")
	}
}

// TestToStackViewInSync 验证两边一致时不报 drift。
func TestToStackViewInSync(t *testing.T) {
	rec := mkRecord("a1", "web", "hash-a")
	remote := &envelope.ComposeProjectSummary{
		Name: "web", Status: "running", ConfigHash: "hash-a",
		Services: []string{"web", "cache"}, Running: 2, Total: 2,
	}
	v := toStackView(rec, remote)

	if v.Drift {
		t.Error("哈希一致时不应报 drift")
	}
	if v.Status != "running" {
		t.Errorf("状态应取 Agent 上报值，实际 %q", v.Status)
	}
	if v.Running != 2 || v.Total != 2 {
		t.Errorf("运行数/总数应取 Agent 上报值，实际 %d/%d", v.Running, v.Total)
	}
	if len(v.Services) != 2 {
		t.Errorf("服务列表应来自 Agent，实际 %v", v.Services)
	}
}

// TestToStackViewDrift 验证哈希不一致时报 drift（本轮需求的核心可见性要求）。
func TestToStackViewDrift(t *testing.T) {
	rec := mkRecord("a1", "web", "hash-a")
	// 本机文件被人手改过，哈希对不上。
	remote := &envelope.ComposeProjectSummary{
		Name: "web", Status: "running", ConfigHash: "hash-CHANGED",
		Services: []string{"web"}, Running: 1, Total: 1,
	}
	v := toStackView(rec, remote)

	if !v.Drift {
		t.Fatal("两边哈希不一致时必须报 drift，否则用户看不到偏离")
	}
	// 两个哈希都要返回：前端要同时展示"应该有"和"实际是"。
	if v.ConfigHash != "hash-a" {
		t.Errorf("Hub 哈希应为 hash-a，实际 %q", v.ConfigHash)
	}
	if v.RemoteHash != "hash-CHANGED" {
		t.Errorf("远端哈希应为 hash-CHANGED，实际 %q", v.RemoteHash)
	}
}

// TestToStackViewNotDeployed 验证 Agent 上不存在该项目时不误报 drift。
//
// 场景：Hub 里刚创建、下发失败（Agent 离线），后来 Agent 上线但本机没有该文件。
// 此时远端哈希为空，用 status 表达"没部署"，而不是报成"被篡改"。
func TestToStackViewNotDeployed(t *testing.T) {
	rec := mkRecord("a1", "web", "hash-a")
	remote := &envelope.ComposeProjectSummary{
		Name: "web", Status: "unknown", ConfigHash: "",
	}
	v := toStackView(rec, remote)

	if v.Drift {
		t.Error("远端哈希为空说明尚未部署，不应误报 drift")
	}
	if v.Status != "unknown" {
		t.Errorf("状态应取 Agent 上报值，实际 %q", v.Status)
	}
}

// TestToStackViewNilServices 验证 Agent 返回 nil 服务列表时视图给出空数组。
//
// 前端无条件遍历 services，收到 null 会崩，所以这里必须兜底成 []。
func TestToStackViewNilServices(t *testing.T) {
	rec := mkRecord("a1", "web", "hash-a")
	remote := &envelope.ComposeProjectSummary{
		Name: "web", Status: "stopped", ConfigHash: "hash-a", Services: nil,
	}
	v := toStackView(rec, remote)
	if v.Services == nil {
		t.Fatal("services 必须是空数组而不是 nil")
	}
	if len(v.Services) != 0 {
		t.Errorf("services 应为空，实际 %v", v.Services)
	}
}
