/** 读取当前处理请求的 Hub 版本，不用前端构建号或浏览器缓存冒充运行版本。 */
export async function fetchRuntimeVersion(signal: AbortSignal): Promise<string | null> {
  try {
    const response = await fetch('/healthz', { cache: 'no-store', signal })
    if (!response.ok) return null
    const body: { version?: unknown } | null = await response.json()
    return typeof body?.version === 'string' ? body.version.trim() || null : null
  } catch {
    return null
  }
}
