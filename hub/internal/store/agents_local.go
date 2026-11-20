package store

// 内置本机使用固定身份，无网络接入凭据；远程握手不可认领。
//
// LocalAgentName 只是**首次创建时**的默认名，落到 agents.name/display_name 后
// 由管理员自行修改，EnsureEmbeddedLocalAgent 不会覆盖已有命名。
const (
	LocalAgentID   = "agent-local"
	LocalAgentName = "local"
)
