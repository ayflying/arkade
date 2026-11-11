# Arkade 容器化部署编排

镜像不在部署机上编译：由 GitHub Actions（[`.github/workflows/images.yml`](../../.github/workflows/images.yml)）
在每次推送到 `master` 后构建并推送到 GitHub Container Registry，编排文件直接拉取。

| 镜像 | 地址 | 说明 |
| --- | --- | --- |
| Hub | `ghcr.io/ayflying/arkade-hub` | 含前端产物，多架构 amd64/arm64 |
| Agent | `ghcr.io/ayflying/arkade-agent` | 内置 docker-cli 与 compose 插件，多架构 |

标签规则：`master` 推送发 `latest` 与 `sha-<完整提交号>`；`v*` 标签额外发语义版本号。
**生产建议固定 `sha-` 标签或摘要**，不要跟 `latest` 漂移，回滚也只改一个字符串。

主从两套编排，**分开部署，互不混装**：

- **主节点**跑 Hub（控制面）：`hub-compose.yml`。不挂任何宿主套接字，不装 docker CLI，操作不到 Docker。
- **被管节点**跑 Agent（执行面）：`agent-compose.yml`。必须挂宿主 `docker.sock` 才能操作本机容器。

> 这是刻意的安全边界（见 `README.md` 与 `docs/01-architecture.md`）：Hub 只下发指令，
> 真正的容器操作全部发生在被管服务器上的 Agent 里。

## 目录

| 文件 | 说明 |
| --- | --- |
| `hub-compose.yml` | 主节点：只有 hub 服务，拉 GHCR 镜像，含健康检查；**没有 agent 服务、没有 docker.sock** |
| `agent-compose.yml` | 被管节点：只有 agent 服务，拉 GHCR 镜像；挂宿主 docker.sock（等价 root 权限） |
| `hub.config.toml` | Hub 容器内配置（路径均为容器内路径） |
| `agent.config.toml` | Agent 容器内配置（Hub 地址由环境变量覆盖） |
| `.env.example` | 主节点凭据样例（密码与令牌），复制到**仓库根目录** `.env` |
| `agent.env.example` | 被管节点凭据样例（专属令牌与 Hub 地址），复制为 Agent 目录的 `.env` |

## 配置约定

- **只有凭据走变量**：`ARKADE_INITIAL_PASS`、`ARKADE_AGENT_TOKEN`（Agent 侧另有专属令牌和 `ARKADE_AGENT_HUB_URL`）从 `.env` 读取，用 `${VAR:?提示}` 做必填校验，不写进编排文件。
- **其余参数直接写在编排文件里**：端口 8080、容器/镜像/卷/网络名、日志级别、心跳间隔等，一眼可见，改直接改 YAML。
- **`.env` 的位置**：compose 只自动读取「项目目录」的 `.env`。Hub 编排的项目目录是**仓库根目录**，所以凭据文件是 `./.env`，不是 `deploy/docker/.env`。

## 主节点：部署 Hub

```bash
cd <仓库根目录>

# 1. 准备凭据（只在这一次填写）
cp deploy/docker/.env.example .env
#    编辑 .env，填 ARKADE_INITIAL_PASS 与 ARKADE_AGENT_TOKEN
chmod 600 .env

# 2. 拉取 CI 构建好的镜像并启动（不编译，秒级）
docker compose -f deploy/docker/hub-compose.yml pull
docker compose -f deploy/docker/hub-compose.yml up -d

# 3. 看状态
docker compose -f deploy/docker/hub-compose.yml ps
curl http://127.0.0.1:8080/healthz

# 4. 浏览器打开 http://<机器IP>:8080，用 .env 里的管理员账号登录
```

想用自己本地源码构建（比如改完还没推 CI）：加 `--build` 即可，镜像仍按同名标签落盘。

```bash
docker compose -f deploy/docker/hub-compose.yml up -d --build
```

主节点的权限边界（有意为之）：容器内只有 `/data` 数据卷与只读的 `hub.config.toml`，
看不到 `/var/run/docker.sock`，因此即使 Hub 有漏洞也无法直接操纵宿主机容器。
要管哪台机器的容器，就在**那台机器**上部署 Agent。

改动 `.env` 后需要重建容器才生效（环境变量在容器创建时注入）：

```bash
docker compose -f deploy/docker/hub-compose.yml up -d --force-recreate
```

## 被管节点：部署 Agent（网页预建服务器）

这是受管服务器上的 **Agent**，不是 HTTP/SOCKS 网络代理。网页先点击“添加服务器”创建待接入记录，取得该记录的专属接入密钥；首次连接后绑定该记录，不再重复创建。

在目标服务器上准备编排三件套（镜像直接从 GHCR 拉，不需要在服务器上编译）：

