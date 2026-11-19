/**
 * 端口链接推导测试
 *
 * 纯 Node，无框架。跑法：node tests/port-links.contract.test.mjs
 *
 * ## 这份测试守的是什么
 *
 * 拼错链接的后果不是难看，而是给用户一个**打不开的地址**，
 * 而且他不会怀疑链接，只会以为服务挂了。
 *
 * ## 核心原则：fail closed
 *
 * 脏数据（带 userinfo、带路径查询）**一律返回 null**，绝不做"顺手修正"。
 * 修正出来的链接会指向用户没预期的地方，比不给链接危险得多——
 * 尤其 `javascript:` 这类。
 *
 * ## 2026-11 的语义（本轮需求）
 *
 * 1. 服务器访问地址是**带协议前缀**的完整地址（`https://x.5ddd.com`），
 *    协议由用户配置决定，不再由前端固定 `http`；
 * 2. 配置里显式带端口 → 原样使用（一个域名反代多个宿主端口的场景）；
 *    只配域名 → 把映射的宿主端口接在主机后面；
 * 3. 「HTTPS 再试一次」入口与「@当前访问地址」说明行都已删除，
 *    下面用显式断言守住它们不会悄悄回来。
 */
import {
  buildPortUrl,
  inferAccessFromLocation,
  isLoopbackBind,
  isUdp,
  isWildcardHost,
  isSctp,
  parseAccessOrigin,
  resolvePortAccess,
  toDisplayItems
} from '../src/utils/portLinks.ts'

let passed = 0
const failures = []

function check(name, ok, detail) {
  if (ok) {
    passed += 1
  } else {
    failures.push(detail === undefined ? name : `${name} —— ${detail}`)
  }
}

/** 造一条端口映射（协议侧 host_port 是 uint16） */
function port(host_ip, host_port, container_port, protocol) {
  return { host_ip, host_port, container_port, protocol }
}

const HTTPS = 'https://ac8b255bfcf6-0.aynastv.5ddd.com'

/* ───────── 正常路径：域名 + 宿主端口 ───────── */

check(
  '通配绑定 + https 域名 → 协议沿用配置，端口用宿主端口',
  buildPortUrl(port('0.0.0.0', 38517, 8080, 'tcp'), HTTPS) === `${HTTPS}:38517`,
  String(buildPortUrl(port('0.0.0.0', 38517, 8080, 'tcp'), HTTPS))
)
check(
  'IPv6 通配 :: 同样使用配置的访问地址',
  buildPortUrl(port('::', 38517, 8080, 'tcp'), HTTPS) === `${HTTPS}:38517`
)
check(
  '具体 host_ip 也用配置的访问地址（用户访问路径由他配置的地址决定）',
  buildPortUrl(port('192.168.1.5', 8080, 80, 'tcp'), 'https://app.example.com') ===
    'https://app.example.com:8080'
)
check(
  'http 前缀同样可用',
  buildPortUrl(port('0.0.0.0', 9000, 80, 'tcp'), 'http://10.0.0.12') === 'http://10.0.0.12:9000'
)

/* ───────── 配置里显式带端口：端口以配置为准 ───────── */

/*
 * 用户可能把整条反代地址配进来：一个域名对应多个宿主端口时，
 * 每个端口都有自己的域名/端口前缀。这时再拼映射端口就错了。
 */
check(
  '配置了端口 → 原样使用，不追加宿主端口',
  buildPortUrl(port('0.0.0.0', 38517, 8080, 'tcp'), 'https://x.5ddd.com:8443') ===
    'https://x.5ddd.com:8443',
  String(buildPortUrl(port('0.0.0.0', 38517, 8080, 'tcp'), 'https://x.5ddd.com:8443'))
)
check(
  '配置了端口时不同宿主端口指向同一地址',
  buildPortUrl(port('0.0.0.0', 38517, 8080, 'tcp'), 'https://x.5ddd.com:443') ===
    buildPortUrl(port('0.0.0.0', 13080, 8080, 'tcp'), 'https://x.5ddd.com:443')
)
check(
  '结尾斜杠被容忍（复制地址栏常见）',
  buildPortUrl(port('0.0.0.0', 8080, 80, 'tcp'), 'https://example.com/') ===
    'https://example.com:8080'
)

/* ───────── IPv6 访问地址 ───────── */

