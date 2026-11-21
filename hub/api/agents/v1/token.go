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

// RenewEnrollmentReq 为「尚未接入」的服务器换发一把新密钥并返回接入信息。
//
// 为什么是换发而不是回看：库里不保存可解密的密钥副本，想看就得重新生成。
// 已接入的服务器一律拒绝——那时身份已经绑定，应当走「重置密钥」那条明确路径。
type RenewEnrollmentReq struct {
	g.Meta  `path:"/api/agents/{agentID}/enrollment/renew" method:"post" tags:"Agents" summary:"换发未接入服务器的接入密钥"`
	AgentID string `json:"-" in:"path"`
}
type RenewEnrollmentRes struct {
	Agent      AgentView      `json:"agent"`
	Enrollment EnrollmentView `json:"enrollment"`
}
