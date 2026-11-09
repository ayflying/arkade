package link

// session_close_test.go 覆盖 Hub 主动关闭连接通知的解析。
//
// 背景：Hub 在禁用服务器时会先发一条 session.close 事件说明原因，
// 再断开连接。Agent 必须能把原因读出来并写进日志——
// 否则运维只能观察到"连不上、反复重连"，真正的原因（被禁用）
// 永远只存在于 Hub 的数据库里，现场人员无从判断。
//
// 端到端行为（Hub 真的发出、Agent 真的收到）由 hub 侧
// internal/httpx 的真实 WebSocket 测试覆盖；这里只验证解析这一环。

import (
	"testing"

	"arkade/protocol/envelope"
)

// TestCloseReasonParsed 验证能取出原因文案。
func TestCloseReasonParsed(t *testing.T) {
	env, err := envelope.NewEvent("close-1", methodSessionClose, "hub",
		map[string]string{"reason": "该服务器已被管理员禁用，连接已断开"})
	if err != nil {
		t.Fatal(err)
	}
	got := closeReason(env)
	if got != "该服务器已被管理员禁用，连接已断开" {
		t.Errorf("原因解析错误，实际 %q", got)
	}
}

// TestCloseReasonMissing 验证载荷缺 reason 时给兜底说明。
//
// 不能返回空串：日志里出现空的 reason 会让人以为是日志系统丢字段，
// 而实际是载荷格式问题——两者必须能区分。
func TestCloseReasonMissing(t *testing.T) {
	env, err := envelope.NewEvent("close-2", methodSessionClose, "hub", map[string]string{})
	if err != nil {
		t.Fatal(err)
	}
	got := closeReason(env)
	if got == "" {
		t.Error("缺 reason 时应给兜底说明，不能返回空串")
	}
	if got == "Hub 未提供关闭原因" {
		return // 期望值
	}
	t.Errorf("兜底文案不符，实际 %q", got)
}

// TestCloseReasonMalformedPayload 验证载荷格式非法时同样给兜底说明。
func TestCloseReasonMalformedPayload(t *testing.T) {
	// 直接构造一个载荷不是对象的信封。
	env := &envelope.Envelope{
		V:       envelope.Version,
		Type:    envelope.TypeEvent,
		ID:      "close-3",
		Method:  methodSessionClose,
		Target:  "hub",
		Payload: []byte(`"just a string"`),
	}
	got := closeReason(env)
	if got == "" {
		t.Error("载荷非法时应给兜底文案，不能返回空串")
	}
}

// TestSessionCloseMethodName 锁定方法名字符串。
//
// Hub 与 Agent 各自定义了这个常量（不放进 protocol 包），
// 两边必须一致，否则通知会退化成"直接断开"——功能静默失效。
// 这个测试是防止有人单方面改名而另一边没跟上。
func TestSessionCloseMethodName(t *testing.T) {
	if methodSessionClose != "session.close" {
		t.Errorf("方法名必须与 Hub 侧一致为 %q，实际 %q", "session.close", methodSessionClose)
	}
}
