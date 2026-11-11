package v1

import "github.com/gogf/gf/v2/frame/g"

type CreateReq struct {
	g.Meta `path:"/api/agents" method:"post" tags:"Agents" summary:"预创建服务器"`
	Name   string            `json:"name"`
	Note   string            `json:"note"`
	Labels map[string]string `json:"labels"`
}
type CreateRes struct {
	Agent      AgentView      `json:"agent"`
	Enrollment EnrollmentView `json:"enrollment"`
}
type ResetTokenReq struct {
	g.Meta  `path:"/api/agents/{agentID}/token/reset" method:"post" tags:"Agents" summary:"重置服务器密钥"`
	AgentID string `json:"-" in:"path"`
}
type ResetTokenRes struct {
	Agent      AgentView      `json:"agent"`
	Enrollment EnrollmentView `json:"enrollment"`
}
type EnrollmentView struct {
	Token   string `json:"token"`
	AgentID string `json:"agent_id"`
}
