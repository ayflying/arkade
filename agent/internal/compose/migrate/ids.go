package migrate

// ids.go：标识符生成。

import (
	"crypto/rand"
	"encoding/hex"
	"strconv"
	"time"
)

// randomHex 生成 n 字节随机数的十六进制串（长度 2n）。
//
// ============================ 为什么不用 math/rand ============================
// 这些 id 会出现在协议里、被拼进文件路径。可预测的 id 让攻击者
// 能够猜到别人的导出标识（虽然是本机场景，但"可预测的路径拼接来源"
// 是不必要的风险，而 crypto/rand 的代价可以忽略）。
//
// 随机源不可用时回落到纳秒时间戳：宁可 id 弱一点，也不能让整个导出功能
// 因为拿不到熵而彻底不可用（极端受限的容器环境确实可能如此）。
func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		// 回落：纳秒时间戳 + 单调计数，保证同一纳秒内不重复。
		fallbackCounter++
		raw := strconv.FormatInt(time.Now().UnixNano(), 16) +
			strconv.FormatUint(fallbackCounter, 16)
		out := make([]byte, 0, n*2)
		for i := 0; i < n*2; i++ {
			out = append(out, raw[i%len(raw)])
		}
		return string(out)
	}
	return hex.EncodeToString(b)
}

// fallbackCounter 只在随机源失效时使用。
var fallbackCounter uint64
