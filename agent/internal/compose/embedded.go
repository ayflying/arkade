package compose

// SetBinary 设置 compose 使用的 docker 可执行文件，仅在启动装配阶段调用。
// 参数不是 shell 命令，不接受附加参数；空字符串使用默认 docker。
func (m *Manager) SetBinary(binary string) {
	m.runner.Binary = binary
}
