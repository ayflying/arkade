// Arkade Agent 模块 arkade/agent。
//
// 依赖说明：
//   - arkade/protocol：Hub 与 Agent 共享的信封、方法名与载荷定义，通过 replace 指向本地目录，
//     保证协议只有一份定义（禁止复制结构体到 Agent 内部）。
//   - github.com/gorilla/websocket：Hub↔Agent 长连接（决策 D-08 明确选用）。
//   - github.com/BurntSushi/toml：配置文件解析。
//
// 运行时访问不引入 docker/docker SDK，改为标准库手写的 HTTP over unix socket 客户端，
// 见 internal/runtime/client.go 的说明。
module arkade/agent

go 1.24

require (
	arkade/protocol v0.0.0
	github.com/BurntSushi/toml v1.6.0
	github.com/gorilla/websocket v1.5.3
)

require gopkg.in/yaml.v3 v3.0.1

replace arkade/protocol => ../protocol