```bash
mkdir -p ./agent-node
cp deploy/docker/agent-compose.yml deploy/docker/agent.config.toml ./agent-node/
cp deploy/docker/agent.env.example ./agent-node/.env
cd ./agent-node
# 编辑.env：填写专属ARKADE_AGENT_TOKEN和可达的ARKADE_AGENT_HUB_URL
chmod 600 .env
docker compose -f agent-compose.yml pull
docker compose -f agent-compose.yml up -d
docker compose -f agent-compose.yml ps
```

上面 `cp` 是从仓库工作区复制；不在本机的服务器，直接从仓库拿这三个文件（或 `git clone`）即可。
镜像公开可拉，无需登录；私有仓库才需要 `docker login ghcr.io`（只给 `read:packages`）。

- 生产连接建议 `wss://你的域名/agent/connect`；内网测试可用 `ws://Hub地址:端口/agent/connect`。跨主机不要填写Agent自己的 `127.0.0.1` 或仅在Hub编排网络内可用的 `hub` 服务名。
- 密钥只在创建/重置时完整返回，不写入程序配置文件或YAML。丢失后在网页重置，旧密钥立即作废；更新 `.env` 后用 `docker compose -f agent-compose.yml up -d --force-recreate` 重新注入。
- `.env`、复制出的部署目录和密钥不得提交到Git；不要分享会展开密钥的 `docker compose config` 输出。
- 保留Agent数据卷，以复用身份和本地编排。只有明确要清除该节点数据时才删除卷。
- Docker socket允许控制宿主机容器，等同高权限访问，仅在可信机器上部署。Podman的套接字路径与权限需单独验证，不能直接照搬Docker配置。

## 本地联调（Hub 与 Agent 同一台机器）

想在一台机器上验证完整链路，就在同一台机器上**分别**起两套：

```bash
# 终端 1：主节点
docker compose -f deploy/docker/hub-compose.yml pull
docker compose -f deploy/docker/hub-compose.yml up -d

# 终端 2：本机作为被管节点（网页添加服务器拿到 Agent 专属密钥后）
mkdir -p ./agent-node && cp deploy/docker/agent-compose.yml deploy/docker/agent.config.toml ./agent-node/
cp deploy/docker/agent.env.example ./agent-node/.env
#    ARKADE_AGENT_HUB_URL 填宿主可达地址，例如 ws://127.0.0.1:8080/agent/connect
#    （Agent 是独立容器，这里写宿主映射出来的 8080 端口是对的）
cd ./agent-node && docker compose -f agent-compose.yml pull && docker compose -f agent-compose.yml up -d
```

注意：这里是把本机登记成一个被管节点，Agent 才挂 socket；Hub 自身仍然没有任何容器操作能力。

## 清理（不留残留）

```bash
# 主节点
docker compose -f deploy/docker/hub-compose.yml down -v --rmi local
# 被管节点（在 Agent 目录）
docker compose -f agent-compose.yml down -v
```

`-v` 删除命名卷（Hub 数据库 / Agent 身份），`--rmi local` 删除本次构建的镜像。
默认卷名 `arkade-hub-data` / `arkade-agent-data`，网络名 `arkade-hub-net` / `arkade-agent-net`，清理后核验：

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
deploy/docker/hub-compose.yml    ← 普通参数直接写值；凭据处只写 ${VAR:?提示}
        │  注入
        ▼
容器环境变量                     ← 启动时写进进程环境
  ARKADE_INITIAL_PASS / ARKADE_AGENT_TOKEN / ARKADE_LOG_LEVEL / ...
        │  程序启动时读取
        ▼
