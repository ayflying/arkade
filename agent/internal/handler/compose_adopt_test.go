package handler

// compose_adopt_test.go 覆盖 compose.preview / compose.adopt 的对外行为。
//
// 这里放的都是"用户能在界面上看到什么"的断言：
// can_import 的取值、blockers 的文案、adopt 之后磁盘上有什么、以及**没有什么**。
// 安全扫描的细节由 compose 包自己的测试覆盖，这里只验证接线正确。

import (
	"encoding/json"
	"strings"
	"testing"

	"arkade/agent/internal/compose"
	"arkade/protocol/envelope"
)

// adoptTestPayload 是正例：一个干净的、可导入的编排。
const adoptTestPayload = `services:
  web:
    image: nginx:1.25
`

// TestComposePreviewRegistered 验证两个新方法都已注册。
func TestComposePreviewRegistered(t *testing.T) {
	reg, _ := newComposeRegistry(t)
	for _, m := range []string{envelope.MethodComposePreview, envelope.MethodComposeAdopt} {
		if !reg.Has(m) {
			t.Errorf("方法 %s 未注册（Hub 会收到 unsupported）", m)
		}
	}
}

// TestComposePreviewRequiresName 验证项目名校验。
func TestComposePreviewRequiresName(t *testing.T) {
	reg, _ := newComposeRegistry(t)
	for _, name := range []string{"", "Bad", "../x", "-x"} {
		resp := request(t, reg, envelope.MethodComposePreview,
			envelope.ComposePreviewPayload{Name: name, Content: adoptTestPayload})
		if resp.Error == nil {
			t.Errorf("项目名 %q 应被拒绝", name)
		}
	}
}

// TestComposePreviewWithoutContentExplains 验证没提供内容时如实说明原因。
//
// 这一条是"不能假成功"的核心：用户还没粘贴内容时，
// 结论必须是 can_import=false 且原因明确，而不是报错或猜。
func TestComposePreviewWithoutContentExplains(t *testing.T) {
	reg, _ := newComposeRegistry(t)
	resp := request(t, reg, envelope.MethodComposePreview,
		envelope.ComposePreviewPayload{Name: "someapp"})
	if resp.Error != nil {
		t.Fatalf("没有内容不应是参数错误（应作为预检结论给出）: %v", resp.Error.Message)
	}
	out := decodePayloadMap(t, resp)
	canImport, _ := out["can_import"].(bool)
	if canImport {
		t.Error("没有内容时不能声称可以导入")
	}
	blockers, _ := out["blockers"].([]any)
	if len(blockers) == 0 {
		t.Fatal("应至少给出一条阻塞原因")
	}
	joined := stringify(blockers)
	if !strings.Contains(joined, "提供") {
		t.Errorf("阻塞原因应说明需要用户提供内容，实际 %v", blockers)
	}
}

// TestComposePreviewRejectsRelativeEnvFile 验证真机验收用例：
// 相对 env_file 必须阻塞，且文案指出字段与行号。
func TestComposePreviewRejectsRelativeEnvFile(t *testing.T) {
	reg, _ := newComposeRegistry(t)
	content := "services:\n  app:\n    image: alpine:3.19\n    env_file: ./app.env\n"

	resp := request(t, reg, envelope.MethodComposePreview,
		envelope.ComposePreviewPayload{Name: "importcheck", Content: content})
	if resp.Error != nil {
		t.Fatalf("预检不应报错: %v", resp.Error.Message)
	}
	out := decodePayloadMap(t, resp)
	if out["can_import"].(bool) {
		t.Error("相对 env_file 必须阻塞导入")
	}
	joined := stringify(out["blockers"].([]any))
	if !strings.Contains(joined, "env_file") {
		t.Errorf("阻塞原因应指出 env_file，实际 %q", joined)
	}
	if !strings.Contains(joined, "第 4 行") {
		t.Errorf("阻塞原因应给出行号，实际 %q", joined)
	}
}

