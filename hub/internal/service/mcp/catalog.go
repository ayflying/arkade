package mcp

import "net/http"

const (
	catalogAgents = "/api/agents"
	catalogAgent  = catalogAgents + "/{agentID}"
)

// Catalog returns the explicit allowlist of Arkade's JSON operations. Each call
// builds independent schema and metadata maps. The existing API remains the
// authority for authorization, validation and execution.
func Catalog() []Tool {
	tools := []Tool{
		catalogTool("list_agents", "列出已登记的服务器及在线状态；使用返回的 ID 定位后续操作。", http.MethodGet, catalogAgents, true, false, nil, nil),
	}
	tools = append(tools, catalogStacks()...)
	tools = append(tools, catalogExternalStacks()...)
	tools = append(tools, catalogResources()...)
	tools = append(tools, catalogImages()...)
	return tools
}

type catalogField struct {
	name     string
	schema   map[string]any
	required bool
}

func catalogTool(name, description, method, path string, readOnly, destructive bool, query []string, fixed map[string]any, fields ...catalogField) Tool {
	properties := make(map[string]any, len(fields))
	required := make([]string, 0, len(fields))
	for _, f := range fields {
		properties[f.name] = f.schema
		if f.required {
			required = append(required, f.name)
		}
	}
	return Tool{
		Name: "arkade_" + name, Description: description, Method: method, Path: path, Query: query, FixedBody: fixed,
		ReadOnly: readOnly, Destructive: destructive,
		InputSchema: map[string]any{"type": "object", "properties": properties, "required": required, "additionalProperties": false},
	}
}

func catalogString(name, description string, required bool, min, max int) catalogField {
	s := map[string]any{"type": "string", "description": description}
	if min > 0 {
		s["minLength"] = min
	}
	if max > 0 {
		s["maxLength"] = max
	}
	return catalogField{name, s, required}
}

// catalogStringArray 是字符串数组字段（例如容器内的命令向量）。
//
// 单独一个 helper 而不是让调用方手写 schema：命令向量必须有
// minItems/maxItems 与单项长度上限，否则 AI 客户端可能传进来
// 几百个参数或一个几 MB 的"参数"，白占控制通道的请求槽位。
func catalogStringArray(name, description string, required bool, minItems, maxItems, maxItemLen int) catalogField {
	item := map[string]any{"type": "string"}
	if maxItemLen > 0 {
		item["maxLength"] = maxItemLen
	}
	s := map[string]any{
		"type":        "array",
		"description": description,
		"items":       item,
		"minItems":    minItems,
		"maxItems":    maxItems,
	}
	return catalogField{name, s, required}
}

func catalogPattern(name, description, pattern string, required bool, min, max int) catalogField {
	f := catalogString(name, description, required, min, max)
	f.schema["pattern"] = pattern
	return f
}

func catalogInteger(name, description string, required bool, min, max int) catalogField {
	s := map[string]any{"type": "integer", "description": description, "minimum": min}
	if max > 0 {
		s["maximum"] = max
	}
	return catalogField{name, s, required}
}

func catalogBool(name, description string) catalogField {
	return catalogField{name, map[string]any{"type": "boolean", "description": description}, false}
}

func catalogAgentID() catalogField {
	return catalogString("agentID", "服务器 ID，从 arkade_list_agents 取得。", true, 1, 0)
}

func catalogStackName(key string) catalogField {
	return catalogPattern(key, "Compose 项目名，以小写字母或数字开头。", "^[a-z0-9][a-z0-9_-]*$", true, 1, 64)
}

func catalogComposeContent() catalogField {
	return catalogString("content", "完整 Compose YAML（非空，最多 1 MiB）；不要在 MCP 提交真实凭据，请用服务端密钥引用或 Arkade 网页。", true, 1, 1<<20)
}

func catalogEnvContent(description string) catalogField {
	return catalogString("env_content", description+" 不要通过 MCP 提交真实凭据。", false, 0, 1<<20)
}

// catalogConfigHash 是项目配置哈希字段的 schema。
//
// 长度必须与 composehash.Project 的真实输出一致：**sha256 前 16 位**。
// 曾经这里写成 64 位，于是 MCP 客户端无论传什么都过不了参数校验
// （传 16 位违反 pattern，传 64 位又和 Agent 算出的哈希对不上），
// import_stack / adopt_external_stack 这类工具因此完全不可用。
// 改动长度时请同步 composehash.Project，并让 catalog_test 的比较继续成立。
func catalogConfigHash(key, description string, required bool) catalogField {
	return catalogPattern(key, description, "^[a-f0-9]{16}$", required, 16, 16)
}

func catalogConfirm() map[string]any { return map[string]any{"confirmation": true} }
