package httpx

// router_types.go 放路由装配用的类型别名与包级状态。

import (
	"net/http"
	"sync/atomic"
)

// 类型别名：让签名简洁，同时保持与标准库完全兼容。
type (
	// httpHandlerFunc 仅为中间件/路径安全测试探针保留标准库签名。
	// 生产业务方法使用原生 GoFrame typed controller。
	httpHandlerFunc = http.HandlerFunc

	// middlewareFunc 是标准库中间件类型。
	//
	// 既有中间件（recoverer/requestLogger/securityHeaders/authenticator/
	// auditMiddleware）都是这个形状，迁移时无需重写。
	middlewareFunc = func(http.Handler) http.Handler
)

// serverSeq 用于生成唯一的 ghttp 实例名。
//
// 为什么必须唯一（GoFrame 实例生命周期约束）：
//
//   - ghttp 实例经包级全局注册表 `serverMapping` 管理，同名**必然复用同一实例**；
//   - 没有 RemoveServer / 清空路由的 API，Shutdown 也不摘除条目；
//   - 对已存在的 route 再次 BindHandler 会走 `Logger().Fatalf`
//     **直接终止进程**——表现为 `go test` 无输出 FAIL，不是某个用例红；
//   - 因此 `go test -count=2` 用固定名字会直接崩掉。
//
// 生产只调用一次 NewRouter，序号恒为 1；测试每建一个 router 递增一个。
var serverSeq int64

// nextServerSeq 原子递增并返回序号。
func nextServerSeq() int64 { return atomic.AddInt64(&serverSeq, 1) }
