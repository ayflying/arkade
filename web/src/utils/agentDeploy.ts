/**
 * 部署片段生成：把专属密钥 + 真实 Hub 地址拼成可直接执行的 .env 与 compose
 *
 * ## 为什么需要这一层
 *
 * 用户拿到密钥之后，真正卡住的地方不是「复制一个 token」，
 * 而是**把它放到哪、放成什么名字、compose 里怎么写映射**。
 * 光给一个 token 字符串，用户还得自己翻文档；给错名字（比如把
 * ARKADE_AGENT_HUB_URL 当成程序能读的变量直接注入），Agent 会连不上而且看不出原因。
 *
 * 因此这里生成三样东西，并且刻意让它们**互相一致**：
 *   1. `.env`            用户要填的值（密钥 + Hub 地址 + 名字）
 *   2. `docker-compose.yml` 片段，全部可配置项走 ${VAR}
 *   3. `agent.config.toml`  最小配置（**不含 secret**），只读挂载进容器
 *
 * ## 两个关键的正确性细节（写错就是「照文档做但起不来」）
 *
 * **1) 变量名映射**
 * Agent 程序实际读取的是 `ARKADE_HUB_URL`（见 agent/internal/config/config.go），
 * 而 .env 里为了和别的 ARKADE_AGENT_* 变量保持一致，习惯命名成 `ARKADE_AGENT_HUB_URL`。
 * 两者靠 compose 的 `environment: ARKADE_HUB_URL: ${ARKADE_AGENT_HUB_URL}` 完成映射。
 * **绝不能把 ARKADE_AGENT_HUB_URL 直接注入程序并以为它会读**——它不会读，Agent 会因为没有
 * hub_url 直接启动失败。
 *
 * **2) 配置文件必须存在**
 * Agent 的启动命令是 `arkade-agent run -c /opt/arkade/agent/agent.toml`，
 * `-c` 是必填参数，**文件不存在就启动失败**（见 agent/internal/cli/run.go）。
 * 所以 compose 必须把 agent.config.toml 只读挂载到那个路径，
 * 否则用户会看到容器反复重启而不知道为什么。
 * 该文件只放「容器内路径」这类没法用环境变量表达的值，且**不含 token**。
 *
 * ## 密钥的注入路径
 * token 只在宿主机的 .env 里出现，经 compose `environment` 注入成
 * `ARKADE_AGENT_TOKEN` 环境变量，**不写进 compose.yml、也不写进 agent.config.toml**。
 * 这样共享 compose 文件不会泄漏密钥，配置文件也不会明文落盘
 * （Agent 启动时会检测配置里的 token 并告警）。
 */

/**
 * Agent 容器内配置文件的挂载路径。
 *
 * 必须与镜像的启动命令一致：`arkade-agent run -c /opt/arkade/agent/agent.toml`。
 * 改这里就必须同步改 compose 的 volumes 与 run 参数，否则容器起不来。
 */
export const AGENT_CONFIG_MOUNT_PATH = '/opt/arkade/agent/agent.toml'

/** 生成片段所需的信息 */
export interface DeploySnippetInput {
  /** 服务器显示名（写进 .env 的 ARKADE_AGENT_NAME） */
  agentName: string
  /** 专属密钥（明文，仅本次可得） */
  token: string
  /** 本 Hub 对外的 WebSocket 地址，例如 ws://192.168.1.10:8080/agent/connect */
  hubUrl: string
  /** agent 镜像；默认本地构建名，用户有现成镜像时可改 */
  image: string
}

