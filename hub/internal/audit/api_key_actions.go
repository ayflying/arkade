package audit

const (
	ActionAPIKeyCreate = "mcp.key_create"
	ActionAPIKeyRevoke = "mcp.key_revoke"
)

func init() {
	labels[ActionAPIKeyCreate] = "创建 MCP 密钥"
	labels[ActionAPIKeyRevoke] = "撤销 MCP 密钥"
}
