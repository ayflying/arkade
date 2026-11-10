/**
 * 端口链接推导
 *
 * 这一份逻辑的**唯一职责**是：给定一条结构化端口映射和服务器直连地址，
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
 * 任何一条规则不满足、或者输入长得不像个规范主机，就返回 null 让界面显示纯文本。
 * **绝不"顺手修正"**——把 `user@host` 剥成 `host`、把 `http://x` 剥成 `x`、
 * 把 `javascript:alert(1)` 剥成 `javascript` 再拼成链接，都是在制造一个
 * 用户以为正确、实际指向别处的地址。宁可少一个链接，也不给一个假的。
 */
import type { PublishedPort } from '@/types/container'

/**
 * 合法主机名的字符集。
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
 * 判断是否回环地址。
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
 * 校验并定型直连地址。
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
 * - 带 scheme：`http://x`、`javascript:alert(1)`
 * - 带 userinfo：`user@host`
 * - 带路径 / 查询 / 片段：`host/x`、`host?x=1`、`host#f`
 * - 带端口：`host:8080`（端口由映射决定，不该出现在这里）
 */
function sanitizeHost(raw: string): string {
  const h = raw.trim()
  if (h === '') return ''
  // 白名单字符集，一次挡掉 scheme/@/路径/空格等
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

  // 单个冒号 → host:port 形态，拒绝（端口不该出现在直连地址里）
  if (colons === 1) return ''

  // 剩下的是 IPv4 / 域名 / 单标签主机名
  return h
}

/**
 * 推导一条端口映射的可访问地址。
 *
 * @param port          结构化端口映射
 * @param directAddress 服务器直连地址；空表示未配置
 * @returns 可点击的 http URL；任何一项不满足都返回 null
 *
 * 规则（必须全部满足）：
 * 1. 协议是 **tcp**——UDP/SCTP 在浏览器里没有对应协议，做成链接一定是坏的
 * 2. `host_port` 是有效正整数（协议侧是 uint16，正常不会越界，仍校验）
 * 3. 直连地址通过 `sanitizeHost` 校验——脏数据一律不生成链接
 * 4. 绑定地址**不是回环**——回环只对服务器自己有含义
 *
 * 地址替换：无论绑定是通配（`0.0.0.0`/`::`）还是具体 IP，
 * 一律用 `direct_address`。原因：用户能访问的路径由他配置的地址决定，
 * 服务器内部绑定的具体 IP 不代表外部可达。
 */
export function buildPortUrl(port: PublishedPort, directAddress: string): string | null {
  // 1. 只处理 TCP（协议可能是 tcp / udp / sctp）
  if (port.protocol.trim().toLowerCase() !== 'tcp') return null

  // 2. 端口必须有效
  if (!Number.isInteger(port.host_port) || port.host_port <= 0 || port.host_port > 65535) {
    return null
  }

  // 4. 回环绑定不给链接（放在地址检查前面：回环配了直连地址也没意义）
  if (isLoopbackBind(port.host_ip)) return null

  // 3. 直连地址必须通过严格校验；不通过就不生成链接（fail closed）
  const host = sanitizeHost(directAddress)
  if (host === '') return null

  return `http://${host}:${String(port.host_port)}`
}

/**
 * 是否 UDP。
 *
 * UDP 映射对用户仍有信息价值（DNS、游戏端口等），
 * 所以照常显示，只是不给链接。
 */
export function isUdp(port: PublishedPort): boolean {
  return port.protocol.trim().toLowerCase() === 'udp'
}

/**
 * 是否 SCTP。
 *
 * 协议里有 sctp，浏览器同样打不开，界面上按「不可直接打开」处理。
 */
export function isSctp(port: PublishedPort): boolean {
  return port.protocol.trim().toLowerCase() === 'sctp'
}

/** 一条端口在界面上应该占一行时的全部信息 */
export interface PortDisplayItem {
  /** 稳定的渲染 key */
  key: string
  port: PublishedPort
  /** 可点击的 http 地址；null 表示不给链接 */
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
  // 没有链接时，绑定地址本身就是"目标"：回环和外网是不同去处
  return `bind:${port.host_ip.trim().toLowerCase()}`
}

/**
 * 把结构化端口映射整理成界面要显示的条目，并去掉**指向同一目标**的重复项。
 *
 * ## 为什么需要去重
 *
 * 双栈容器会同时上报 `0.0.0.0:18080` 和 `:::18080` 两条映射，
 * 两者在同一个 `direct_address` 下生成的 URL 完全一样，
 * 界面上就变成「18080 HTTPS 18080 HTTPS」。这是实测撞到的真问题。
 *
 * ## 去重键的选择很重要
 *
 * 键是「最终把用户带到哪里」，不是端口号本身：
 * - 有链接 → `host_port/protocol` + **URL**：同一地址下的 IPv4/IPv6 通配合并；
 * - 无链接 → `host_port/protocol` + **绑定地址**：回环与外网、UDP 与 TCP 不合并，
 *   否则用户会以为容器只绑了一个地址/一种协议。
 *
 * 合并时不丢弃信息：容器端口不同的项会被记进 `otherContainerPorts`，
 * 由 tooltip 展示（`host_port` 相同而 `container_port` 不同理论上不该出现，
 * 但真出现时不能默默吞掉）。
 */
export function toDisplayItems(
  ports: readonly PublishedPort[] | undefined,
  directAddress: string
): PortDisplayItem[] {
  const byKey = new Map<string, PortDisplayItem>()
  const out: PortDisplayItem[] = []
  for (const p of ports ?? []) {
    if (!Number.isInteger(p.host_port) || p.host_port <= 0) continue
    const url = buildPortUrl(p, directAddress)
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
