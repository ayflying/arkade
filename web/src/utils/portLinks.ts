/**
 * 端口链接推导
 *
 * 这一份逻辑的**唯一职责**是：给定一条结构化端口映射和服务器配置的访问地址，
 * 判断「能不能做成可点击的链接」，以及链接应该长什么样。
 *
 * ## 为什么值得单独抽出来
 *
 * 拼错 URL 的后果不是难看，而是给用户一个打不开的链接，
 * 而且他不会怀疑是链接错了，只会以为服务挂了。规则又多又琐碎
 * （通配绑定、回环地址、协议、IPv6 方括号），所以抽成纯函数单独测。
 *
 * ## 核心原则：fail closed
 *
 * 任何一条规则不满足、或者输入长得不像个规范地址，就返回 null 让界面显示纯文本。
 * **绝不"顺手修正"**——把 `user@host` 剥成 `host`、把 `javascript:alert(1)`
 * 剥成 `javascript` 再拼成链接，都是在制造一个用户以为正确、实际指向别处的地址。
 * 宁可少一个链接，也不给一个假的。
 *
 * ## 2026-11 起的规则变化（用户明确要求）
 *
 * 1. 服务器访问地址是**带协议前缀**的完整地址，例如
 *    `https://ac8b255bfcf6-0.aynastv.5ddd.com` 或 `http://10.0.0.12:9000`；
 *    协议与端口都由用户配置决定，不再由前端固定 `http` + 映射端口。
 * 2. 不再提供「HTTPS 再试一次」入口——用户已经能配协议，那个按钮没有存在意义。
 * 3. 列表里的「@主机（当前访问地址）」说明行整行去掉：它不提供操作价值，
 *    还把端口单元格撑成两行。
 */
import type { PublishedPort } from '@/types/container'

/**
 * 已归一化的完整访问地址
 *
 * `origin` 是可以直接前缀到 `:端口` 前面的部分，例如 `https://example.com`；
 * `host` 只用来在界面上展示「链接会指向哪台主机」。
 */
export interface AccessOrigin {
  /** 完整 origin：`http://host:port` / `https://host`（不含结尾斜杠、路径、查询） */
  origin: string
  /** 仅主机部分（IPv6 带方括号），用于展示 */
  host: string
  /** 协议：`http` 或 `https` */
  scheme: 'http' | 'https'
  /** 显式配置的端口；undefined 表示用协议默认端口 */
  port?: number
}

/**
 * 合法主机名/主机地址的字符集
 *
 * 只放行：字母、数字、点、短横线、冒号（裸 IPv6 会用到）、方括号。
 * 这个白名单同时挡掉了 `@`（userinfo 注入）、`/`（路径）、`?`/`#`（查询/片段）、
 * 空格、以及任何非 ASCII 字符。
 */
const HOST_CHARSET = /^[A-Za-z0-9.\-:[\]_]+$/

/** 判断是否通配绑定（绑定到本机所有地址） */
export function isWildcardHost(hostIp: string): boolean {
  const h = hostIp.trim()
  return h === '0.0.0.0' || h === '::' || h === '[::]' || h === ''
}

/**
 * 判断是否回环地址
 *
 * 回环地址只在服务器自己内部有意义，从用户浏览器点过去一定失败，
 * 所以不生成链接。
 *
 * 导出它是因为界面需要**准确**说明"为什么没有链接"：
 * 回环是可判定的成因，说清楚比笼统地说"无法生成"更有用；
 * 而其他成因（地址是脏数据等）前端区分不了，就不该硬猜。
 */
export function isLoopbackBind(hostIp: string): boolean {
  const h = hostIp.trim().toLowerCase()
  if (h === 'localhost') return true
  const bare = h.startsWith('[') && h.endsWith(']') ? h.slice(1, -1) : h
  if (bare === '::1') return true
  return /^127\./.test(bare)
}

/**
 * 校验并定型主机部分（不含协议、不带端口）
 *
 * fail closed：任何不符合「纯主机」形态的输入一律判为不可用，**不做修正**。
 * 返回 '' 表示不可用。
 *
 * 允许的形态：
 * - 域名 / IPv4：`app.example.com`、`10.0.0.12`
 * - IPv6：协议侧可能存裸地址 `2001:db8::1`，这里补成 `[2001:db8::1]`
 * - 已带方括号的 IPv6：`[2001:db8::1]` 原样保留
 *
 * 明确拒绝（返回 ''，不尝试挽救）：
 * - 带 userinfo：`user@host`
 * - 带路径 / 查询 / 片段：`host/x`、`host?x=1`、`host#f`
 * - 空主机
 */
