export interface StackLogSource { id: string; name: string }
interface LogResponse { lines: string[]; truncated: boolean }

/** 限制并发以免大编排同时向 Agent 发出大量请求；保留容器顺序和各自的行序。 */
export async function readStackLogSnapshot(
  sources: readonly StackLogSource[],
  read: (id: string) => Promise<LogResponse>,
  isCurrent: () => boolean
) {
  const results = new Array<{ source: StackLogSource; response?: LogResponse; error?: string }>(sources.length)
  let index = 0
  async function worker() {
    while (isCurrent() && index < sources.length) {
      const slot = index++
      const source = sources[slot]
      try {
        results[slot] = { source, response: await read(source.id) }
      } catch (err) {
        results[slot] = { source, error: err instanceof Error && err.message ? err.message : '日志加载失败' }
      }
    }
  }
  await Promise.all(Array.from({ length: Math.min(sources.length, 4) }, worker))
  return results
}
