package httpx

// csrf.go 的这一段校验**已停用**。
//
// ============================ 为什么移到这里 ============================
//
// 部署形态决定了这道校验在哪一层做才对：Arkade 几乎总是跑在反向代理后面
// （Nginx/Caddy/frp 等），而代理到 Hub 的一跳是明文 http。
// 浏览器发来的是 https 的 Origin，Hub 看到的是 http，于是"同源"永远判失败，
// **连登录都会被 403 拦死**（实测：https://.../api/auth/login → 拒绝跨站修改请求）。
//
// 要修对，Hub 只能去信 X-Forwarded-Proto，而那等于让"谁访问得到 Hub"
// 来决定安全判断——一个能被伪造的头不该成为防线。
// 因此这层防护交给反向代理（那里能看到真实的 TLS 与 Host），
// Hub 只保留与部署位置无关的三条防线：
//
//   - 只接受 JSON 请求体（middleware_bare_json.go）：表单/简单请求发不出 application/json；
//   - 会话 Cookie 为 SameSite=Lax：跨站发起时不带 Cookie；
//   - 自定义请求头要求：跨站请求想带上它必须先过预检，而预检由代理按自己的策略应答。
//
// 如果将来有人想重新打开服务端 Origin 校验，前提是先让 Hub 知道
// "我这个部署到底被我暴露在哪个对外地址上"（显式配置，而不是从请求头推断）。

import "net/http"

// browserMutationAllowed 现在恒为 true：跨站写请求的拦截已交给反向代理。
//
// 保留函数而不是删掉调用点，是为了让 securityHeaders 里那一段
// 保持形状不变——将来若补上可信代理配置，这里就是唯一要改的地方。
func browserMutationAllowed(r *http.Request) bool {
	_ = r
	return true
}
