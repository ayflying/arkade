package controller

// health.go 是健康检查的 GoFrame 标准规范控制器。
//
// ## 采用规范路由（Bind + g.Meta + (ctx,*Req)(*Res,error)）
//
// 对齐 GoFrame 官方示例的写法：请求/响应是带 g.Meta 的类型，
// 控制器方法是 `func(ctx, *Req) (*Res, error)`，由 `group.Bind` 把
// 类型上的 path/method 注册成路由。
//
// 好处是路由与契约**只有一处定义**（类型本身），不会出现
// "手写字符串路由"与"结构体字段"各自演化。
//
// ## 但不启用 MiddlewareHandlerResponse
//
// 官方示例里通常配 `s.Use(ghttp.MiddlewareHandlerResponse)`，
// 它会把响应包成 `{"code":0,"message":"","data":…}`。
// Hub 的对外契约是**顶层裸 JSON**，包一层会同时破坏所有前端调用，
// 因此这里不用它；`(Res, error)` 的返回值由我们自己的中间件写出去
// （见 router_gf.go 的 bindGF）。

import (
	"context"

	healthapi "arkade/hub/api/health/v1"
)

// HealthDeps 是健康检查控制器需要的依赖。
//
// 只取真正用到的两个能力（ping 与在线数），而不是整个 httpx.Deps：
// 这样这个控制器可以在不搭起完整 Hub 的情况下单独测试。
type HealthDeps struct {
	// Ping 探测数据库可用性。返回 error 表示不可用。
	Ping func(ctx context.Context) error
	// AgentsOnline 返回当前在线 Agent 数量。
	AgentsOnline func() int
	// Version 是 Hub 版本。
	Version string
}

// Health 是健康检查控制器。
type Health struct {
	deps HealthDeps
}

// NewHealth 构造健康检查控制器。
func NewHealth(d HealthDeps) *Health { return &Health{deps: d} }

// Index 处理 GET /healthz。
//
// 返回 200 + {status, version, agents_online}；
// 数据库不可用时返回错误（由适配层映射为 5xx，探针据此判定实例不健康）。
//
// 这里**不**把底层错误包进 Res：它可能含 DSN、表名等部署细节。
// 具体原因在 Ping 内部已记日志，对外只给固定文案。
func (c *Health) Index(ctx context.Context, _ *healthapi.HealthReq) (*healthapi.HealthRes, error) {
	if err := c.deps.Ping(ctx); err != nil {
		return nil, ErrUnavailable
	}
	return &healthapi.HealthRes{
		Status:       "ok",
		Version:      c.deps.Version,
		AgentsOnline: c.deps.AgentsOnline(),
	}, nil
}