/**
 * 把值编码成 **dotenv 安全**的形式，防止变量注入。
 *
 * ## 为什么必须做这一步
 *
 * `.env` 是「一行一个 KEY=VALUE」的纯文本格式，用户输入里只要出现
 * 换行、`$`、`#`、引号，就可能破坏结构甚至注入新变量。典型攻击面：
 *
 * - 名称填 `x\nARKADE_AGENT_TOKEN=attacker-token` → **凭空多出一个变量**，
 *   如果它排在后面就会覆盖掉真密钥，Agent 会拿着别人的密钥去连；
 * - 名称填 `a #b` → `#` 之后被 dotenv 当成注释，值被截断；
 * - 名称填 `a$ARKADE_AGENT_TOKEN` → compose 做变量插值时会把密钥拼进去，
 *   密钥就出现在容器环境变量里本不该出现的位置（也会进日志）。
 *
 * ## 采用策略：能安全加引号就加单引号，不能则拒绝
 *
 * dotenv 里**单引号包裹的值不做变量插值、`#` 不是注释、也不解析转义**，
 * 因此单引号是最稳的选择。但单引号内部不能再出现单引号
 * （标准 dotenv 不支持 `\'` 转义），所以：
 *
 * - 值里含单引号 → 拒绝（返回 null），让用户改掉；
 *   静默改写用户输入会导致他填的名字与实际存的不一致，更难排查；
 * - 含 CR/LF → 拒绝（换行会直接拆出新的一行，是注入的主路径）；
 * - 其余（含 `$`、`#`、双引号、空格、中文）→ 单引号包裹即安全。
 *
 * @returns 编码后的值；null 表示该值无法安全编码
 */
export function encodeDotenvValue(raw: string): string | null {
  const value = raw
  // 换行是最危险的：会把一个值拆成两行，直接产生新变量
  if (value.includes('\n') || value.includes('\r')) return null
  // 单引号无法在单引号字符串里表达（不依赖 `\'` 这种非标准转义）
  if (value.includes("'")) return null
  // 单引号包裹：内部一切字符（$ # " 空格 中文）都按字面量处理
  return `'${value}'`
}

/**
 * 校验名称/镜像这类要写进 .env 的自由文本。
 *
 * 与 encodeDotenvValue 配合：先检查能否安全编码，不能就给用户明确原因。
 *
 * @returns 错误文案；空串表示通过
 */
export function validateEnvSafeText(label: string, raw: string): string {
  if (raw.includes('\n') || raw.includes('\r')) {
    return `${label}不能包含换行`
  }
  if (raw.includes("'")) {
    return `${label}不能包含单引号 '（会破坏 .env 格式）`
  }
  return ''
}

/**
 * 校验 Hub 地址。
 *
 * 只接受 ws:// 或 wss://，并且：
 * - **必须带路径**：Agent 连的是 `/agent/connect`，少了路径会 404 而不是被重定向，
 *   排查起来很像「网络不通」；
 * - **拒绝 userinfo**（`ws://user:pass@host`）：凭据会明文落在 .env 和进程参数里，
 *   Agent 也不会用它们做认证——写了只会造成「以为加了认证」的错觉；
 * - **拒绝 query / fragment**：Agent 只按 host+path 建连，这两部分会被静默丢弃，
 *   用户却以为配置生效了。
 *
 * @returns 错误文案；空串表示通过
 */
export function validateHubUrl(url: string): string {
  const value = url.trim()
  if (value === '') return '请填入 Hub 地址'
  if (!/^wss?:\/\//i.test(value)) {
    return 'Hub 地址必须以 ws:// 或 wss:// 开头（例如 ws://10.0.0.5:8080/agent/connect）'
  }
  // 只校验协议与路径，不解析主机名：内网主机名/Tailscale 域名都能用
  let parsed: URL
  try {
    parsed = new URL(value)
  } catch {
    return 'Hub 地址格式不正确'
  }
  if (parsed.username !== '' || parsed.password !== '') {
    return 'Hub 地址不能包含用户名或密码：Agent 不使用 URL 里的凭据认证，请改用专属密钥'
  }
  if (parsed.search !== '') {
    return 'Hub 地址不能带查询参数（? 之后的部分会被忽略）'
  }
  if (parsed.hash !== '') {
    return 'Hub 地址不能带 # 片段（会被忽略）'
  }
  if (parsed.pathname === '' || parsed.pathname === '/') {
    return 'Hub 地址需要包含 /agent/connect 路径'
  }
  return ''
}

/**
 * 猜测当前 Hub 的默认 WebSocket 地址，作为输入框的初始值。
 *
 * 用当前页面的 origin 推断（同源部署时通常就是对的），
 * 把 http→ws、https→wss，并补上 /agent/connect。
 * 猜不准没关系——用户可以改；但给个合理初值比让用户从零手写强得多，
 * 尤其是很多人并不知道「要带 /agent/connect」。
 */
export function guessHubUrl(): string {
  if (typeof window === 'undefined') return ''
  try {
    const { protocol, host } = window.location
    if (host === '') return ''
    const wsProtocol = protocol === 'https:' ? 'wss:' : 'ws:'
    return `${wsProtocol}//${host}/agent/connect`
  } catch {
    return ''
  }
}

