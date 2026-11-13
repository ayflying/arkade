package compose

import "arkade/protocol/composehash"

// HashProject 复用共享协议算法，空 env 同样纳入域分隔的项目哈希。
func HashProject(content, envContent string) string { return composehash.Project(content, envContent) }