check(
  '裸 IPv6 访问地址补方括号',
  buildPortUrl(port('0.0.0.0', 8080, 80, 'tcp'), 'http://2001:db8::1') ===
    'http://[2001:db8::1]:8080',
  String(buildPortUrl(port('0.0.0.0', 8080, 80, 'tcp'), 'http://2001:db8::1'))
)
check(
  '带方括号的 IPv6 不重复包裹',
  buildPortUrl(port('0.0.0.0', 8080, 80, 'tcp'), 'http://[2001:db8::1]') ===
    'http://[2001:db8::1]:8080'
)
check(
  '方括号 IPv6 + 端口原样保留',
  parseAccessOrigin('https://[2001:db8::1]:8443')?.origin === 'https://[2001:db8::1]:8443'
)

/* ───────── fail closed：脏数据一律 null，绝不修正 ───────── */

/*
 * 这一组是核心防线。以前实现会"顺手剥掉" scheme/路径/端口，
 * 结果把 `user@evil.com` 变成指向 evil.com 的合法链接——
 * 用户看到的是一个能点、能打开、但通往别处的地址。
 */
check(
  '不带协议的地址不给链接（要求配置完整地址）',
  buildPortUrl(port('0.0.0.0', 8080, 80, 'tcp'), 'example.com') === null,
  '用户明确要求带 http/https 前缀'
)
check(
  '旧的纯主机配置不再可用',
  buildPortUrl(port('0.0.0.0', 8080, 80, 'tcp'), '10.0.0.12') === null
)
check(
  '带 userinfo 的地址不给链接（防 userinfo 注入）',
  buildPortUrl(port('0.0.0.0', 8080, 80, 'tcp'), 'https://user@evil.com') === null,
  'userinfo 会让人以为访问的是前一段主机'
)
check(
  '带路径的地址不给链接',
  buildPortUrl(port('0.0.0.0', 8080, 80, 'tcp'), 'https://example.com/foo') === null
)
check(
  '带查询串的地址不给链接',
  buildPortUrl(port('0.0.0.0', 8080, 80, 'tcp'), 'https://example.com?a=1') === null
)
check(
  'javascript: 伪协议不给链接',
  buildPortUrl(port('0.0.0.0', 8080, 80, 'tcp'), 'javascript:alert(1)') === null,
  '这类输入绝不能变成可点击链接'
)
check(
  '带空格的地址不给链接',
  buildPortUrl(port('0.0.0.0', 8080, 80, 'tcp'), 'https://exa mple.com') === null
)
check(
  '端口越界不给链接',
  buildPortUrl(port('0.0.0.0', 8080, 80, 'tcp'), 'https://example.com:70000') === null
)
check(
  '端口非数字不给链接',
  buildPortUrl(port('0.0.0.0', 8080, 80, 'tcp'), 'https://example.com:abc') === null
)
check(
  '方括号不成对时不给链接',
  buildPortUrl(port('0.0.0.0', 8080, 80, 'tcp'), 'https://[10.0.0.12') === null
)
check(
  '空配置不给链接',
  buildPortUrl(port('0.0.0.0', 8080, 80, 'tcp'), '') === null
)
check('空串解析为 null', parseAccessOrigin('   ') === null)
check('只写协议不给链接', parseAccessOrigin('https://') === null)

/* ───────── 非 TCP 与回环 ───────── */

check(
  'UDP 不给链接',
  buildPortUrl(port('0.0.0.0', 53, 53, 'udp'), HTTPS) === null
)
check(
  'SCTP 不给链接',
  buildPortUrl(port('0.0.0.0', 9000, 9000, 'sctp'), HTTPS) === null
)
check(
  '回环绑定不给链接（即使配了访问地址）',
  buildPortUrl(port('127.0.0.1', 8080, 80, 'tcp'), HTTPS) === null
)
check(
  '回环: 127.0.0.1 判定为回环',
  isLoopbackBind('127.0.0.1') === true
)
check('回环: localhost 判定为回环', isLoopbackBind('localhost') === true)
check('回环: ::1 判定为回环', isLoopbackBind('::1') === true)
check('回环: [::1] 判定为回环', isLoopbackBind('[::1]') === true)
check('回环: 具体 IP 不是回环', isLoopbackBind('192.168.1.5') === false)
check('回环: 通配地址不是回环', isLoopbackBind('0.0.0.0') === false)
check('回环: 空串不是回环', isLoopbackBind('') === false)

