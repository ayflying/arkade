package httpx

import (
	"errors"

	servagents "arkade/hub/internal/service/agents"
)

// 访问地址校验只保留一份实现（service/agents.NormalizeDirectAddress），
// 这里只做薄封装，避免两套规则随需求演进分叉。
// 2026-11 起允许带 http:// 或 https:// 前缀的完整地址（协议与端口由用户配置）。
// Hub 不连接此地址，不能把这项元数据变成服务端代理或 SSRF 入口。
func normalizeDirectAddress(raw string) (string, error) {
	normalized, err := servagents.NormalizeDirectAddress(raw)
	if err != nil {
		return "", errors.New(err.Error())
	}
	return normalized, nil
}
