package handler

// compose_source_test.go：compose.source 处理器的行为测试。
//
// 重点不是"能读到"，而是"读到的东西出去之前被正确收窄"：
// 有阻塞项时不回传内容、文案不含路径、错误码可判定。

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"arkade/agent/internal/runtime"
	"arkade/protocol/envelope"
)

// composeSourceFake 是实现了配置读取能力的替身。
type composeSourceFake struct {
	fakeRuntime
	content string
	err     error
}

func (f *composeSourceFake) RunComposeSource(context.Context, string) (string, error) {
	if f.err != nil {
		return "", f.err
	}
	return f.content, nil
}

// runSource 用给定项目名调用处理器。
func runSource(t *testing.T, rt runtime.Runtime, name string) *envelope.Envelope {
	t.Helper()
	reg := NewRegistry()
	reg.Register(envelope.MethodComposeSource, newComposeSourceHandler(rt))
	raw, _ := json.Marshal(envelope.ComposeSourcePayload{Name: name})
	return reg.Dispatch(context.Background(), &envelope.Envelope{
		V: envelope.Version, ID: "t", Type: envelope.TypeRequest,
		Method: envelope.MethodComposeSource, Payload: raw,
	})
}

// TestComposeSourceReturnsContentAndHash 验证正常配置回传内容与哈希。
func TestComposeSourceReturnsContentAndHash(t *testing.T) {
	const cfg = "services:\n  web:\n    image: nginx:alpine\n"
	resp := runSource(t, &composeSourceFake{content: cfg}, "demo")
	if resp.Error != nil {
		t.Fatalf("不应失败: %+v", resp.Error)
	}
	var out envelope.ComposeSourceResult
	if err := envelope.DecodePayload(resp, &out); err != nil {
		t.Fatal(err)
	}
	if out.Content != cfg {
		t.Fatalf("内容不符: %q", out.Content)
	}
	if out.ConfigHash == "" {
		t.Fatal("应返回哈希")
	}
	if len(out.Blockers) != 0 {
		t.Fatalf("不应有阻塞: %v", out.Blockers)
	}
}

// TestComposeSourceBlockedOmitsContent 验证有阻塞项时**不回传内容**。
//
// 理由：这份配置本来就不能接管，回传原文只会让敏感内容在 Hub 多存一份。
func TestComposeSourceBlockedOmitsContent(t *testing.T) {
	const cfg = "services:\n  web:\n    image: nginx\n    env_file: ./app.env\n"
	resp := runSource(t, &composeSourceFake{content: cfg}, "demo")
	if resp.Error != nil {
		t.Fatalf("不应失败: %+v", resp.Error)
	}
	var out envelope.ComposeSourceResult
	if err := envelope.DecodePayload(resp, &out); err != nil {
		t.Fatal(err)
	}
	if out.Content != "" {
		t.Fatalf("有阻塞时不应回传内容，实际回传 %d 字节", len(out.Content))
	}
	if len(out.Blockers) == 0 {
		t.Fatal("应给出阻塞原因")
	}
	// 阻塞文案不得包含路径原文。
	for _, b := range out.Blockers {
		if strings.Contains(b, "app.env") {
			t.Fatalf("阻塞文案泄露了路径: %q", b)
		}
	}
	if out.ConfigHash == "" {
		t.Fatal("有阻塞时仍应给出哈希，便于 Hub 判定是否同一份")
	}
}

// TestComposeSourceUnsafePathMapsToConflict 验证路径被拒映射为 conflict。
func TestComposeSourceUnsafePathMapsToConflict(t *testing.T) {
	resp := runSource(t, &composeSourceFake{err: runtime.ErrComposeSourceUnsafePath}, "demo")
	if resp.Error == nil || resp.Error.Code != envelope.CodeConflict {
		t.Fatalf("期望 conflict，实际 %+v", resp.Error)
	}
}

// TestComposeSourceRejectsBadName 验证非法项目名被拒。
func TestComposeSourceRejectsBadName(t *testing.T) {
	resp := runSource(t, &composeSourceFake{content: "x"}, "../../etc")
	if resp.Error == nil || resp.Error.Code != envelope.CodeBadRequest {
		t.Fatalf("期望 bad_request，实际 %+v", resp.Error)
	}
}

// TestComposeSourceBlockerShownToHub 验证预期的"不能接管"以 Blockers 返回，
// 而不是变成通用失败——这是 Hub 能显示具体原因的唯一途径。
func TestComposeSourceBlockerShownToHub(t *testing.T) {
	reason := "该项目使用了多个配置文件（2 个），首版仅支持单配置文件的项目"
	resp := runSource(t, &composeSourceFake{
		err: &runtime.SourceBlockerError{Reason: reason},
	}, "demo")
	if resp.Error != nil {
		t.Fatalf("blocker 不应作为协议错误返回: %+v", resp.Error)
	}
	var out envelope.ComposeSourceResult
	if err := envelope.DecodePayload(resp, &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Blockers) != 1 || out.Blockers[0] != reason {
		t.Fatalf("应原样返回具体原因，实际: %v", out.Blockers)
	}
	if out.Content != "" {
		t.Fatal("有 blocker 时不应回传内容")
	}
}

// TestComposeSourceRealFaultStillErrors 验证真正的引擎故障仍是协议错误。
func TestComposeSourceRealFaultStillErrors(t *testing.T) {
	for _, tc := range []struct {
		err  error
		code string
	}{
		{runtime.ErrHelperBusy, envelope.CodeAgentBusy},
		{runtime.ErrHelperImageMissing, envelope.CodeUnsupported},
		{context.DeadlineExceeded, envelope.CodeTimeout},
	} {
		resp := runSource(t, &composeSourceFake{err: tc.err}, "demo")
		if resp.Error == nil {
			t.Fatalf("%v 应作为协议错误返回", tc.err)
		}
		if resp.Error.Code != tc.code {
			t.Fatalf("%v 期望 %s，实际 %s", tc.err, tc.code, resp.Error.Code)
		}
	}
}

// TestComposeSourceUnsupportedEngine 验证引擎不支持时返回 unsupported。
func TestComposeSourceUnsupportedEngine(t *testing.T) {
	resp := runSource(t, &fakeRuntime{}, "demo")
	if resp.Error == nil || resp.Error.Code != envelope.CodeUnsupported {
		t.Fatalf("期望 unsupported，实际 %+v", resp.Error)
	}
}