check('0.0.0.0 判为通配', isWildcardHost('0.0.0.0') === true)
check(':: 判为通配', isWildcardHost('::') === true)
check('空串判为通配', isWildcardHost('') === true)
check('具体 IP 不是通配', isWildcardHost('192.168.1.5') === false)

/* ───────── 本轮删除的两个 UI 元素：不要再回来 ───────── */

check(
  '端口链接不再固定 http：https 配置生成 https 链接',
  buildPortUrl(port('0.0.0.0', 8080, 80, 'tcp'), HTTPS).startsWith('https://')
)

/* ───────── 显示项去重（双栈重复） ───────── */

/*
 * 实测撞到的真问题：双栈容器同时上报 0.0.0.0:18080 和 :::18080，
 * 两者在同一个访问地址下 URL 完全一样，界面就成了同一条重复两遍。
 */
check(
  '去重: IPv4/IPv6 通配指向同一 URL 时合并成一条',
  toDisplayItems(
    [port('0.0.0.0', 18080, 80, 'tcp'), port('::', 18080, 80, 'tcp')],
    HTTPS
  ).length === 1,
  '双栈会重复渲染同一个端口'
)
check(
  '去重: 合并后保留第一条（顺序稳定）',
  toDisplayItems(
    [port('0.0.0.0', 18080, 80, 'tcp'), port('::', 18080, 80, 'tcp')],
    HTTPS
  )[0].port.host_ip === '0.0.0.0'
)
check(
  '去重: 具体 IP 与通配指向同一地址也算重复',
  toDisplayItems(
    [port('0.0.0.0', 18080, 80, 'tcp'), port('192.168.1.5', 18080, 80, 'tcp')],
    HTTPS
  ).length === 1,
  '两条最终 URL 相同，用户点了没区别'
)

/* 不能过度合并 */
check(
  '去重: 不同端口不合并',
  toDisplayItems(
    [port('0.0.0.0', 18080, 80, 'tcp'), port('0.0.0.0', 19090, 80, 'tcp')],
    HTTPS
  ).length === 2
)
check(
  '去重: TCP 与 UDP 不合并（协议不同）',
  toDisplayItems(
    [port('0.0.0.0', 53, 53, 'tcp'), port('0.0.0.0', 53, 53, 'udp')],
    HTTPS
  ).length === 2,
  'UDP 和 TCP 是不同服务'
)
check(
  '去重: 回环与外网绑定不合并',
  toDisplayItems(
    [port('127.0.0.1', 8080, 80, 'tcp'), port('0.0.0.0', 8080, 80, 'tcp')],
    HTTPS
  ).length === 2,
  '合并会让用户以为只绑了一个地址'
)
check(
  '去重: 没有访问地址时两个回环不误合并到通配',
  toDisplayItems(
    [port('127.0.0.1', 8080, 80, 'tcp'), port('0.0.0.0', 8080, 80, 'tcp')],
    ''
  ).length === 2,
  '无链接时按绑定地址区分，二者不同'
)
check(
  '去重: 无地址时同样的回环重复项仍合并',
  toDisplayItems(
    [port('127.0.0.1', 8080, 80, 'tcp'), port('127.0.0.1', 8080, 80, 'tcp')],
    ''
  ).length === 1
)
check(
  '去重: 无地址时 IPv4/IPv6 的同一通配映射只显示一次',
  toDisplayItems(
    [port('0.0.0.0', 8080, 80, 'tcp'), port('::', 8080, 80, 'tcp')],
    ''
  ).length === 1
)

/* 其他行为 */
check('去重: 空/undefined 返回空数组', toDisplayItems(undefined, HTTPS).length === 0)
check('去重: 过滤非法端口', toDisplayItems([port('0.0.0.0', 0, 80, 'tcp')], HTTPS).length === 0)
check(
  '去重: 非 TCP 标记 nonTcp 正确',
  toDisplayItems([port('0.0.0.0', 53, 53, 'udp')], HTTPS)[0].nonTcp === true
)
check(
  '去重: TCP 标记 nonTcp 为 false',
  toDisplayItems([port('0.0.0.0', 80, 80, 'tcp')], HTTPS)[0].nonTcp === false
)
check('isUdp 判定 udp', isUdp(port('0.0.0.0', 53, 53, 'UDP')) === true)
check('isSctp 判定 sctp', isSctp(port('0.0.0.0', 1, 1, 'sctp')) === true)

