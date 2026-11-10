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
 * 脏数据（带 scheme、带 userinfo、带路径端口）**一律返回 null**，
 * 绝不做"顺手修正"。修正出来的链接会指向用户没预期的地方，
 * 比不给链接危险得多——尤其 `javascript:` 这类。
 */
import {
  buildPortUrl,
  isLoopbackBind,
  isUdp,
  isWildcardHost,
  isSctp,
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

const ADDR = '10.0.0.12'

/* ───────── 正常路径 ───────── */

check(
  '通配绑定 + 直连地址 → 用直连地址生成链接',
  buildPortUrl(port('0.0.0.0', 8080, 80, 'tcp'), ADDR) === 'http://10.0.0.12:8080',
  String(buildPortUrl(port('0.0.0.0', 8080, 80, 'tcp'), ADDR))
)
check(
  'IPv6 通配 :: + 直连地址 → 同样用直连地址',
  buildPortUrl(port('::', 8080, 80, 'tcp'), ADDR) === 'http://10.0.0.12:8080'
)
check(
  '具体 host_ip 也用直连地址（用户访问路径由他配置的地址决定）',
  buildPortUrl(port('192.168.1.5', 8080, 80, 'tcp'), ADDR) === 'http://10.0.0.12:8080',
  String(buildPortUrl(port('192.168.1.5', 8080, 80, 'tcp'), ADDR))
)
check(
  '显示的是宿主端口而不是容器端口',
  buildPortUrl(port('0.0.0.0', 18080, 80, 'tcp'), ADDR) === 'http://10.0.0.12:18080'
)
check(
  '域名形式的直连地址可用',
  buildPortUrl(port('0.0.0.0', 8080, 80, 'tcp'), 'app.example.com') === 'http://app.example.com:8080'
)

/* ───────── 非 TCP 一律不给链接 ───────── */

check('UDP 不给链接（浏览器无对应协议）', buildPortUrl(port('0.0.0.0', 53, 53, 'udp'), ADDR) === null)
check('UDP 大小写不敏感', buildPortUrl(port('0.0.0.0', 53, 53, 'UDP'), ADDR) === null)
// 协议里确实有 sctp 这个可能值
check('SCTP 不给链接', buildPortUrl(port('0.0.0.0', 3868, 3868, 'sctp'), ADDR) === null)
check('isUdp 正确识别', isUdp(port('0.0.0.0', 53, 53, 'udp')) === true)
check('isUdp 对 tcp 返回 false', isUdp(port('0.0.0.0', 80, 80, 'tcp')) === false)
check('isSctp 正确识别', isSctp(port('0.0.0.0', 1, 1, 'sctp')) === true)

/* ───────── 回环不给链接 ───────── */

check('127.0.0.1 不给链接', buildPortUrl(port('127.0.0.1', 8080, 80, 'tcp'), ADDR) === null)
check('127.x 变体也不给', buildPortUrl(port('127.5.6.7', 8080, 80, 'tcp'), ADDR) === null)
check('localhost 不给链接', buildPortUrl(port('localhost', 8080, 80, 'tcp'), ADDR) === null)
check('IPv6 回环 ::1 不给链接', buildPortUrl(port('::1', 8080, 80, 'tcp'), ADDR) === null)
check('带方括号的 ::1 也不给', buildPortUrl(port('[::1]', 8080, 80, 'tcp'), ADDR) === null)
check('回环绑定即使有直连地址也不给链接', buildPortUrl(port('127.0.0.1', 8080, 80, 'tcp'), ADDR) === null)

/* ───────── 没配直连地址就不猜 ───────── */

check('直连地址为空 → 不给链接', buildPortUrl(port('0.0.0.0', 8080, 80, 'tcp'), '') === null)
check('直连地址只有空白 → 不给链接', buildPortUrl(port('0.0.0.0', 8080, 80, 'tcp'), '   ') === null)

/* ───────── 端口号有效性 ───────── */

check('端口 0 不给链接', buildPortUrl(port('0.0.0.0', 0, 80, 'tcp'), ADDR) === null)
check('负端口不给', buildPortUrl(port('0.0.0.0', -1, 80, 'tcp'), ADDR) === null)
check('超过 65535 不给', buildPortUrl(port('0.0.0.0', 70000, 80, 'tcp'), ADDR) === null)
check('非整数端口不给', buildPortUrl(port('0.0.0.0', 80.5, 80, 'tcp'), ADDR) === null)

/* ───────── IPv6 地址处理 ───────── */

/*
 * 协议侧直连地址**可能存裸 IPv6**（不带方括号），
 * 而且裸 IPv6 不含 scheme/@/斜杠，等价于"已校验的合法主机"，
 * 所以补方括号是**正确格式化**，不算"默默变形"。
 */
check(
  '裸 IPv6 直连地址补方括号',
  buildPortUrl(port('0.0.0.0', 8080, 80, 'tcp'), '2001:db8::1') === 'http://[2001:db8::1]:8080',
  String(buildPortUrl(port('0.0.0.0', 8080, 80, 'tcp'), '2001:db8::1'))
)
check(
  '已带方括号的 IPv6 不重复包裹',
  buildPortUrl(port('0.0.0.0', 8080, 80, 'tcp'), '[2001:db8::1]') === 'http://[2001:db8::1]:8080'
)

/* ───────── fail closed：脏数据一律 null，绝不修正 ───────── */

/*
 * 这一组是本轮修正的核心。以前的实现会"顺手剥掉" scheme/路径/端口，
 * 结果把 `user@evil.com` 变成指向 evil.com 的合法链接——
 * 用户看到的是一个能点、能打开、但通往别处的地址。
 */
check(
  '带 scheme 的地址不给链接（不剥 http://）',
  buildPortUrl(port('0.0.0.0', 8080, 80, 'tcp'), 'http://10.0.0.12') === null,
  '剥掉 scheme 等于默默变形'
)
check(
  '带 userinfo 的地址不给链接（防 userinfo 注入）',
  buildPortUrl(port('0.0.0.0', 8080, 80, 'tcp'), 'user@evil.com') === null,
  'userinfo 会让人以为访问的是前一段主机'
)
check(
  '带路径的地址不给链接',
  buildPortUrl(port('0.0.0.0', 8080, 80, 'tcp'), '10.0.0.12/foo') === null
)
check(
  '带端口的地址不给链接（端口由映射决定）',
  buildPortUrl(port('0.0.0.0', 8080, 80, 'tcp'), '10.0.0.12:9999') === null,
  '默默换成映射端口会让地址含义不清'
)
check(
  'javascript: 伪协议不给链接',
  buildPortUrl(port('0.0.0.0', 8080, 80, 'tcp'), 'javascript:alert(1)') === null,
  '这类输入绝不能变成可点击链接'
)
check(
  '带查询串的地址不给链接',
  buildPortUrl(port('0.0.0.0', 8080, 80, 'tcp'), '10.0.0.12?a=1') === null
)
check(
  '带片段的地址不给链接',
  buildPortUrl(port('0.0.0.0', 8080, 80, 'tcp'), '10.0.0.12#frag') === null
)
check(
  '带空格的地址不给链接',
  buildPortUrl(port('0.0.0.0', 8080, 80, 'tcp'), '10.0.0.12 evil') === null
)
check(
  '方括号不成对时不给链接',
  buildPortUrl(port('0.0.0.0', 8080, 80, 'tcp'), '[10.0.0.12') === null
)

/* ───────── 通配判定 ───────── */

check('0.0.0.0 判为通配', isWildcardHost('0.0.0.0') === true)
check(':: 判为通配', isWildcardHost('::') === true)
check('空串判为通配', isWildcardHost('') === true)
check('具体 IP 不是通配', isWildcardHost('192.168.1.5') === false)

/* ───────── 回环判定（供界面准确说明「为什么没链接」） ───────── */

/*
 * 这个判定导出给界面用：回环是**可确定**的成因，说清楚比笼统更好；
 * 而"地址是脏数据"前端区分不了，所以不能硬猜成回环。
 */
check('回环: 127.0.0.1 判定为回环', isLoopbackBind('127.0.0.1') === true)
check('回环: localhost 判定为回环', isLoopbackBind('localhost') === true)
check('回环: ::1 判定为回环', isLoopbackBind('::1') === true)
check('回环: [::1] 判定为回环', isLoopbackBind('[::1]') === true)
check('回环: 具体 IP 不是回环', isLoopbackBind('192.168.1.5') === false)
check('回环: 通配地址不是回环', isLoopbackBind('0.0.0.0') === false)
check('回环: 空串不是回环', isLoopbackBind('') === false)

/* ───────── HTTPS 入口：只换协议，不动端口 ───────── */

/*
 * 用户约定 HTTP 为默认、HTTPS 为可选。
 * HTTPS 入口的唯一作用就是把协议换掉，**端口必须原样保留**——
 * 推断成 443 会拼出一个用户根本没监听的地址。
 */
check(
  'HTTPS 入口: 只替换协议，端口不变',
  buildPortUrl(port('0.0.0.0', 8080, 80, 'tcp'), ADDR) === 'http://10.0.0.12:8080',
  '基础链接必须是 http'
)

/* ───────── 显示项去重（双栈重复） ───────── */

/*
 * 实测撞到的真问题：双栈容器同时上报 0.0.0.0:18080 和 :::18080，
 * 两者在同一个 direct_address 下 URL 完全一样，
 * 界面就成了「8080 HTTPS 8080 HTTPS」。
 */
check(
  '去重: IPv4/IPv6 通配指向同一 URL 时合并成一条',
  toDisplayItems(
    [port('0.0.0.0', 18080, 80, 'tcp'), port('::', 18080, 80, 'tcp')],
    ADDR
  ).length === 1,
  '双栈会重复渲染同一个端口'
)
check(
  '去重: 合并后保留第一条（顺序稳定）',
  toDisplayItems(
    [port('0.0.0.0', 18080, 80, 'tcp'), port('::', 18080, 80, 'tcp')],
    ADDR
  )[0].port.host_ip === '0.0.0.0'
)
check(
  '去重: 具体 IP 与通配指向同一 direct_address 也算重复',
  toDisplayItems(
    [port('0.0.0.0', 18080, 80, 'tcp'), port('192.168.1.5', 18080, 80, 'tcp')],
    ADDR
  ).length === 1,
  '两条最终 URL 相同，用户点了没区别'
)

/* 不能过度合并 */
check(
  '去重: 不同端口不合并',
  toDisplayItems(
    [port('0.0.0.0', 18080, 80, 'tcp'), port('0.0.0.0', 19090, 80, 'tcp')],
    ADDR
  ).length === 2
)
check(
  '去重: TCP 与 UDP 不合并（协议不同）',
  toDisplayItems(
    [port('0.0.0.0', 53, 53, 'tcp'), port('0.0.0.0', 53, 53, 'udp')],
    ADDR
  ).length === 2,
  'UDP 和 TCP 是不同服务'
)
check(
  '去重: 回环与外网绑定不合并',
  toDisplayItems(
    [port('127.0.0.1', 8080, 80, 'tcp'), port('0.0.0.0', 8080, 80, 'tcp')],
    ADDR
  ).length === 2,
  '合并会让用户以为只绑了一个地址'
)
check(
  '去重: 没有 direct_address 时两个回环不误合并到通配',
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
  '去重: 无地址时 IPv4/IPv6 通配不合并（绑定地址不同）',
  toDisplayItems(
    [port('0.0.0.0', 8080, 80, 'tcp'), port('::', 8080, 80, 'tcp')],
    ''
  ).length === 2,
  '没有统一地址时它们是两个不同绑定，不能掩盖'
)

/* 其他行为 */
check('去重: 空/undefined 返回空数组', toDisplayItems(undefined, ADDR).length === 0)
check('去重: 过滤非法端口', toDisplayItems([port('0.0.0.0', 0, 80, 'tcp')], ADDR).length === 0)
check(
  '去重: 非 TCP 标记 nonTcp 正确',
  toDisplayItems([port('0.0.0.0', 53, 53, 'udp')], ADDR)[0].nonTcp === true
)
check(
  '去重: TCP 标记 nonTcp 为 false',
  toDisplayItems([port('0.0.0.0', 80, 80, 'tcp')], ADDR)[0].nonTcp === false
)

/*
 * 合并时不能丢信息：host_port 相同而 container_port 不同理论上不该出现
 * （daemon 会让第二条起不来），但真出现时 tooltip 必须能说出来，
 * 否则用户排查端口冲突时会被误导成「只有一条映射」。
 */
check(
  '去重: 合并后记录其它容器端口',
  (() => {
    const items = toDisplayItems(
      [
        port('0.0.0.0', 8080, 80, 'tcp'),
        port('::', 8080, 8081, 'tcp')
      ],
      ADDR
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
      ADDR
    )
    return items.length === 1 && items[0].otherContainerPorts.length === 0
  })()
)
check(
  '去重: 未发生合并时 otherContainerPorts 为空数组',
  toDisplayItems([port('0.0.0.0', 8080, 80, 'tcp')], ADDR)[0].otherContainerPorts.length === 0
)

/* ───────── 结果 ───────── */

if (failures.length > 0) {
  console.error(`\n✗ 端口链接测试：${passed} 通过，${failures.length} 失败\n`)
  for (const f of failures) console.error(`  ✗ ${f}`)
  process.exit(1)
}
console.log(`\n✓ 端口链接测试：${passed} 通过，0 失败`)
