package agentlink

// util.go 存放 agentlink 包共用的时间与上下文小工具。
// 这些函数被从多个文件调用，抽取在此避免重复实现。

import (
	"context"
	"time"
)

// nowUnixNano 返回当前纳秒时间戳。
func nowUnixNano() int64 { return time.Now().UnixNano() }

// timeoutContext 在原有 ctx 上叠加超时。
// 若调用方已设置更短的截止时间，则以更早的为准（context 语义天然保证这一点）。
func timeoutContext(ctx context.Context, d time.Duration) (context.Context, context.CancelFunc) {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithTimeout(ctx, d)
}
