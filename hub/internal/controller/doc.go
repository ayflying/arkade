// Package controller 承载 GoFrame 风格的接口控制器。
//
// ## 与 httpx 里既有 handler 的关系
//
// httpx 的 handler 是标准库签名（http.HandlerFunc），业务逻辑写在闭包里，
// 契约（路径、请求字段、响应字段）散落在 register 调用与 map 字面量中。
// 本包的 controller 把这些拆开：
//
//	api/<域>/v1      → 纯契约（Req/Resp 结构体 + json tag），零逻辑
//	internal/controller/<域> → 控制器方法：读输入、调服务、写输出
//
// ## 两条硬约束
//
//  1. **不启用 MiddlewareHandlerResponse**。它会把响应包成
//     `{"code":0,"message":"","data":…}`，而 Hub 的对外契约是顶层裸 JSON，
//     启用它会一次性破坏所有前端调用。
//
//  2. **Response 由控制器直接写**（通过 r.Response）。这样能精确控制
//     状态码、Set-Cookie 与 body，与迁移前的行为逐字节一致；
//     交给 GF 的自动序列化无法保证这些细节（例如 204 不能带 body）。
//
// 因此控制器方法接收 *ghttp.Request，内部用标准库方式写响应。
package controller
