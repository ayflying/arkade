# 部署说明（Agent 侧）

本目录只放 Agent 的部署资产，Hub 侧的部署文件由 Hub 负责人维护。

## 安全约定（必读）

**配置文件里不允许出现任何真实密钥或令牌。** 所有凭据一律通过环境变量注入：

| 凭据 | 环境变量 | 写入位置 |
| --- | --- | --- |
| Agent 接入令牌 | `AGENT_TOKEN` | `/etc/arkade/agent.env`（0600） |
| 初始管理员密码 | `INITIAL_PASS` | 仅 Hub 侧，交互式输入或 EnvironmentFile |

原因很实际：配置文件会被备份、被复制到其他机器、被 ansible 之类的工具批量下发，
令牌写进去等于同时泄露到所有这些副本；而环境变量只存在于进程环境中，不随文件扩散。

`*.example.toml` 里的凭据字段一律留空，不是让你填完再提交——**复制成实际配置后也不填**，
改用 `AGENT_TOKEN` 传入（Agent 读取环境变量的优先级高于配置文件）。

`.gitignore` 已拦截 `.env`、`agent.toml`、`hub.toml`、`config.toml` 等实际配置文件，
只放行 `*.example`。新增配置文件时请沿用这个命名习惯。

## 目录内容

| 文件 | 用途 |
| --- | --- |
| `arkade-agent.service` | systemd 单元文件，Agent 以专用用户常驻运行 |
| `agent.example.toml` | 配置文件模板，逐字段带中文注释 |
| `README.md` | 本文，安装与排障步骤 |

## 安装步骤

```bash
# 1. 创建专用用户与目录（Agent 不需要 root，只要求能访问运行时套接字）
sudo useradd --system --home /opt/arkade --shell /usr/sbin/nologin arkade
sudo install -d -m 0750 -o arkade -g arkade /opt/arkade/agent /etc/arkade

# 2. 安装二进制（静态编译，无外部依赖）
sudo install -m 0755 arkade-agent /usr/local/bin/arkade-agent

# 3. 准备配置：只填 hub_url 这类非敏感项，token 保持留空
sudo install -m 0640 -o arkade -g arkade deploy/agent.example.toml /etc/arkade/agent.toml
sudo editor /etc/arkade/agent.toml

# 3.1 令牌单独放环境变量文件，权限设为 0600（不要写进 agent.toml）
sudo install -m 0600 -o arkade -g arkade /dev/null /etc/arkade/agent.env
sudo editor /etc/arkade/agent.env
# 写入 AGENT_TOKEN=<从 Hub 后台创建并复制的此节点专属密钥>

# 4. 让 Agent 用户能访问运行时套接字
#    Docker：加入 docker 组（等于给了等效 root 权限，属已知权衡）
sudo usermod -aG docker arkade
#    Podman rootless：改用该用户的 systemd 用户会话，见下文

# 5. 安装并启动服务（单元文件已配置 EnvironmentFile=/etc/arkade/agent.env）
sudo install -m 0644 deploy/arkade-agent.service /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable --now arkade-agent
sudo journalctl -u arkade-agent -f
```

## Agent 发现与反向代理

Agent 只配置 Hub 的 HTTP(S) 地址，例如 `HUB_URL=https://hub.example.com`（TOML：`hub_url`）。地址只包含协议、主机和可选端口，默认 `http://127.0.0.1:8080`。生产使用 HTTPS；旧 `ws://`、`wss://` 和带路径的地址不再支持。升级时在 Compose、`.env` 或 systemd 环境文件中手动去掉公开部署变量的 `ARKADE_` 前缀。

1. Agent 请求 `GET /agent/discover` 获取连接路径。反代将这个路径原样转发给 Hub，保留 `Authorization` 请求头。Hub 验证接入密钥后才返回路径，并在升级 WebSocket 前再次验证；已禁用的专属 Agent 同样会被拒绝。
2. Agent 在同一站点对发现返回的路径发起 WebSocket 握手，HTTPS 使用 WSS。默认路径是 `/agent/connect`。
3. 若在 Hub 设置 `server.agent_connect_prefix = "/edge"` 或 `AGENT_CONNECT_PREFIX=/edge`，发现会返回 `/edge/agent/connect`。反代须支持 WebSocket 升级，并在转发时剥离 `/edge`，使 Hub 仍收到 `/agent/connect`。不要修改 `/agent/discover`、Agent 的 `HUB_URL` 或 Hub 的其他路由。`AGENT_CONNECT_PREFIX=` 可清除 TOML 配置的前缀。

## Podman rootless 注意点

rootless Podman 的套接字位于 `$XDG_RUNTIME_DIR/podman/podman.sock`，它**属于某个登录用户**，
默认不会在系统服务里存在。两种做法：

1. **推荐**：让 Agent 以该普通用户的 systemd 用户单元运行
   （`systemctl --user enable --now arkade-agent`，单元文件放 `~/.config/systemd/user/`），
   此时 `XDG_RUNTIME_DIR` 天然存在，配置里 `runtime_kind = "podman"` 即可。
2. 以系统服务运行时，需要该用户先执行一次 `systemctl --user enable --now podman.socket`
   让套接字常驻，并在 `agent.toml` 里显式写 `socket = "/run/user/<uid>/podman/podman.sock"`。

若显式配置的套接字连不上，Agent 会**直接报错退出**而不是静默变成"没有运行时"——
这是刻意的：静默降级会让人误以为机器没装引擎。

## 离线管理（决策 D-05）

Hub 不可用、甚至网络完全断开时，SSH 登录服务器仍可直接管理本机容器：

```bash
arkade-agent ps                     # 列出容器（等同 docker ps -a 的列含义）
arkade-agent start web              # 启动容器
arkade-agent stop  web --force      # 立即停止（SIGKILL）
arkade-agent logs  web -n 100       # 查看末尾 100 行日志
arkade-agent info                   # 查看探测到的运行时类型/版本/端点/rootless
```

这些命令**完全不连 Hub**，内部走与 Hub 下行请求同一套处理器注册表，
因此在线与离线的行为一致，不会出现"线上能用、离线不行"的分歧。

退出码约定：`0` 成功、`1` 运行期失败（如引擎不可用）、`2` 用法/参数错误。

## 排障速查

| 现象 | 排查方向 |
| --- | --- |
| 日志反复出现"连接 Hub 失败（HTTP 401/403）" | 令牌错误或已被吊销，核对 `token` |
| 日志出现"运行时探测失败" | 套接字路径不对或权限不足：用 `arkade-agent info` 确认 |
| 界面显示 Agent 在线但容器列表为空 | 本机确实没有容器，或运行时上报 RuntimeNone |
| 心跳间隔变了 | Hub 在 HelloAck 里下发了 `heartbeat_sec`，以 Hub 为准 |
| 重启后 Agent ID 变了 | `/opt/arkade/agent` 不可写，`agent_id` 未能持久化 |
