package stackmigrate

// ids.go：任务标识生成。

import (
	"crypto/rand"
	"encoding/hex"
	"strconv"
	"sync/atomic"
	"time"
)

// fallbackSeq 在随机源失效时提供单调性。
var fallbackSeq uint64

// newTaskID 生成 32 位小写十六进制任务标识。
//
// ============================ 形态与其它任务标识一致 ============================
// 与镜像传输、卷归档的标识同形态（32 位十六进制），前端不需要为它
// 另写一套校验规则。
//
// 随机源不可用时回落到"纳秒时间戳 + 计数"：宁可标识弱一点，
// 也不能让整个迁移因为极端受限环境下拿不到熵而彻底不可用。
// 回落值仍需补齐/截断到 32 位，否则前端校验会拒收。
func newTaskID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err == nil {
		return hex.EncodeToString(b)
	}

	n := atomic.AddUint64(&fallbackSeq, 1)
	raw := strconv.FormatInt(time.Now().UnixNano(), 16) + strconv.FormatUint(n, 16)
	for len(raw) < 32 {
		raw += "0"
	}
	return raw[:32]
}
