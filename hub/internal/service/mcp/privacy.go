package mcp

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"

	protocol "github.com/modelcontextprotocol/go-sdk/mcp"
)

// Each tool has an explicit response contract independent of the Web API.
// Never copy a whole object at a leaf: future API fields are private by default.
type publicFieldTree map[string]publicFieldTree

func publicFields(tool string) []string {
	if fields := stackPublicFields(tool); fields != nil {
		return fields
	}
	return resourcePublicFields(tool)
}

func publicFieldPolicy(fields []string) publicFieldTree {
	if fields == nil {
		return nil
	}
	root := make(publicFieldTree)
	for _, field := range fields {
		node := root
		for _, name := range strings.Split(field, ".") {
			if node[name] == nil {
				node[name] = make(publicFieldTree)
			}
			node = node[name]
		}
	}
	return root
}

func projectPublicValue(value any, policy publicFieldTree) (any, bool) {
	switch raw := value.(type) {
	case map[string]any:
		if len(policy) == 0 {
			return nil, false
		}
		result := make(map[string]any)
		for key, field := range policy {
			if v, present := raw[key]; present {
				if projected, ok := projectPublicValue(v, field); ok {
					result[key] = projected
				}
			}
		}
		return result, true
	case []any:
		if _, stringList := policy["[]"]; stringList {
			result := make([]string, 0, len(raw))
			for _, item := range raw {
				if text, ok := item.(string); ok {
					result = append(result, text)
				}
			}
			return result, true
		}
		if len(policy) == 0 {
			return nil, false
		}
		result := make([]any, 0, len(raw))
		for _, item := range raw {
			if projected, ok := projectPublicValue(item, policy); ok {
				result = append(result, projected)
			}
		}
		return result, true
	case nil, string, bool, json.Number:
		return raw, len(policy) == 0 || raw == nil
	default:
		return nil, false
	}
}

func safeToolResponse(out *response, policy publicFieldTree, tool string) *protocol.CallToolResult {
	if out.status < 200 || out.status >= 300 {
		// Hub/Agent errors may echo config, stderr, credentials or user input.
		code := "operation_failed"
		switch out.status {
		case http.StatusBadRequest, http.StatusUnprocessableEntity:
			code = "bad_request"
		case http.StatusUnauthorized:
			code = "unauthorized"
		case http.StatusForbidden:
			code = "forbidden"
		case http.StatusNotFound:
			code = "not_found"
		case http.StatusConflict:
			code = "conflict"
		}
		return toolResult(`{"error":{"code":"`+code+`","message":"操作失败；如需详细原因，请在 Arkade 网页查看"}}`, true)
	}
	if out.overflow {
		return toolResult("接口响应超过 MCP 大小限制；若这是写操作，请查询资源或任务状态，不要盲目重试", true)
	}
	if policy == nil {
		return toolResult("该工具没有可公开的响应定义", true)
	}
	if out.status == http.StatusNoContent || len(policy) == 0 {
		return toolResult("操作完成", false)
	}
	dec := json.NewDecoder(bytes.NewReader(out.body.Bytes()))
	dec.UseNumber()
	var body map[string]any
	if !json.Valid(out.body.Bytes()) || dec.Decode(&body) != nil || body == nil {
		return toolResult("Hub 未返回预期的 JSON 操作结果，请查询资源状态", true)
	}
	projected, _ := projectPublicValue(body, policy)
	result := projected.(map[string]any)
	if tool == "arkade_get_container_logs" {
		limitContainerLogs(result)
	}
	safe, err := json.Marshal(result)
	if err != nil {
		return toolResult("无法生成 MCP 安全响应", true)
	}
	syncOK, hasSync := body["sync_ok"].(bool)
	return toolResult(string(safe), hasSync && !syncOK)
}
