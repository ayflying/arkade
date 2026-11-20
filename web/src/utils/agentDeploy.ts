const DEFAULT_AGENT_IMAGE = 'ayflying/arkade-agent:latest'
const AGENT_DATA_DIR = '/opt/arkade/agent'

interface DeploySnippetInput {
  agentName: string
  token: string
  hubUrl: string
}

/** 在 URL 规范化前检查输入，避免空查询、空凭据、点路径等被解析器吞掉。 */
function parseHubUrl(url: string): URL | string {
  const value = url.trim()
  if (value === '') return '请填入 Hub 地址'
  const scheme = /^https?:\/\//i.exec(value)
  if (!scheme) return 'Hub 地址必须以 http:// 或 https:// 开头（例如 http://10.0.0.5:8080）'
  if (/[\s\x00-\x1f\x7f]/.test(value)) return 'Hub 地址不能包含空白或控制字符'
  if (value.includes('?')) return 'Hub 地址不能带查询参数'
  if (value.includes('#')) return 'Hub 地址不能带 # 片段'

  const address = value.slice(scheme[0].length)
  const authority = address.endsWith('/') ? address.slice(0, -1) : address
  if (/[\\/]/.test(authority)) return 'Hub 地址不能包含路径（末尾 / 除外）'
  if (authority.includes('@')) return 'Hub 地址不能包含用户名或密码，请改用专属密钥'
  if (authority === '' || authority.endsWith(':')) return 'Hub 地址格式不正确'

  try {
    const parsed = new URL(value)
    return parsed.port === '0' ? 'Hub 地址端口必须在 1 至 65535 之间' : parsed
  } catch {
    return 'Hub 地址格式不正确'
  }
}

export function validateHubUrl(url: string): string {
  const result = parseHubUrl(url)
  return typeof result === 'string' ? result : ''
}

/** 用当前页面的 HTTP(S) origin 作为初值，目标服务器跨网时可手动修改。 */
export function guessHubUrl(): string {
  if (typeof window === 'undefined') return ''
  const { protocol, origin } = window.location
  return protocol === 'http:' || protocol === 'https:' ? origin : ''
}

/** 先转义 Compose 的 $ 插值，再引用为 YAML 字符串，避免输入改变文件结构。 */
function quoteComposeValue(value: string): string {
  return JSON.stringify(value.replace(/\$/g, () => '$$'))
}

/** 镜像默认执行 run，直接读取环境变量，无需挂载 TOML。 */
export function buildComposeSnippet(input: DeploySnippetInput): string {
  const hubUrl = parseHubUrl(input.hubUrl)
  if (typeof hubUrl === 'string') throw new Error(hubUrl)

  return `# Arkade Agent 部署配置：保存为 docker-compose.yml
# 包含专属密钥，请妥善保管，不要分享或提交到版本库。
# 启动：docker compose up -d
# 查看日志：docker compose logs -f

services:
  agent:
    image: ${DEFAULT_AGENT_IMAGE}
    restart: unless-stopped
    environment:
      AGENT_TOKEN: ${quoteComposeValue(input.token)}
      HUB_URL: ${quoteComposeValue(hubUrl.origin)}
      AGENT_NAME: ${quoteComposeValue(input.agentName || 'arkade-agent')}
    volumes:
      # 镜像内数据目录固定为 /opt/arkade/agent，挂载它即可保留身份与数据。
      - agent-data:${AGENT_DATA_DIR}
      - /var/run/docker.sock:/var/run/docker.sock

volumes:
  agent-data:
`
}
