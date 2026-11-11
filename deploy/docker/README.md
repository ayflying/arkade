# Arkade 容器化调试部署

在一台装有 Docker 的机器上，用 `docker compose` 把 Hub 与 Agent 一起跑起来，验证从
「浏览器 → Hub → Agent → 本机容器」的完整链路。

> **这不是生产部署模板。** 生产形态是：Hub 单独一台，Agent 部署在每台被管服务器上
> （见 `deploy/arkade-agent.service`）。这里的 Agent 挂载宿主 `docker.sock`，只用于本机自测。

## 目录

| 文件 | 说明 |
| --- | --- |
| `docker-compose.yml` | Hub + Agent 两个服务，含健康检查与依赖顺序；普通参数直接写在这里 |
| `hub.config.toml` | Hub 容器内配置（路径均为容器内路径） |
| `agent.config.toml` | Agent 容器内配置（`hub_url` 用服务名 `hub`） |
| `.env.example` | 凭据样例（只有密码与令牌），复制到**仓库根目录** `.env` |
| `../../hub/Dockerfile` | Hub 镜像：先构建前端产物，再构建 Go 二进制 |
| `../../agent/Dockerfile` | Agent 镜像：静态 Go 二进制 |

## 配置约定

- **只有凭据走变量**：`ARKADE_INITIAL_PASS`、`ARKADE_AGENT_TOKEN`（独立部署还要 `ARKADE_AGENT_HUB_URL`）从 `.env` 读取，用 `${VAR:?提示}` 做必填校验，不写进编排文件。
- **其余参数直接写在编排文件里**：端口 8080、容器/镜像/卷/网络名、日志级别、心跳间隔等，一眼可见，改直接改 YAML。
- **`.env` 的位置**：compose 只自动读取「项目目录」的 `.env`。Hub+Agent 编排的项目目录是**仓库根目录**，所以凭据文件是 `./.env`，不是 `deploy/docker/.env`。

## 独立部署 Agent（网页预建服务器）

这是受管服务器上的 **Agent**，不是 HTTP/SOCKS 网络代理。网页先点击“添加服务器”创建待接入记录，取得该记录的专属接入密钥；首次连接后绑定该记录，不再重复创建。

在目标服务器上准备可信的 Agent 镜像（当前不假设存在公开镜像仓库）：

```bash
# 在仓库根目录构建Linux镜像
docker build -f agent/Dockerfile -t arkade-agent:debug .
# 准备独立目录，不覆盖Hub演示部署现有的.env
mkdir -p ./agent-node
cp deploy/docker/agent-compose.yml deploy/docker/agent.config.toml ./agent-node/
cp deploy/docker/agent.env.example ./agent-node/.env
cd ./agent-node
# 编辑.env：填写专属ARKADE_AGENT_TOKEN和可达的ARKADE_AGENT_HUB_URL
chmod 600 .env
docker compose -f agent-compose.yml up -d
docker compose -f agent-compose.yml ps
```

- 生产连接建议 `wss://你的域名/agent/connect`；内网测试可用 `ws://Hub地址:端口/agent/connect`。跨主机不要填写Agent自己的 `127.0.0.1` 或仅在Hub编排网络内可用的 `hub` 服务名。
- 密钥只在创建/重置时完整返回，不写入程序配置文件或YAML。丢失后在网页重置，旧密钥立即作废；更新 `.env` 后用 `docker compose -f agent-compose.yml up -d --force-recreate` 重新注入。
- `.env`、复制出的部署目录和密钥不得提交到Git；不要分享会展开密钥的 `docker compose config` 输出。
- 保留Agent数据卷，以复用身份和本地编排。只有明确要清除该节点数据时才删除卷。
- Docker socket允许控制宿主机容器，等同高权限访问，仅在可信机器上部署。Podman的套接字路径与权限需单独验证，不能直接照搬Docker配置。

## 使用

凭据只配在 `.env`，编排文件里只出现 `${VAR}` 引用。

```bash
cd <仓库根目录>

# 1. 准备凭据（只在这一次填写，之后启动不用再管）
cp deploy/docker/.env.example .env
#    编辑 .env，填 ARKADE_INITIAL_PASS 与 ARKADE_AGENT_TOKEN
chmod 600 .env

# 2. 构建并启动（compose 自动读取根目录 .env，不需要 --env-file）
docker compose -f deploy/docker/docker-compose.yml up -d --build

# 3. 看状态
docker compose -f deploy/docker/docker-compose.yml ps
curl http://127.0.0.1:8080/healthz

# 4. 浏览器打开 http://<机器IP>:8080，用 .env 里的管理员账号登录
```

改动 `.env` 后需要重建容器才生效（环境变量在容器创建时注入）：

```bash
docker compose -f deploy/docker/docker-compose.yml up -d --force-recreate
```

## 清理（不留残留）

```bash
docker compose -f deploy/docker/docker-compose.yml down -v --rmi local
```

`-v` 删除命名卷（数据库与 Agent 身份），`--rmi local` 删除本次构建的镜像。
默认卷名是 `arkade-debug-*`、网络名 `arkade-debug-net`，清理后可用下面的命令核验：

