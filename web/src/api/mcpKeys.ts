import { httpDelete, httpGet, httpPost } from './request'

export interface McpKey {
  id: string
  name: string
  prefix: string
  createdAt: string
}

interface CreatedMcpKey {
  key: McpKey
  token: string
}

const path = '/mcp/keys'

export async function listMcpKeys(): Promise<McpKey[]> {
  return (await httpGet<{ items: McpKey[] }>(path)).items
}

export function createMcpKey(name: string): Promise<CreatedMcpKey> {
  return httpPost<CreatedMcpKey>(path, { name })
}

export function revokeMcpKey(id: string): Promise<void> {
  return httpDelete(`${path}/${encodeURIComponent(id)}`)
}
