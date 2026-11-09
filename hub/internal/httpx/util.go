package httpx

// util.go 放 HTTP 层的纯工具函数。

import (
	"crypto/rand"
	"encoding/hex"
)

// randomSuffix 生成 8 字节随机十六进制串，用于生成稳定的文本主键。
//
// 为什么不用自增：SQLite 自增语义是 SQLite 专有（AUTOINCREMENT），
// 且迁移数据库时需要重建序列。用应用层生成的随机 ID 与数据库无关。
func randomSuffix() string {
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		// 随机源异常属于系统级故障，此时宁可 panic 也不能生成可预测 ID。
		panic("生成随机 ID 失败: " + err.Error())
	}
	return hex.EncodeToString(buf)
}
