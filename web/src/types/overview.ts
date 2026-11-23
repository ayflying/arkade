export type AgentResourceCounts = {
  containers?: number
  runningContainers?: number
  stacks?: number
  unmanagedStacks?: number
  images?: number
  networks?: number
  volumes?: number
}

export type AgentOverview = {
  agentId: string
  counts: AgentResourceCounts
  driftedStacks: string[]
  orphanStacks: string[]
  failures: Array<{ resource: string; message: string }>
}
