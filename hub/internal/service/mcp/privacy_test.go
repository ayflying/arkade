package mcp

import (
	"net/http"
	"strings"
	"testing"

	protocol "github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestMCPPrivacyContracts(t *testing.T) {
	for _, tool := range Catalog() {
		if publicFields(tool.Name) == nil {
			t.Errorf("%s: missing public response contract", tool.Name)
		}
		switch tool.Name {
		case "arkade_get_container_logs", "arkade_get_stack_logs":
			t.Errorf("raw log tool exposed: %s", tool.Name)
		}
	}
}

func TestMCPPrivacyResponses(t *testing.T) {
	for _, tc := range []struct {
		tool, input, want string
		status            int
		failed            bool
	}{
		{"arkade_get_stack_config", `{"version":9007199254740993,"content":"PRIVATE","env_content":"PRIVATE"}`, `{"version":9007199254740993}`, 200, false},
		{"arkade_get_external_stack_config", `{"readable":true,"config_hash":"0123456789abcdef","content":"PRIVATE","draft":{"version":1,"content":"PRIVATE"}}`, `{"config_hash":"0123456789abcdef","draft":{"version":1},"readable":true}`, 200, false},
		{"arkade_save_external_stack_draft", `{"draft":{"version":2,"content":"PRIVATE"}}`, `{"draft":{"version":2}}`, 200, false},
		{"arkade_update_stack", `{"sync_ok":false,"sync_hint":"PRIVATE","stack":{"name":"demo","version":2,"env_content":"PRIVATE"}}`, `{"stack":{"name":"demo","version":2},"sync_ok":false}`, 200, true},
		{"arkade_get_stack_operation", `{"task":{"status":"failed","error":"PRIVATE","command":"PRIVATE","output":[{"text":"PRIVATE"}],"task_id":"demo-123","migrate":{"residual":{"uncertain":true,"extra":"PRIVATE"}}}}`, `{"task":{"migrate":{"residual":{"uncertain":true}},"status":"failed","task_id":"demo-123"}}`, 200, false},
		{"arkade_inspect_container", `{"container":{"id":"demo","command":"PRIVATE","env":["PRIVATE"],"networks":[{"name":"internal","address":"PRIVATE"}],"mounts":[{"type":"volume","source":"PRIVATE"}]}}`, `{"container":{"id":"demo","mounts":[{"type":"volume"}],"networks":[{"name":"internal"}]}}`, 200, false},
		{"arkade_read_volume_file", `{"result":{"size":5,"sha256":"test-hash","data_base64":"PRIVATE"}}`, `{"result":{"sha256":"test-hash","size":5}}`, 200, false},
		{"arkade_list_images", `{"images":[{"id":"sha256:demo","tags":["demo:latest",{"unexpected":"PRIVATE"}]}]}`, `{"images":[{"id":"sha256:demo","tags":["demo:latest"]}]}`, 200, false},
		{"arkade_list_agents", `{"agents":[{"id":"a","name":{"unexpected":"PRIVATE"},"direct_address":"PRIVATE"}]}`, `{"agents":[{"id":"a"}]}`, 200, false},
		{"arkade_get_stack_config", `{"version":{"unexpected":"PRIVATE"}}`, `{}`, 200, false},
		{"arkade_get_stack_config", `{"version":1} PRIVATE`, "Hub 未返回预期的 JSON 操作结果", 200, true},
		{"arkade_get_stack_config", `{"error":{"code":"PRIVATE","message":"PRIVATE"}}`, `"code":"forbidden"`, 403, true},
		{"arkade_delete_image", "", "操作完成", 204, false},
		{"arkade_unclassified", `{"secret":"PRIVATE"}`, "没有可公开的响应定义", 200, true},
	} {
		t.Run(tc.tool, func(t *testing.T) {
			out := &response{header: make(http.Header), status: tc.status}
			out.body.WriteString(tc.input)
			result := safeToolResponse(out, publicFieldPolicy(publicFields(tc.tool)))
			text := result.Content[0].(*protocol.TextContent).Text
			if result.IsError != tc.failed || !strings.Contains(text, tc.want) || strings.Contains(text, "PRIVATE") {
				t.Fatalf("unexpected result: error=%t text=%q", result.IsError, text)
			}
		})
	}
}