/**
 * 生成 .env 内容。
 *
 * 所有用户可控的值都经 `encodeDotenvValue` 用单引号包裹：
 * 单引号内的 `$`、`#`、双引号、空格都按字面量处理，既不会被 dotenv 当注释截断，
 * 也不会在 compose 插值时把密钥拼进别的变量里。
 *
 * 密钥本身也走同一套编码：密钥由后端生成（通常是 URL-safe 随机串），
 * 但它同样是「不可信输入」——万一是 base64 带 `+/=` 或含 `#`，
 * 不编码就会被截断，用户拿到的 .env 里密钥是坏的，排查起来极其费时。
 *
 * 若某个值无法安全编码（含换行或单引号），直接以 `#!` 注释标出问题行，
 * **不静默降级**——宁可让用户看到提示改掉，也不能生成一个会被注入的 .env。
 */
export function buildEnvSnippet(input: DeploySnippetInput): string {
  const lines: string[] = [
    '# Arkade Agent 部署环境变量',
    '# 与本文件同目录的 docker-compose.yml 会自动读取。',
    '# 值统一用单引号包裹：这样 $ 和 # 都按字面量处理，不会被当成变量或注释。',
    ''
  ]

  /** 写入一个键值对；编码失败时写入注释提示，绝不写入未编码的值 */
  const write = (key: string, raw: string, comment?: string): void => {
    if (comment !== undefined) lines.push(`# ${comment}`)
    const encoded = encodeDotenvValue(raw)
    if (encoded === null) {
      lines.push(`# ！！${key} 的值含有换行或单引号，无法安全写入 .env，请修改后重试`)
    } else {
      lines.push(`${key}=${encoded}`)
    }
  }

  write(
    'ARKADE_AGENT_TOKEN',
    input.token,
    '专属接入密钥（在 Arkade 里生成，仅属于这台服务器）\n# 泄露者可直接以这台服务器身份接入，请勿提交进版本库'
  )
  lines.push('')
  write(
    'ARKADE_AGENT_HUB_URL',
    input.hubUrl.trim(),
    'Hub 地址。注意这里叫 ARKADE_AGENT_HUB_URL，\n# compose 会把它映射成 Agent 程序读取的 ARKADE_HUB_URL'
  )
  lines.push('')
  write('ARKADE_AGENT_NAME', input.agentName, '这台服务器的显示名')
  lines.push('')
  write(
    'ARKADE_AGENT_IMAGE',
    input.image.trim() === '' ? 'arkade-agent:debug' : input.image.trim(),
    'Agent 镜像：默认用本地构建的 arkade-agent:debug，\n# 已有可用镜像时改成你自己的镜像地址'
  )
  lines.push('')
  write('ARKADE_DOCKER_SOCKET', '/var/run/docker.sock', '宿主机 Docker 套接字路径（rootless 或 Podman 需改）')

  return lines.join('\n') + '\n'
}

/** 生成 agent.config.toml（最小、且**不含任何 secret**） */
export function buildAgentConfigSnippet(): string {
  return `# Arkade Agent 容器内配置
#
# 本文件只放「容器内路径」这类无法用环境变量表达的值。
# 所有可调项（Hub 地址、密钥、名称、日志级别）都由环境变量覆盖，
# 改配置请改 .env，不要改这里。
#
# 本文件不含密钥：token 由 ARKADE_AGENT_TOKEN 环境变量注入，避免明文落盘。

# 占位值，实际由 ARKADE_HUB_URL 环境变量覆盖。
hub_url = "ws://hub:8080/agent/connect"
token = ""

# 由 ARKADE_AGENT_NAME 覆盖。
name = "arkade-agent"

# Agent 身份与数据目录（挂卷持久化，重启后复用同一个 agent_id）。
# 由 ARKADE_DATA_DIR 覆盖。
data_dir = "/opt/arkade/agent"

# 留空即自动探测 Docker / Podman。
runtime_kind = "auto"

heartbeat_sec = 15
log_level = "info"
log_format = "text"
insecure_tls = false
`
}

