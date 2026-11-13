package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// agentIDFile 由连接器持久化，配置层只负责读取，不回写运维配置。
const agentIDFile = "agent_id"

// ResolveAgentID 优先使用显式配置，其次读取连接器保存的稳定身份。
// 首次尚无身份时返回空值，等待 Hub 在 HelloAck 分配。
func (c *Config) ResolveAgentID() (string, error) {
	if id := strings.TrimSpace(c.AgentID); id != "" {
		return id, nil
	}
	path := filepath.Join(c.DataDir, agentIDFile)
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", nil
		}
		return "", fmt.Errorf("读取已保存的 agent_id 失败(%s): %w", path, err)
	}
	return strings.TrimSpace(string(data)), nil
}