function sanitizeHost(raw: string): string {
  const h = raw.trim()
  if (h === '') return ''
  // 白名单字符集，一次挡掉 @/路径/空格等
  if (!HOST_CHARSET.test(h)) return ''

  // 方括号形式：必须是 [xxx]，内部只能是 IPv6 字符
  if (h.startsWith('[') || h.endsWith(']')) {
    if (!h.startsWith('[') || !h.endsWith(']')) return ''
    const inner = h.slice(1, -1)
    if (inner === '' || !/^[0-9A-Fa-f:.]+$/.test(inner) || !inner.includes(':')) return ''
    return `[${inner}]`
  }

  const colons = (h.match(/:/g) ?? []).length

  // 多个冒号 → 裸 IPv6，补方括号
  if (colons > 1) {
    if (!/^[0-9A-Fa-f:.]+$/.test(h)) return ''
    return `[${h}]`
  }

  // 单个冒号 → host:port 形态；由调用方在解析协议后单独处理，这里判为不可用
  if (colons === 1) return ''

  // 剩下的是 IPv4 / 域名 / 单标签主机名
  return h
}

/**
 * 解析服务器配置的完整访问地址
 *
 * 规则（与后端 NormalizeDirectAddress 保持一致，两边同一套语义）：
 * 1. 必须带协议前缀 `http://` 或 `https://`——用户明确要求「带 https 或 http 前缀」，
 *    少写协议就不是一个可点击的地址，宁可让用户补全也不猜；
 * 2. 协议之后是主机 + 可选端口，不允许路径、查询、片段、userinfo；
 * 3. 末尾斜杠容忍（复制地址栏常见），其余一律拒绝。
 *
 * 返回 null 表示「这条配置不可用」，界面据此不生成链接并提示去设置里补全。
 */
export function parseAccessOrigin(raw: string): AccessOrigin | null {
  const s = raw.trim()
  if (s === '') return null

  const schemeMatch = /^(https?):\/\//i.exec(s)
  if (schemeMatch === null) return null
  const scheme = schemeMatch[1].toLowerCase() as 'http' | 'https'
  let rest = s.slice(schemeMatch[0].length)

  // 只容忍结尾单个斜杠（`https://host/`），其余路径一律拒绝
  if (rest.endsWith('/')) rest = rest.slice(0, -1)
  if (rest === '' || rest.includes('/') || rest.includes('?') || rest.includes('#')) return null

  // 拆分端口：方括号 IPv6 里的冒号不算分隔符，裸 IPv6 也不能拆
  let hostPart = rest
  let portText = ''
  if (rest.startsWith('[')) {
    const close = rest.indexOf(']')
    if (close < 0) return null
    hostPart = rest.slice(0, close + 1)
    const tail = rest.slice(close + 1)
    if (tail !== '') {
      if (!tail.startsWith(':')) return null
      portText = tail.slice(1)
    }
  } else {
    const colons = (rest.match(/:/g) ?? []).length
    // ⚠️ 多个冒号 = 裸 IPv6（`2001:db8::1`），整段都是主机。
    // 按最后一个冒号拆端口会把 `2001:db8::1` 拆成 `2001:db8:` + 端口 `1`，
    // 既毁掉地址又造出假端口——这是实测踩过的坑。
    if (colons > 1) {
      hostPart = rest
    } else if (colons === 1) {
      const idx = rest.lastIndexOf(':')
      hostPart = rest.slice(0, idx)
      portText = rest.slice(idx + 1)
    }
  }

  const host = sanitizeHost(hostPart)
  if (host === '') return null

  let port: number | undefined
  if (portText !== '') {
    if (!/^\d{1,5}$/.test(portText)) return null
    const n = Number(portText)
    if (!Number.isInteger(n) || n <= 0 || n > 65535) return null
    port = n
  }

  return {
    origin: port === undefined ? `${scheme}://${host}` : `${scheme}://${host}:${String(port)}`,
    host,
    scheme,
    port
  }
}

/**
 * 是否 UDP
 *
 * UDP 映射对用户仍有信息价值（DNS、游戏端口等），
 * 所以照常显示，只是不给链接。
 */
export function isUdp(port: PublishedPort): boolean {
  return port.protocol.trim().toLowerCase() === 'udp'
}