进程内存                         ← 凭据不落盘
```

改普通参数：改编排文件 → `up -d --force-recreate`。
改凭据：改 `.env` → 同一条命令。都不需要改 toml，也不需要重新构建镜像。

凭据清单（样例见 [`.env.example`](.env.example) 与 [`agent.env.example`](agent.env.example)）：

| 变量 | 位置 | 必填 | 说明 |
| --- | --- | --- | --- |
| `ARKADE_INITIAL_PASS` | 主节点 | 是 | 初始管理员密码 |
| `ARKADE_AGENT_TOKEN` | 主节点 | 是 | Hub 侧 Agent 准入令牌 |
| `ARKADE_AGENT_TOKEN` | 被管节点 | 是 | 该节点专属接入令牌，与网页生成的值一致 |
| `ARKADE_AGENT_HUB_URL` | 被管节点 | 是 | 从该服务器可达的 Hub 地址 |

编排文件里的普通参数（修改直接改 YAML）：

| 配置 | 值 | 说明 |
| --- | --- | --- |
| Hub 端口 | `8080` | `ports` 里改，前后两处保持一致 |
| 会话有效期 | `72h` | `ARKADE_SESSION_TTL`，支持 `30m`/`24h` |
| Agent 名 | `arkade-node` | 不设会用容器 ID，重建后漂移 |
| 日志 | `info` / `text` | 级别 `debug`/`info`/`warn`/`error`，格式 `text`/`json` |
| 心跳 | `15` 秒 | 兜底值，Hub 握手后会下发 |

两个关键保障：

1. **缺凭据直接拒绝启动**：compose 用 `${VAR:?提示}` 在宿主侧校验，`.env` 没填就中止；
   Hub 自己也会校验（缺 `ARKADE_INITIAL_PASS` 时拒绝启动），不会带着空令牌跑起来。
2. **`.env` 不入库**：`.gitignore` 拦截 `.env`，只放行 `.env.example`。

## 关键设计说明

### 镜像由 CI 构建，部署机只拉取

镜像在 GitHub Actions 里统一构建、统一打标签、统一推 GHCR，部署机只做 `pull`：

- 部署机不需要装 Go、Node、pnpm，也不需要拉源码——一台只装了 docker 的服务器就能上；
- 构建产物来自同一次流水线的同一个提交，`sha-<提交号>` 标签可以直接对回代码；
- 多架构清单让 x86 与 ARM 服务器拉到各自正确的镜像；
- 回滚只是把 `image:` 改回上一个 `sha-` 标签或摘要，不需要在服务器上重新编译。

代价是「改了代码但没推」时部署机拿不到新镜像，这时用 `--build` 走本地构建覆盖，
但那条路径的产物没有 CI 的测试门禁，只适合自测。

### 为什么 Hub 编排里没有 docker.sock

Hub 是控制面：Web、鉴权、Agent 管理、编排下发、镜像凭据集中配置。它本身不改任何一台机器的容器。
把 socket 挂给 Hub，等于给主节点开了一个「绕过 Agent 直接操作宿主机」的旁路，
既违背主从安全边界，也让审计链路断在这里。

所以主节点的容器里只有 `/data` 卷和只读配置文件；容器操作一律经过 Agent，
而 Agent 只部署在明确授权的被管服务器上。早期为了单机联调，把 Hub 和 Agent
写在同一个 compose 文件里、共用一个挂载，容易让人误以为主节点也需要 socket——现已拆开。

### 为什么只有凭据用变量

普通参数（端口、名字、日志级别）写死在编排文件里，读配置文件就能看到实际生效值，
不用在「编排里改了什么、`.env` 里又覆盖成什么」之间来回对照。凭据则相反：
它不能进 Git、不能进镜像、不该出现在 YAML 里，所以只走 `.env` + 环境变量注入。

两侧程序仍支持用 `ARKADE_*` 环境变量覆盖可调项
（`hub/internal/config.applyEnv`、`agent/internal/config.applyEnvOverrides`），
并有单元测试逐个变量盯住——漏支持一个，编排里写了也会静默失效。

### 为什么 `.env` 放在仓库根目录

compose 只会自动读取两类 `.env`：项目目录的 `.env`，以及用 `--env-file` 指定的文件。
Hub 编排位于 `deploy/docker/`，但项目目录是仓库根目录（`context: ../..`、`name: arkade-hub`），
所以 `cp deploy/docker/.env.example .env` 是把凭据放到**根目录**。
被管节点则在自己复制出去的目录里用自己的 `.env`（`agent.env.example`），
两份密钥天然隔离，不会互相覆盖。

### 构建上下文必须传仓库根目录

`hub-compose.yml` 里的 `context: ../..` 不是笔误：`hub/` 与 `agent/` 都通过
`replace` 引用了同级的 `protocol/` 模块，只把子目录拷进镜像会找不到该模块。
CI 也是这样构造的（工作流的 `context: .`、`file: <module>/Dockerfile`），
所以用 `--build` 在本地构建和在 CI 构建，产物结构一致。

### 密码与令牌只走环境变量

配置文件会被挂载进容器、随卷备份，明文写进去等于把凭据永久留在磁盘。
`hub.config.toml` 与 `agent.config.toml` 里的 `agent_token`、`initial_pass` 都留空，
由 `ARKADE_AGENT_TOKEN` / `ARKADE_INITIAL_PASS` 注入。

如果还是有人把令牌写进配置文件，Agent 启动时会打印安全提醒（只提醒不拒绝启动，
避免存量部署突然起不来）。

### Agent 以 root 运行

容器里要读宿主 `/var/run/docker.sock`，该 socket 属主是宿主 `root:docker`，
非 root 用户默认无权限。更严格的做法是用 `group_add` 映射宿主 docker 组 GID，
但那要求部署时知道宿主 GID，跨机器会失效，因此当前用 root——这也是 socket 只给被管节点的原因。
