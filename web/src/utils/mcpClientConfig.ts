/** Match the key API's Unicode whitespace, character count and control-character rules. */
export function mcpKeyName(input: string): string | null {
  const name = input.replace(/^\p{White_Space}+|\p{White_Space}+$/gu, '')
  return name && [...name].length <= 80 && !/\p{Cc}/u.test(name) ? name : null
}

export function mcpEndpoint(origin: string): string {
  return new URL('/mcp', origin).href
}

/** Use a placeholder outside the one-time create dialog. */
export function mcpClientConfig(endpoint: string, token = '<MCP_KEY>'): string {
  return JSON.stringify({
    mcpServers: {
      arkade: { type: 'http', url: endpoint, headers: { Authorization: `Bearer ${token}` } }
    }
  }, null, 2)
}