/**
 * 是否 SCTP
 *
 * 协议里有 sctp，浏览器同样打不开，界面上按「不可直接打开」处理。
 */
export function isSctp(port: PublishedPort): boolean {
  return port.protocol.trim().toLowerCase() === 'sctp'
}

/**
 * 从当前浏览器地址推断访问地址
 *
 * ## 为什么还要这个兜底
 *
 * 用户要求：**没配置就用上报的地址自动连**。
 * 内置 Agent 与 Hub 同机时，"上报的地址"就是用户此刻访问 Arkade 用的那个地址，
 * 只有这种情况可以拿它兜底；远端 Agent 地址不明时宁可不出链接，
 * 也不能把用户指向 Hub 自己的端口。
 *
 * ## 保留协议
 *
 * 2026-11 起链路协议由地址决定，所以这里取 `window.location.protocol`，
 * 用户在 https 站点上点端口不会被迫降到 http（混合内容会被浏览器拦掉）。
 *
 * ## 边界（仍然 fail closed）
 * - 非浏览器环境（SSR / 单测 / 本 Node 进程）→ null
 * - 回环地址（localhost / 127.x / ::1）→ null：那是用户自己的机器，不是服务器
 * - 主机名不含规字符 → 交给 sanitizeHost 判定，不通过就 null
 */
export function inferAccessFromLocation(): AccessOrigin | null {
  if (typeof window === 'undefined') return null
  const { hostname, protocol } = window.location
  if (hostname === '') return null
  if (isLoopbackBind(hostname) || isWildcardHost(hostname)) return null
  const scheme: 'http' | 'https' = protocol === 'https:' ? 'https' : 'http'
  const host = sanitizeHost(hostname)
  if (host === '') return null
  return { origin: `${scheme}://${host}`, host, scheme }
}

/**
 * 决定端口链接实际使用的访问地址
 *
 * 优先级（用户明确要求）：
 * 1. 服务器「编辑服务器」里配置的完整地址（带 http/https 前缀）——配了就用它；
 * 2. 没配置 → 内置 Agent（agentId === 'agent-local'）自动用当前访问地址兜底；
 * 3. 远端 Agent 未配置 → null（不生成链接，界面提示去配置）。
 *
 * 返回 null 表示「没有可用地址」。注意返回的是可复用的 origin 对象，
 * 调用方不用再解析一次字符串。
 */
export function resolvePortAccess(configured: string, agentId: string): AccessOrigin | null {
  const configuredAccess = parseAccessOrigin(configured)
  if (configuredAccess !== null) return configuredAccess

  /*
   * 配置非空却解析失败（例如旧数据只存了 `10.0.0.12`，没有协议前缀）：
   * 不兜底。用户自己写了一条地址，界面若悄悄换成别的地址，
   * 点过去打不开时更难排查——提示他去补全协议更有用。
   */
  if (configured.trim() !== '') return null
  if (agentId !== 'agent-local') return null
  return inferAccessFromLocation()
}

/** 归一化外部传入的访问地址：可以是配置字符串、已解析对象或 null */
function normalizeAccess(access: string | AccessOrigin | null): AccessOrigin | null {
  if (access === null) return null
  if (typeof access === 'string') return parseAccessOrigin(access)
  return access
}

/**
 * 推导一条端口映射的可访问地址
 *
 * @param port    结构化端口映射
 * @param access  访问地址：配置字符串、已解析的 origin 对象，或 null（表示没有可用地址）
 * @returns 可点击的 URL；任何一项不满足都返回 null
 *
 * 规则（必须全部满足）：
 * 1. 协议是 **tcp**——UDP/SCTP 在浏览器里没有对应协议，做成链接一定是坏的
 * 2. `host_port` 是有效正整数（协议侧是 uint16，正常不会越界，仍校验）
 * 3. 访问地址有效——脏数据一律不生成链接
 * 4. 绑定地址**不是回环**——回环只对服务器自己有含义
 *
 * 地址拼装（2026-11 起的语义）：
 * - 访问地址里**显式带端口**时，端口由配置决定，直接使用该 origin。
 *   适用于「一个域名反代到多个宿主端口」的场景，如 `https://x.5ddd.com:38517`。
 * - 只配了域名（协议默认端口）时，把**映射的宿主端口**接到主机后面：
 *   域名解析到这台服务器，宿主端口才是真实入口（`https://x.5ddd.com:38517`）。
 */
