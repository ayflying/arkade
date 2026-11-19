const DEFAULT_AGENT_IMAGE = 'ghcr.io/ayflying/arkade-agent:latest'
const AGENT_DATA_DIR = '/opt/arkade/agent'

interface DeploySnippetInput {
  agentName: string
  token: string
  hubUrl: string
}

/** 校验 Agent 使用的 WebSocket 地址，不允许在地址里携带凭据。 */
export function validateHubUrl(url: string): string {
  const value = url.trim()
  if (value === '') return '请填入 Hub 地址'
  if (!/^wss?:\/\//i.test(value)) {
    return 'Hub 地址必须以 ws:// 或 wss:// 开头（例如 ws://10.0.0.5:8080/agent/connect）'
  }
  if (/[\r\n\t]/.test(value)) return 'Hub 地址不能包含换行或制表符'

  let parsed: URL
  try {
    parsed = new URL(value)
  } catch {
    return 'Hub 地址格式不正确'
  }
  if (parsed.username !== '' || parsed.password !== '') {
    return 'Hub 地址不能包含用户名或密码：Agent 不使用 URL 里的凭据认证，请改用专属密钥'
  }
  if (parsed.search !== '') return 'Hub 地址不能带查询参数（? 之后的部分会被忽略）'
  if (parsed.hash !== '') return 'Hub 地址不能带 # 片段（会被忽略）'
  if (parsed.pathname === '' || parsed.pathname === '/') return 'Hub 地址需要包含 /agent/connect 路径'
  return ''
}

/** 用当前页面的 origin 作为初值，目标服务器跨网时可手动修改。 */
export function guessHubUrl(): string {
  if (typeof window === 'undefined') return ''
  const { protocol, host } = window.location
  if (host === '') return ''
  return `${protocol === 'https:' ? 'wss:' : 'ws:'}//${host}/agent/connect`
}

/** 先转义 Compose 的 $ 插值，再引用为 YAML 字符串，避免输入改变文件结构。 */
function quoteComposeValue(value: string): string {
  return JSON.stringify(value.replace(/\$/g, () => '$$'))
}

/** 镜像默认执行 run，直接读取环境变量，无需挂载 TOML。 */
export function buildComposeSnippet(input: DeploySnippetInput): string {
  return `# Arkade Agent 部署配置：保存为 docker-compose.yml
# 包含专属密钥，请妥善保管，不要分享或提交到版本库。
# 启动：docker compose up -d
# 查看日志：docker compose logs -f

services:
  agent:
    image: ${DEFAULT_AGENT_IMAGE}
    restart: unless-stopped
    environment:
      ARKADE_AGENT_TOKEN: ${quoteComposeValue(input.token)}
      ARKADE_HUB_URL: ${quoteComposeValue(input.hubUrl.trim())}
      ARKADE_AGENT_NAME: ${quoteComposeValue(input.agentName || 'arkade-agent')}
      ARKADE_DATA_DIR: ${AGENT_DATA_DIR}
      ARKADE_RUNTIME_KIND: auto
      ARKADE_LOG_LEVEL: info
    volumes:
      # 保留 Agent 身份和数据，避免重建容器后丢失。
      - agent-data:${AGENT_DATA_DIR}
      - /var/run/docker.sock:/var/run/docker.sock

volumes:
  agent-data:
`
}