/**
 * 生成 docker-compose.yml 片段。
 *
 * ## 三条硬约束
 *
 * **1) 用户可控的值绝不内插进 YAML**
 * 镜像默认值固定为 `arkade-agent:debug`，用户自己的镜像只写在 `.env` 的
 * `ARKADE_AGENT_IMAGE` 里（那里经过单引号编码）。原因：把用户输入内插进 YAML
 * 等于允许他在 YAML 里注入任意配置（例如值里塞 `\n    privileged: true`），
 * 而 YAML 没有 dotenv 那样的引号编码兜底。
 *
 * **2) 所有可配置项走 ${}，不写死**
 * `restart` 等也必须可覆盖：同一份片段会被复制到多台机器上。
 * 这里刻意**不设 container_name**——写死 `container_name: arkade-agent`
 * 会让同一宿主机上的第二个 Agent 因名称冲突起不来。
 * 交给 compose 默认命名，多实例天然不冲突。
 *
 * **3) 不用 env_file 把整个 .env 灌进容器**
 * `.env` 只给 compose 做 `${}` 插值（compose 默认行为，无需 env_file）；
 * 容器里只显式注入程序真正需要的变量，避免把无关变量顺带塞进容器环境。
 *
 * 关键映射与挂载见下方注释：漏了任何一条，容器都会起不来。
 */
export function buildComposeSnippet(): string {
  return `# Arkade Agent 部署片段
# 保存为 docker-compose.yml，与 .env、agent.config.toml 放在同一目录
#
# 启动：docker compose up -d
# 查看：docker compose logs -f
# 停止：docker compose down
#
# 提示：compose 会自动读取同目录的 .env 做变量替换，不需要 env_file。

services:
  agent:
    # 默认用本地构建的镜像：在 Arkade 仓库根目录执行
    #   docker build -f agent/Dockerfile -t arkade-agent:debug .
    # 已有可用镜像时，改 .env 里的 ARKADE_AGENT_IMAGE 即可。
    image: \${ARKADE_AGENT_IMAGE:-arkade-agent:debug}
    restart: \${ARKADE_RESTART_POLICY:-unless-stopped}
    # 刻意不设 container_name：同一台宿主机跑多个 Agent 时会名称冲突。
    environment:
      # ---- 必填：专属密钥由 .env 注入，不写死在本文件 ----
      ARKADE_AGENT_TOKEN: \${ARKADE_AGENT_TOKEN:?必须在 .env 里设置 ARKADE_AGENT_TOKEN}
      # ---- 关键映射 ----
      # Agent 程序读的是 ARKADE_HUB_URL；.env 里叫 ARKADE_AGENT_HUB_URL。
      # 少了这一行映射，程序拿不到 hub_url，会直接启动失败。
      ARKADE_HUB_URL: \${ARKADE_AGENT_HUB_URL:?必须在 .env 里设置 ARKADE_AGENT_HUB_URL}
      # ---- 可调项 ----
      ARKADE_AGENT_NAME: \${ARKADE_AGENT_NAME:-arkade-agent}
      ARKADE_DATA_DIR: /opt/arkade/agent
      ARKADE_RUNTIME_KIND: \${ARKADE_RUNTIME_KIND:-auto}
      ARKADE_LOG_LEVEL: \${ARKADE_LOG_LEVEL:-info}
    volumes:
      # 数据目录持久化：重建容器后复用同一个 agent_id，不会在 Arkade 里变成新机器
      - agent-data:/opt/arkade/agent
      # 配置文件只读挂载到 run -c 指定的路径。
      # 不挂这个文件，Agent 会因为「配置文件不存在」反复重启。
      - ./agent.config.toml:${AGENT_CONFIG_MOUNT_PATH}:ro
      # Agent 需要通过宿主机的 Docker 套接字操作容器
      - \${ARKADE_DOCKER_SOCKET:-/var/run/docker.sock}:/var/run/docker.sock

volumes:
  agent-data:
`
}

/**
 * 一次生成全部片段。
 *
 * 注意 compose 不接收 input：它不含任何用户可控值（见 buildComposeSnippet 的说明），
 * 用户的自定义镜像只通过 .env 生效。保留 input 参数会让调用方误以为
 * 镜像名是两条路径都能改的，进而产生「改了这里却没生效」的困惑。
 */
export function buildSnippets(input: DeploySnippetInput): {
  env: string
  config: string
  compose: string
} {
  return {
    env: buildEnvSnippet(input),
    config: buildAgentConfigSnippet(),
    compose: buildComposeSnippet()
  }
}