export function buildPortUrl(
  port: PublishedPort,
  access: string | AccessOrigin | null
): string | null {
  // 1. 只处理 TCP（协议可能是 tcp / udp / sctp）
  if (port.protocol.trim().toLowerCase() !== 'tcp') return null

  // 2. 端口必须有效
  if (!Number.isInteger(port.host_port) || port.host_port <= 0 || port.host_port > 65535) {
    return null
  }

  // 3. 回环绑定不给链接（放在地址检查前面：回环配了访问地址也没意义）
  if (isLoopbackBind(port.host_ip)) return null

  // 4. 访问地址必须有效；不通过就不生成链接（fail closed）
  const resolved = normalizeAccess(access)
  if (resolved === null) return null

  if (resolved.port !== undefined) return resolved.origin
  return `${resolved.origin}:${String(port.host_port)}`
}

/** 一条端口在界面上应该占一行时的全部信息 */
export interface PortDisplayItem {
  /** 稳定的渲染 key */
  key: string
  port: PublishedPort
  /** 可点击的地址；null 表示不给链接 */
  url: string | null
  /** 非 TCP：展示端口号但不给链接 */
  nonTcp: boolean
  /** 绑定在通配地址上 */
  wildcard: boolean
  /**
   * 被合并掉的同目标项里出现的**其它容器端口**。
   *
   * 正常情况下一个宿主端口只映射一个容器端口，但 daemon 允许
   * `-p 8080:80 -p 8080:8081` 这种写法（后一条通常起不来，但 inspect 里能看到）。
   * 非空说明这一行代表多条映射，tooltip 必须把它们列出来，
   * 否则用户会以为只有一条，排查端口冲突时被误导。
   */
  otherContainerPorts: number[]
}

/** 计算一条端口在界面上会指向哪里（用于去重） */
function targetOf(port: PublishedPort, url: string | null): string {
  if (url !== null) return url
  // 没有链接时，两种通配绑定的同一端口映射只显示一次；具体绑定仍保留区别。
  if (isWildcardHost(port.host_ip)) return `wildcard:${String(port.container_port)}`
  return `bind:${port.host_ip.trim().toLowerCase()}`
}

/**
 * 把结构化端口映射整理成界面要显示的条目，并去掉**指向同一目标**的重复项。
 *
 * ## 为什么需要去重
 *
 * 双栈容器会同时上报 `0.0.0.0:18080` 和 `:::18080` 两条映射，
 * 两者在同一个访问地址下生成的 URL 完全一样，
 * 界面上就变成「18080 18080」。这是实测撞到的真问题。
 *
 * ## 去重键的选择很重要
 *
 * 键是「最终把用户带到哪里」，不是端口号本身：
 * - 有链接 → `host_port/protocol` + **URL**：同一地址下的 IPv4/IPv6 通配合并；
 * - 无链接 → `host_port/protocol` + **绑定地址**；双栈通配仅合并相同的容器端口，
 *   回环与外网、UDP 与 TCP 不合并。
 *
 * 合并时不丢弃信息：容器端口不同的项会被记进 `otherContainerPorts`，
 * 由 tooltip 展示。
 */
export function toDisplayItems(
  ports: readonly PublishedPort[] | undefined,
  access: string | AccessOrigin | null
): PortDisplayItem[] {
  const byKey = new Map<string, PortDisplayItem>()
  const out: PortDisplayItem[] = []
  for (const p of ports ?? []) {
    if (!Number.isInteger(p.host_port) || p.host_port <= 0) continue
    const url = buildPortUrl(p, access)
    const protocol = p.protocol.trim().toLowerCase()
    const dedupeKey = `${String(p.host_port)}/${protocol}|${targetOf(p, url)}`
    const existing = byKey.get(dedupeKey)
    if (existing !== undefined) {
      // 同一目标的重复项：不新增一行，但把不同的容器端口记下来
      if (existing.port.container_port !== p.container_port) {
        existing.otherContainerPorts.push(p.container_port)
      }
      continue
    }
    const item: PortDisplayItem = {
      key: `${p.host_ip}:${String(p.host_port)}:${String(p.container_port)}/${p.protocol}`,
      port: p,
      url,
      nonTcp: protocol !== 'tcp',
      wildcard: isWildcardHost(p.host_ip),
      otherContainerPorts: []
    }
    byKey.set(dedupeKey, item)
    out.push(item)
  }
  return out
}
