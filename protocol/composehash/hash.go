// Package composehash 定义 Hub 与 Agent 共用的编排哈希协议。
package composehash

import (
	"crypto/sha256"
	"encoding/hex"
	"hash"
	"strconv"
)

// Project 始终对 compose 内容与 env 内容做域分隔、长度定界的联合哈希。
// 空 env 也是项目输入，不回退到单文件哈希；不规整换行或其他原始字节。
func Project(content, env string) string {
	h := sha256.New()
	h.Write([]byte("arkade.compose.project.v1\x00content\x00"))
	writeSized(h, content)
	h.Write([]byte("env\x00"))
	writeSized(h, env)
	return hex.EncodeToString(h.Sum(nil))[:16]
}
func writeSized(h hash.Hash, s string) {
	h.Write([]byte(strconv.Itoa(len(s))))
	h.Write([]byte("\x00"))
	h.Write([]byte(s))
}