/*
 * 合并时不能丢信息：host_port 相同而 container_port 不同理论上不该出现
 * （daemon 会让第二条起不来），但真出现时 tooltip 必须能说出来。
 */
check(
  '去重: 合并后记录其它容器端口',
  (() => {
    const items = toDisplayItems(
      [port('0.0.0.0', 8080, 80, 'tcp'), port('::', 8080, 8081, 'tcp')],
      HTTPS
    )
    return items.length === 1 && items[0].otherContainerPorts.join(',') === '8081'
  })(),
  '合并掉不同的容器端口会让 tooltip 丢失冲突信息'
)
check(
  '去重: 容器端口相同的重复项不产生额外记录',
  (() => {
    const items = toDisplayItems(
      [port('0.0.0.0', 8080, 80, 'tcp'), port('::', 8080, 80, 'tcp')],
      HTTPS
    )
    return items.length === 1 && items[0].otherContainerPorts.length === 0
  })()
)

/* ───────── 访问地址解析与兜底（resolvePortAccess） ───────── */

check(
  '访问地址解析: 配置了完整地址 → 有 origin 和 host',
  (() => {
    const r = resolvePortAccess(HTTPS, 'remote')
    return r !== null && r.origin === HTTPS && r.host === 'ac8b255bfcf6-0.aynastv.5ddd.com'
  })()
)
check(
  '访问地址解析: 远端服务器未配置且无浏览器环境 → null（不给链接）',
  resolvePortAccess('', 'remote') === null
)
check(
  '访问地址解析: 配了但格式不可用 → null，不悄悄换成别的地址',
  resolvePortAccess('example.com', 'agent-local') === null,
  '旧数据没协议前缀时提示补全，而不是回退到当前访问地址'
)
check(
  '访问地址解析: IPv6 保留方括号',
  resolvePortAccess('https://[2001:db8::1]', 'remote')?.host === '[2001:db8::1]'
)
check('推断地址: 非浏览器环境返回 null', inferAccessFromLocation() === null)

/*
 * 用户要求：没配置就自动用「上报的地址」兜底（内置服务器 = 当前访问地址）。
 * 这里模拟浏览器环境验证兜底链路，并确认远端服务器不会被误导到 Hub 的地址。
 */
const priorWindow = Object.getOwnPropertyDescriptor(globalThis, 'window')
try {
  Object.defineProperty(globalThis, 'window', {
    configurable: true,
    value: { location: { hostname: 'ac8b255bfcf6-0.aynastv.5ddd.com', protocol: 'https:' } }
  })
  const inferred = resolvePortAccess('', 'agent-local')
  check(
    '内置服务器未配置时自动用当前访问地址兜底',
    inferred !== null && inferred.origin === HTTPS,
    String(inferred && inferred.origin)
  )
  check(
    '兜底时保留 https 协议，不降级成 http',
    inferred !== null && inferred.scheme === 'https'
  )
  check(
    '远端服务器不会误用 Hub 的访问地址',
    resolvePortAccess('', 'remote') === null,
    '那会指向 Hub 自己的端口'
  )
  const items = toDisplayItems(
    [port('0.0.0.0', 38517, 8080, 'tcp'), port('::', 38517, 8080, 'tcp')],
    inferred
  )
  check(
    '兜底时双栈映射仅显示一次，打开宿主端口',
    items.length === 1 && items[0].url === `${HTTPS}:38517`,
    String(items[0] && items[0].url)
  )
  check(
    '兜底地址是回环时不生成链接',
    (() => {
      Object.defineProperty(globalThis, 'window', {
        configurable: true,
        value: { location: { hostname: '127.0.0.1', protocol: 'http:' } }
      })
      return resolvePortAccess('', 'agent-local') === null
    })(),
    '回环是用户自己的机器，不是服务器'
  )
} finally {
  if (priorWindow === undefined) delete globalThis.window
  else Object.defineProperty(globalThis, 'window', priorWindow)
}

/* 汇总 */

if (failures.length > 0) {
  console.error(`\n✗ 端口链接测试：${passed} 通过，${failures.length} 失败\n`)
  for (const f of failures) console.error(`  ✗ ${f}`)
  process.exit(1)
}
console.log(`\n✓ 端口链接测试：${passed} 通过，0 失败`)