```bash
docker ps -a --filter name=arkade
docker volume ls --filter name=arkade
docker network ls --filter name=arkade
docker images --filter reference='arkade-*'
```

## 配置怎么流转

**凭据放 `.env`，普通参数写在编排文件，程序侧统一从环境变量读。**

```
<仓库根目录>/.env               ← 只放密码与令牌（已被 .gitignore 排除）
        │  compose 读取 ${VAR}
        ▼
deploy/docker/docker-compose.yml ← 普通参数直接写值；凭据处只写 ${VAR:?提示}
        │  注入
        ▼
容器环境变量                     ← 启动时写进进程环境
  ARKADE_INITIAL_PASS / ARKADE_AGENT_TOKEN / ARKADE_LOG_LEVEL / ...
        │  程序启动时读取
        ▼
进程内存                         ← 凭据不落盘
```

改普通参数：改 `docker-compose.yml` → `up -d --force-recreate`。
改凭据：改 `.env` → 同一条命令。都不需要改 toml，也不需要重新构建镜像。

凭据清单（全部变量样例见 [`.env.example`](.env.example)）：

| 变量 | 必填 | 说明 |
| --- | --- | --- |
| `ARKADE_INITIAL_PASS` | 是 | 初始管理员密码 |
| `ARKADE_AGENT_TOKEN` | 是 | Agent 接入令牌，两侧必须一致 |
| `ARKADE_COMPOSE_SOURCE_ROOT` | 否 | 外部编排根目录，只读挂给 Agent；留空不挂 |

编排文件里的普通参数（修改直接改 YAML）：

| 配置 | 值 | 说明 |
| --- | --- | --- |
| Hub 端口 | `8080` | `ports` 里改，前后两处保持一致 |
| 会话有效期 | `72h` | `ARKADE_SESSION_TTL`，支持 `30m`/`24h` |
| Agent 名 | `arkade-debug-node` | 不设会用容器 ID，重建后漂移 |
| 日志 | `info` / `text` | 级别 `debug`/`info`/`warn`/`error`，格式 `text`/`json` |
| 名称 | `arkade-*-debug` | 容器/镜像/卷/网络名，改掉可让多套环境并存 |

两个关键保障：

1. **缺凭据直接拒绝启动**：compose 用 `${VAR:?提示}` 在宿主侧校验，`.env` 没填就中止；
   Hub 自己也会校验（缺 `ARKADE_INITIAL_PASS` 时拒绝启动），不会带着空令牌跑起来。
2. **`.env` 不入库**：`.gitignore` 拦截 `.env`，只放行 `.env.example`。

## 关键设计说明

### 为什么只有凭据用变量

普通参数（端口、名字、日志级别）写死在编排文件里，读配置文件就能看到实际生效值，
不用在「编排里改了什么、`.env` 里又覆盖成什么」之间来回对照。凭据则相反：
它不能进 Git、不能进镜像、不该出现在 YAML 里，所以只走 `.env` + 环境变量注入。

两侧程序仍支持用 `ARKADE_*` 环境变量覆盖可调项
（`hub/internal/config.applyEnv`、`agent/internal/config.applyEnvOverrides`），
并有单元测试逐个变量盯住——漏支持一个，编排里写了也会静默失效。

### 为什么 `.env` 放在仓库根目录

compose 只会自动读取两类 `.env`：项目目录的 `.env`，以及用 `--env-file` 指定的文件。
本编排文件位于 `deploy/docker/`，但项目目录是仓库根目录（`context: ../..`、`name: arkade-debug`），
所以 `cp deploy/docker/.env.example .env` 是把凭据放到**根目录**，而不是 `deploy/docker/.env`。
这样也顺带避免了和同目录独立 Agent 部署（`agent-compose.yml`）的 `.env` 互相覆盖：
那份是另一台服务器、另一份密钥。

### 构建上下文必须传仓库根目录

`docker-compose.yml` 里的 `context: ../..` 不是笔误：`hub/` 与 `agent/` 都通过
`replace` 引用了同级的 `protocol/` 模块，只把子目录拷进镜像会找不到该模块。

### 密码与令牌只走环境变量

配置文件会被挂载进容器、随卷备份，明文写进去等于把凭据永久留在磁盘。
`hub.config.toml` 与 `agent.config.toml` 里的 `agent_token`、`initial_pass` 都留空，
由 `ARKADE_AGENT_TOKEN` / `ARKADE_INITIAL_PASS` 注入。

如果还是有人把令牌写进配置文件，Agent 启动时会打印安全提醒（只提醒不拒绝启动，
避免存量部署突然起不来）。

### Agent 以 root 运行

容器里要读宿主 `/var/run/docker.sock`，该 socket 属主是宿主 `root:docker`，
非 root 用户默认无权限。更严格的做法是用 `group_add` 映射宿主 docker 组 GID，
但那要求部署时知道宿主 GID，跨机器会失效，因此调试环境先用 root。

### agent 服务依赖 hub 健康

`depends_on: condition: service_healthy` 让 Agent 等 Hub 真正健康后再启动，
避免开局进入重连退避循环、日志被刷满（重连退避是 1s→2s→4s…，刷起来很快）。
