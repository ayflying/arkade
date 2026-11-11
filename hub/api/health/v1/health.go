// Package v1 定义健康检查接口的 GoFrame 标准契约。
//
// ## 契约声明方式（对齐 GoFrame 官方规范）
//
// 请求结构体里内嵌 `g.Meta`，用 tag 声明路径与方法：
//
//	type Req struct {
//	    g.Meta `path:"/healthz" method:"get"`
//	}
//
// 这样路由由**框架从类型上读取**，调用方只需 `s.Bind(controller)`，
// 不再有一处手写的 "GET:/healthz" 字符串与类型定义各自演化。
//
// ## 为什么响应仍然自己写
//
// Hub 的对外契约是**顶层裸 JSON**（`{"status":"ok",…}`），
// 而 GF 默认会包成 `{"code":0,"message":"","data":{…}}`。
// 一旦启用那层包装，所有前端调用同时失效。因此这里不启用
// MiddlewareHandlerResponse，响应体由控制器显式写出。
// 这也是本包只把 g.Meta 用作路由声明的唯一原因。
package v1

import "github.com/gogf/gf/v2/frame/g"

// ============================ GET /healthz ============================

// HealthReq 是健康检查请求。
//
// 健康检查没有参数：它由容器探活与部署校验调用，
// 任何查询参数都不影响结果。
type HealthReq struct {
	g.Meta `path:"/healthz" method:"get" tags:"Health" summary:"健康检查"`
}

// HealthRes 是健康检查响应。
//
// 字段名一旦改动，运维脚本、容器探针、部署校验都会失效，
// 因此这里是权威定义：
//
//	{
//	  "status":        "ok",
//	  "version":       "1.2.3",
//	  "agents_online": 2
//	}
//
// 注意：本结构体只描述字段，实际序列化由控制器直接完成
// （见包注释里"为什么响应仍然自己写"）。
type HealthRes struct {
	// Status 恒为 "ok"；数据库不可用时接口返回 500 而不是改这个字段。
	Status string `json:"status" dc:"固定为 ok"`
	// Version 是 Hub 版本，来自构建注入。
	Version string `json:"version" dc:"Hub 版本"`
	// AgentsOnline 是当前在线 Agent 数量。
	AgentsOnline int `json:"agents_online" dc:"在线 Agent 数量"`
}