// TestComposeAdoptRequiresConfirmation 验证未确认时拒绝。
//
// 用布尔而不是"省略即默认"：让"误调接口"不可能完成导入。
func TestComposeAdoptRequiresConfirmation(t *testing.T) {
	reg, _ := newComposeRegistry(t)
	resp := request(t, reg, envelope.MethodComposeAdopt, envelope.ComposeAdoptPayload{
		Name: "noconfirm", Content: adoptTestPayload,
		ExpectedContentHash: compose.Hash(adoptTestPayload),
		Confirmation:        false,
	})
	if resp.Error == nil {
		t.Fatal("未确认时应拒绝")
	}
	if !strings.Contains(resp.Error.Message, "确认") {
		t.Errorf("文案应说明需要确认，实际 %q", resp.Error.Message)
	}
}

// TestComposeAdoptRejectsHashMismatch 验证哈希不符时拒绝且不写盘。
func TestComposeAdoptRejectsHashMismatch(t *testing.T) {
	reg, mgr := newComposeRegistry(t)
	resp := request(t, reg, envelope.MethodComposeAdopt, envelope.ComposeAdoptPayload{
		Name: "hashbad", Content: adoptTestPayload,
		ExpectedContentHash: "0000000000000000",
		Confirmation:        true,
	})
	if resp.Error == nil {
		t.Fatal("哈希不符应拒绝")
	}
	if mgr.Workspace().Exists("hashbad") {
		t.Error("哈希不符时不得写入管理区")
	}
}

// TestComposeAdoptRejectsUnsafeContent 验证含相对路径的内容被拒且不写盘。
func TestComposeAdoptRejectsUnsafeContent(t *testing.T) {
	reg, mgr := newComposeRegistry(t)
	content := "services:\n  app:\n    image: alpine\n    env_file: ./app.env\n"
	resp := request(t, reg, envelope.MethodComposeAdopt, envelope.ComposeAdoptPayload{
		Name: "unsafeimport", Content: content,
		ExpectedContentHash: compose.Hash(content),
		Confirmation:        true,
	})
	if resp.Error == nil {
		t.Fatal("含相对 env_file 的内容应拒绝导入")
	}
	if mgr.Workspace().Exists("unsafeimport") {
		t.Error("不安全的内容不得写入管理区")
	}
}

// TestComposeAdoptWithoutExternalProject 验证没有外部同名项目时拒绝。
//
// 这是安全边界：导入的对象必须是"引擎里真实存在的外部项目"。
// 允许凭空"导入"一个不存在的项目，等价于绕过同名闸门做接管。
func TestComposeAdoptWithoutExternalProject(t *testing.T) {
	reg, mgr := newComposeRegistry(t)
	resp := request(t, reg, envelope.MethodComposeAdopt, envelope.ComposeAdoptPayload{
		Name: "ghost", Content: adoptTestPayload,
		ExpectedContentHash: compose.Hash(adoptTestPayload),
		Confirmation:        true,
	})
	if resp.Error == nil {
		t.Fatal("引擎里没有该项目时应拒绝导入")
	}
	if mgr.Workspace().Exists("ghost") {
		t.Error("拒绝时不得写入管理区")
	}
}

// TestComposeAdoptNilPayload 验证空载荷被拒（不 panic）。
func TestComposeAdoptNilPayload(t *testing.T) {
	reg, _ := newComposeRegistry(t)
	for _, m := range []string{envelope.MethodComposePreview, envelope.MethodComposeAdopt} {
		resp := request(t, reg, m, nil)
		if resp.Error == nil {
			t.Errorf("%s 空载荷应报错", m)
		}
	}
}

// decodePayloadMap 把响应的原始载荷解成 map，便于断言字段。
func decodePayloadMap(t *testing.T, resp *envelope.Envelope) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(resp.Payload, &out); err != nil {
		t.Fatalf("载荷解码失败: %v（原始 %s）", err, string(resp.Payload))
	}
	return out
}

// stringify 把 JSON 数组转成一个字符串，便于断言文案。
func stringify(v any) string {
	arr, ok := v.([]any)
	if !ok {
		return ""
	}
	parts := make([]string, 0, len(arr))
	for _, item := range arr {
		if s, ok := item.(string); ok {
			parts = append(parts, s)
		}
	}
	return strings.Join(parts, "；")
}
