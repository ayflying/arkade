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
	return catalogString("content", "完整 Compose YAML（非空，最多 1 MiB）；不要把其中的凭据转发到无关位置。", true, 1, 1<<20)
}

func catalogEnvContent(description string) catalogField {
	return catalogString("env_content", description, false, 0, 1<<20)
}

func catalogConfigHash(key, description string, required bool) catalogField {
	return catalogPattern(key, description, "^[a-f0-9]{64}$", required, 64, 64)
}

func catalogTail() catalogField {
	return catalogInteger("tail", "返回的尾部行数，省略时为 200。", false, 1, 2000)
}

func catalogConfirm() map[string]any { return map[string]any{"confirmation": true} }
