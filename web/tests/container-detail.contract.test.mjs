/**
 * 容器详情 + 服务器直连地址 契约测试
 *
 * 纯 Node，无框架。跑法：node tests/container-detail.contract.test.mjs
 *
 * 覆盖重点（都是「猜错会误导用户」的地方）：
 * - 直连地址只透传不猜，格式交后端校验
 * - 旧 Agent 没有 published_ports 时**不解析 ports 文本猜链接**
 * - 容器详情归一化：null 不等于 0（退出码/资源限制）
 * - 详情 manage / 日志 view 的双权限组合
 * - 契约里没有 Env，前端也不反推
 */
import { registerHooks } from 'node:module'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { dirname, join } from 'node:path'

const here = dirname(fileURLToPath(import.meta.url))

let passed = 0
const failures = []

function check(name, ok, detail) {
  if (ok) {
    passed += 1
  } else {
    failures.push(detail === undefined ? name : `${name} —— ${detail}`)
  }
}

/* ───────── 桩：替换 api/request ───────── */

let lastCall = null

const requestStub = `
function take() {
  const err = globalThis.__CD_STUB__.error
  globalThis.__CD_STUB__.error = null
  if (err !== null) throw err
  return globalThis.__CD_STUB__.response
}
export async function httpGet(url) {
  globalThis.__CD_STUB__.record({ method: 'GET', url })
  return take()
}
export async function httpPost(url, body) {
  globalThis.__CD_STUB__.record({ method: 'POST', url, body })
  return take()
}
export async function httpPut(url, body) {
  globalThis.__CD_STUB__.record({ method: 'PUT', url, body })
  return take()
}
export async function httpDelete(url) {
  globalThis.__CD_STUB__.record({ method: 'DELETE', url })
  return take()
}
export async function httpPatch(url, body) {
  globalThis.__CD_STUB__.record({ method: 'PATCH', url, body })
  return take()
}
`

const stubUrl = `data:text/javascript;base64,${Buffer.from(requestStub, 'utf8').toString('base64')}`

registerHooks({
  resolve(specifier, context, nextResolve) {
    // 两个被测模块都用同一个桩：它们是两个独立的模块实例，
    // 但共享同一个 globalThis 状态，所以指向同一 URL 即可
    if (
      specifier === './request' &&
      (context.parentURL?.includes('/api/containers.ts') ||
        context.parentURL?.includes('/api/agents.ts'))
    ) {
      return { url: stubUrl, shortCircuit: true }
    }
    return nextResolve(specifier, context)
  }
})

globalThis.__CD_STUB__ = {
  response: null,
  error: null,
  record: (call) => {
    lastCall = call
  }
}

function setResponse(value) {
  globalThis.__CD_STUB__.response = value
  globalThis.__CD_STUB__.error = null
}
function setError(err) {
  globalThis.__CD_STUB__.error = err
}

const containersApi = await import('../src/api/containers.ts')
const agentsApi = await import('../src/api/agents.ts')
const portLinksModule = await import('../src/utils/portLinks.ts')
const { ApiError } = await import('../src/types/api.ts')

/* ───────── 容器详情：路径与归一化 ───────── */

/*
 * 这份 fixture **逐字段对齐** protocol/envelope/payload_container_detail.go：
 * - created / started_at / finished_at 都是 RFC3339 字符串（不是 Unix 秒！）
 * - exposed_ports 是 []string，形如 "80/tcp"
 * - resource_limits 用 0 表示"未设置上限"，pids_limit 是可空指针
 * - exit_code / restart_count 是 int，不是指针
 */
/**
 * 造一个 `{ container: ... }` 包装响应——与后端实际返回一致。
 * 这一层包装本身也要被测到：漏解包会让页面拿到 undefined 全空。
 */
function wrapped(fields) {
  return { container: fields }
}

setResponse(
  wrapped({
    id: 'abc123',
    name: 'web-1',
    image: 'nginx:latest',
    state: 'running',
    status: 'Up 3 hours',
    created: '2024-01-15T10:30:00Z',
    started_at: '2024-01-15T10:31:00Z',
    finished_at: '',
    exit_code: 0,
    restart_count: 2,
    network_mode: 'bridge',
    command: ['nginx', '-g', 'daemon off;'],
    networks: [{ name: 'bridge', ip_address: '172.17.0.2' }],
    mounts: [{ type: 'volume', source: 'data', destination: '/data', read_only: true }],
    published_ports: [
      { host_ip: '0.0.0.0', host_port: 8080, container_port: 80, protocol: 'tcp' }
    ],
    exposed_ports: ['80/tcp', '443/tcp'],
    resource_limits: { memory_bytes: 536870912, nano_cpus: 1500000000, pids_limit: 100 }
  })
)

const detail = await containersApi.getContainer('ag-1', 'abc123')

check(
  '详情: 路径 /agents/{id}/containers/{cid}',
  lastCall.url === '/agents/ag-1/containers/abc123',
  String(lastCall.url)
)
check('详情: 方法 GET', lastCall.method === 'GET')

/* 解包：响应套了 { container: ... } */
check(
  '详情: 正确解包 { container } 包装',
  detail.id === 'abc123' && detail.name === 'web-1',
  '漏解包会让所有字段变空'
)

setResponse(wrapped({ id: 'a/b' }))
await containersApi.getContainer('ag-1', 'a/b')
check('详情: 容器 ID 编码', lastCall.url === '/agents/ag-1/containers/a%2Fb', String(lastCall.url))

setResponse(
  wrapped({
    id: 'x',
    created: '2024-01-15T10:30:00Z',
    started_at: '2024-01-15T10:31:00Z',
    finished_at: ''
  })
)
const d1 = await containersApi.getContainer('ag-1', 'x')

/*
 * 最关键的一条：created 是 RFC3339 字符串。
 * 以前按 Unix 秒处理会对字符串做 Number() 得到 NaN → 归零 → 显示 1970。
 */
check(
  '详情: created 保持 RFC3339 字符串（不转数字）',
  d1.created === '2024-01-15T10:30:00Z',
  `实际 ${String(d1.created)} —— 转成数字会把它变成 1970`
)
check('详情: created 不是数字类型', typeof d1.created === 'string', typeof d1.created)
check(
  '详情: started_at 保持字符串',
  d1.started_at === '2024-01-15T10:31:00Z',
  String(d1.started_at)
)
check('详情: 零值 finished_at 归一成空串', d1.finished_at === '', JSON.stringify(d1.finished_at))

// Go 的零值时间也要归一成空串，否则界面显示 0001 年
setResponse(wrapped({ id: 'x', finished_at: '0001-01-01T00:00:00Z', created: '2024-01-15T10:30:00Z' }))
const zero = await containersApi.getContainer('ag-1', 'x')
check('详情: Go 零值时间归一成空串', zero.finished_at === '', zero.finished_at)
check('详情: created 不合法时也不崩（保留空串）', zero.created === '2024-01-15T10:30:00Z')

check('详情: command 保留数组', detail.command.length === 3 && detail.command[0] === 'nginx')
check('详情: networks 解出', detail.networks.length === 1 && detail.networks[0].ip_address === '172.17.0.2')
check('详情: mounts 解出且 read_only', detail.mounts.length === 1 && detail.mounts[0].read_only === true)
check('详情: published_ports 解出', detail.published_ports.length === 1)
check(
  '详情: exposed_ports 是字符串数组（"80/tcp" 形式）',
  detail.exposed_ports.length === 2 && detail.exposed_ports[0] === '80/tcp',
  JSON.stringify(detail.exposed_ports)
)
check('详情: exit_code 是 int 且保留 0', detail.exit_code === 0, String(detail.exit_code))
check('详情: restart_count 保留', detail.restart_count === 2)
check('详情: memory_bytes 保留', detail.resource_limits.memory_bytes === 536870912)
check('详情: nano_cpus 保留', detail.resource_limits.nano_cpus === 1500000000)
check('详情: pids_limit 保留', detail.resource_limits.pids_limit === 100)

/*
 * 资源上限的 0 表示"未设置上限"（协议注释明确），
 * 必须原样保留 0 交给界面翻译成「未限制」，不能在这里改成别的值。
 */
setResponse(
  wrapped({
    id: 'x',
    created: '2024-01-15T10:30:00Z',
    resource_limits: { memory_bytes: 0, nano_cpus: 0 }
  })
)
const noLimit = await containersApi.getContainer('ag-1', 'x')
check('详情: memory_bytes=0 原样保留（0 = 未限制）', noLimit.resource_limits.memory_bytes === 0)
check('详情: nano_cpus=0 原样保留（0 = 未限制）', noLimit.resource_limits.nano_cpus === 0)
check('详情: 缺 pids_limit → null（指针语义）', noLimit.resource_limits.pids_limit === null)

/* 缺字段一律保守，不崩 */
setResponse(wrapped({ id: 'x' }))
const sparse = await containersApi.getContainer('ag-1', 'x')
check('详情: 缺 mounts → 空数组', Array.isArray(sparse.mounts) && sparse.mounts.length === 0)
check('详情: 缺 command → 空数组', Array.isArray(sparse.command) && sparse.command.length === 0)
check('详情: 缺 exposed_ports → 空数组', Array.isArray(sparse.exposed_ports) && sparse.exposed_ports.length === 0)
check('详情: 缺 created → 空串而不是 0', sparse.created === '', JSON.stringify(sparse.created))

/* 详情 403 是权限，调用方要能区分 */
setError(new ApiError(403, 'forbidden', '无权限'))
let status = 0
try {
  await containersApi.getContainer('ag-1', 'x')
} catch (err) {
  status = err.status ?? 0
}
check('详情: 403 原样抛出（页面据此显示权限提示而非故障）', status === 403)

/* ───────── 直连地址：只透传，不猜格式 ───────── */

setResponse({ agent: { id: 'ag-1', name: 'n', status: 'online', version: '1', runtime: 'docker', runtime_version: '1', last_seen: 0, labels: {}, direct_address: '10.0.0.12' } })
const updated = await agentsApi.updateAgent('ag-1', { direct_address: '10.0.0.12' })

check('直连地址: 走 PATCH /agents/{id}', lastCall.method === 'PATCH' && lastCall.url === '/agents/ag-1')
check('直连地址: 原样提交', lastCall.body.direct_address === '10.0.0.12', String(lastCall.body.direct_address))
check('直连地址: 后端返回值带回', updated.direct_address === '10.0.0.12')

/* 清空要如实提交空串，不能省略（省略等于「不改」） */
setResponse({ agent: { id: 'ag-1', name: 'n', status: 'online', version: '1', runtime: 'docker', runtime_version: '1', last_seen: 0, labels: {}, direct_address: '' } })
await agentsApi.updateAgent('ag-1', { direct_address: '' })
check('直连地址: 清空时提交空串而不是省略字段', lastCall.body.direct_address === '')

/* IPv6 方括号原样透传，前端不改造 */
setResponse({ agent: { id: 'ag-1', name: 'n', status: 'online', version: '1', runtime: 'docker', runtime_version: '1', last_seen: 0, labels: {}, direct_address: '[2001:db8::1]' } })
await agentsApi.updateAgent('ag-1', { direct_address: '[2001:db8::1]' })
check('直连地址: IPv6 方括号形式原样提交', lastCall.body.direct_address === '[2001:db8::1]')

/* 格式错误由后端拒绝，前端展示原文 */
setError(new ApiError(400, 'bad_request', '直连地址不允许包含端口'))
let badStatus = 0
let badMessage = ''
try {
  await agentsApi.updateAgent('ag-1', { direct_address: '10.0.0.12:8080' })
} catch (err) {
  badStatus = err.status ?? 0
  badMessage = err.message
}
check('直连地址: 后端校验失败原样抛出', badStatus === 400, String(badStatus))
check('直连地址: 保留后端错误原文', badMessage === '直连地址不允许包含端口', badMessage)

/* ───────── 源码级断言：不能解析 ports 文本 ───────── */

const portCellSrc = readFileSync(
  join(here, '..', 'src', 'components', 'container', 'ContainerPortCell.vue'),
  'utf8'
)
const listSrc = readFileSync(join(here, '..', 'src', 'views', 'ContainerListView.vue'), 'utf8')
const detailViewSrc = readFileSync(
  join(here, '..', 'src', 'views', 'ContainerDetailView.vue'),
  'utf8'
)
const editModalSrc = readFileSync(
  join(here, '..', 'src', 'components', 'agent', 'AgentEditModal.vue'),
  'utf8'
)

/*
 * 最危险的一条：拿 `ports` 文本 split('->') 猜端口。
 * 文本格式在 docker 版本间有差异，猜错就给一个打不开的链接。
 */
check(
  '端口: 不解析 ports 文本猜链接',
  !/split\(\s*['"]->['"]\s*\)/.test(portCellSrc) && !/ports\s*\.\s*split/.test(portCellSrc),
  '在解析 ports 文本，旧 Agent 场景会猜出错误链接'
)
check(
  '端口: 只消费结构化 published_ports',
  /published_ports/.test(portCellSrc)
)
check(
  '端口: 无数据时文案不断言服务器版本',
  !/服务器版本较旧/.test(portCellSrc) && /无已上报的端口映射/.test(portCellSrc),
  'published_ports 带 omitempty，无映射和旧 Agent 都是 undefined，无法区分'
)
check(
  '端口: 提供轻量 HTTPS 入口',
  /httpsUrl/.test(portCellSrc) && /https:\/\//.test(portCellSrc),
  '用户约定 http 默认、https 可选，需要入口'
)
check(
  '端口: HTTPS 入口文案是 HTTPS 而非单个字母',
  />HTTPS<\/a>/.test(portCellSrc.replace(/\s+/g, ' ')) || />\s*HTTPS\s*<\/a>/.test(portCellSrc.replace(/\s+/g, ' ')),
  '「s」太含糊，用户猜不到是 HTTPS'
)
/*
 * 没链接时不能一律声称「绑定回环」：
 * url 为 null 的成因有多种（未配置 / 脏地址 / 回环 / 端口非法），
 * 前端只有"有没有链接"这一个结果，只能对回环这种**可判定**的成因说死。
 */
check(
  '端口: 非回环成因给合并说法而不硬猜回环',
  /未配置有效直连地址或绑定回环，不生成链接/.test(portCellSrc),
  '笼统地说成回环是错误断言'
)
check(
  '端口: 回环用可判定函数而非字符串比较',
  /isLoopbackBind/.test(portCellSrc)
)
check(
  '端口: tooltip 展示被合并的其它容器端口',
  /otherContainerPorts/.test(portCellSrc),
  '合并时不说出冲突，排查端口时会被误导'
)
/*
 * HTTPS 入口必须只换协议、不改端口。
 * 断言只看代码行（剥掉行注释与块注释），因为注释里本来就会提到"不推断 443"。
 */
const portCellCode = portCellSrc
  .replace(/<!--[\s\S]*?-->/g, '')
  .replace(/\/\*[\s\S]*?\*\//g, '')
  .replace(/^\s*\*.*$/gm, '')
  .replace(/\/\/[^\n]*/g, '')
check(
  '端口: HTTPS 入口不改端口（不出现 443）',
  !/443/.test(portCellCode),
  '固定推断 443 会拼出错误地址'
)
check(
  '端口: 通过 toDisplayItems 统一推导（内含 buildPortUrl + 去重）',
  /toDisplayItems/.test(portCellSrc),
  '链接规则与去重必须都走同一个纯函数，不能组件里各写一份'
)
check(
  '端口: 去重逻辑在 portLinks 里（可单测）',
  /export function toDisplayItems/.test(
    readFileSync(join(here, '..', 'src', 'utils', 'portLinks.ts'), 'utf8')
  )
)
/*
 * 回归：双栈（0.0.0.0 + ::）曾导致「18080 HTTPS 18080 HTTPS」。
 * 断到具体行为上，而不只是"调用了某个函数"。
 */
check(
  '端口: 双栈重复项只渲染一条',
  (() => {
    const { toDisplayItems: t } = portLinksModule
    const dual = [
      { host_ip: '0.0.0.0', host_port: 18080, container_port: 80, protocol: 'tcp' },
      { host_ip: '::', host_port: 18080, container_port: 80, protocol: 'tcp' }
    ]
    return t(dual, '10.0.0.12').length === 1
  })(),
  '双栈会渲染成两条一模一样的端口'
)
check(
  '端口: 回环与外网同端口不被去重合并',
  (() => {
    const { toDisplayItems: t } = portLinksModule
    const mixed = [
      { host_ip: '127.0.0.1', host_port: 8080, container_port: 80, protocol: 'tcp' },
      { host_ip: '0.0.0.0', host_port: 8080, container_port: 80, protocol: 'tcp' }
    ]
    return t(mixed, '10.0.0.12').length === 2
  })(),
  '合并会让用户以为只绑了一个地址'
)

/*
 * fail closed：portLinks 里不允许出现"剥掉 scheme/路径/端口"的修正逻辑。
 * 历史上那种写法会把 user@evil.com 变成指向 evil.com 的合法链接。
 */
const portLinksSrc = readFileSync(join(here, '..', 'src', 'utils', 'portLinks.ts'), 'utf8')
check(
  '端口: 不剥掉 http(s):// 前缀（脏数据应 fail closed）',
  !/replace\(\s*\/\^https\?/.test(portLinksSrc),
  '剥前缀等于默默变形，会给用户一个指向别处的地址'
)
check(
  '端口: 不用 split 剥路径/端口',
  !/split\(\s*['"]\/['"]\s*\)/.test(portLinksSrc),
  '修正脏数据会掩盖配置错误'
)
check(
  '端口: 有主机字符白名单（挡 @ / ? # 等）',
  /HOST_CHARSET/.test(portLinksSrc),
  '缺少白名单时 userinfo 注入可绕过'
)
check(
  '端口: 校验直连地址后才拼链接',
  /sanitizeHost\(/.test(portLinksSrc)
)
check(
  '端口: 非 tcp 一律不给链接',
  /!== 'tcp'\)\s*return null/.test(portLinksSrc)
)
check(
  '端口: 列表页不再直接渲染 ports 文本',
  !/render: \(row\) => row\.ports/.test(listSrc),
  '列表仍在显示原始 ports 文本'
)

/* 容器名称 → 详情路由 */
check('列表: 容器名称可点击进详情', /goDetail/.test(listSrc))
check('列表: 使用独立详情路由', /agent-container-detail/.test(listSrc))
check(
  '列表: 详情路由位于 containers 之后',
  (() => {
    const r = readFileSync(join(here, '..', 'src', 'router', 'index.ts'), 'utf8')
    const list = r.indexOf("path: 'containers',")
    const det = r.indexOf("path: 'containers/:containerId'")
    return list > -1 && det > -1 && list < det
  })()
)

/* 双权限：详情 403 也要能看日志 */
check('详情: 403 与故障分开处理', /forbidden\.value = true/.test(detailViewSrc))
check(
  '详情: 403 时明确告知日志仍可看',
  /日志不受影响/.test(detailViewSrc),
  '用户会以为整页都用不了'
)
check('详情: 日志是独立 tab', /name="logs"/.test(detailViewSrc))
check(
  '详情: 日志走 view 权限的日志接口',
  /fetchContainerLogs/.test(
    readFileSync(
      join(here, '..', 'src', 'components', 'container', 'ContainerDetailLogs.vue'),
      'utf8'
    )
  )
)

/* 契约无 Env，也不反推 */
/*
 * 检查前先剥掉注释和 script 段：注释里写「契约里没有 Env」是说明，
 * 模板里真去渲染环境变量才是问题。直接全文匹配会把注释当成违规。
 */
const detailMarkup = detailViewSrc
  .slice(detailViewSrc.indexOf('<template>'))
  .replace(/<!--[\s\S]*?-->/g, '')
const detailScript = detailViewSrc
  .slice(0, detailViewSrc.indexOf('<template>'))
  .replace(/\/\*[\s\S]*?\*\//g, '')
  .replace(/\/\/[^\n]*/g, '')
check(
  '详情: 模板不渲染环境变量',
  !/\benv\b|\bEnv\b|environment/i.test(detailMarkup),
  '契约没有 Env，展示它就是编造'
)
check(
  '详情: 代码不读取 env 字段',
  !/\.env\b/.test(detailScript) && !/environment/i.test(detailScript)
)

/* 直连地址可编辑 */
check('服务器编辑: 有直连地址输入', /direct_address/.test(editModalSrc) && /直连地址/.test(editModalSrc))
check(
  '服务器编辑: 说明只填主机不要带 scheme/端口',
  /不要带 http/.test(editModalSrc),
  '用户不知道该填什么格式'
)
check('服务器编辑: 只提交改动过的字段', /nextAddress !== prevAddress/.test(editModalSrc))

/* ───────── 文案：未接管 → 外部项目 / 配置未登记 ───────── */

const excludeWording = readFileSync(join(here, '..', 'src', 'utils', 'statusMap.ts'), 'utf8')
const adoptSrc = readFileSync(
  join(here, '..', 'src', 'components', 'stack', 'AutoAdoptStatus.vue'),
  'utf8'
)
const stackListSrc = readFileSync(join(here, '..', 'src', 'components', 'stack', 'StackUnifiedTable.vue'), 'utf8')

check(
  '文案: 容器来源 discovered 显示「外部项目」',
  /label: '外部项目'/.test(excludeWording),
  '仍显示「未接管」'
)
check(
  '文案: 不再出现「未接管」作为展示标签',
  !/label: '未接管'/.test(excludeWording)
)
check(
  '文案: 自动接管 skipped 显示「配置未登记」',
  /label: '配置未登记'/.test(adoptSrc),
  '仍显示「暂不接管」'
)
check(
  '文案: 编排列表说明不再说「暂不能接管」',
  !/暂不能接管的原因/.test(stackListSrc)
)
check(
  '文案: 统一表明确区分配置来源和登记状态',
  /title: '配置来源'/.test(stackListSrc) && /title: '登记状态'/.test(stackListSrc)
)

/* ───────── 契约对齐：时间字段与资源上限语义 ───────── */

const typesSrc = readFileSync(join(here, '..', 'src', 'types', 'container.ts'), 'utf8')
const apiSrc = readFileSync(join(here, '..', 'src', 'api', 'containers.ts'), 'utf8')
const factsSrc = readFileSync(
  join(here, '..', 'src', 'components', 'container', 'ContainerDetailFacts.vue'),
  'utf8'
)

/* created 在详情里是 RFC3339 字符串，绝不能声明成 number */
check(
  '契约: ContainerDetail.created 声明为 string',
  /created:\s*string/.test(typesSrc),
  '声明成 number 会把 RFC3339 字符串归零'
)
check(
  '契约: started_at / finished_at 声明为 string',
  /started_at:\s*string/.test(typesSrc) && /finished_at:\s*string/.test(typesSrc)
)
check(
  '契约: exposed_ports 声明为 string[]',
  /exposed_ports:\s*string\[\]/.test(typesSrc),
  '协议是 []string（"80/tcp"），声明成 number[] 会把数据全丢掉'
)
check(
  '契约: pids_limit 可空、memory/nano 为 0 语义',
  /pids_limit:\s*number \| null/.test(typesSrc) && /memory_bytes:\s*number(?!\s*\|)/.test(typesSrc)
)
check(
  '契约: 有 ContainerDetailResponse 包装类型',
  /ContainerDetailResponse/.test(typesSrc) && /container:\s*ContainerDetail/.test(typesSrc)
)

/* API 层不能对时间字符串做数值转换 */
check(
  'API: 不对 created 做 Number 转换',
  !/Number\(res\.created\)/.test(apiSrc) && !/Number\(raw\.created\)/.test(apiSrc),
  'Number("2024-01-15T...") 是 NaN，会被当成无值'
)
check('API: 有 normalizeRFC3339 处理零值时间', /normalizeRFC3339/.test(apiSrc))
check(
  'API: 解包 { container } 响应',
  /res\?\.container|res\.container/.test(apiSrc),
  '漏解包会让所有字段变空'
)

/* 界面：0 显示成「未限制」而不是 "0 字节" */
check(
  '界面: 内存 0 显示「未限制」',
  /bytes <= 0\) return '未限制'/.test(factsSrc),
  '显示成 0 字节会让用户以为容器无法运行'
)
check(
  '界面: RFC3339 真正解析而不是 Number 强转',
  /Date\.parse/.test(factsSrc),
  'Number(rfc3339) 得 NaN，真实时间会显示成「—」'
)
check(
  '界面: 未启动/未结束有专门文案',
  /尚未启动/.test(factsSrc) && /尚未结束/.test(factsSrc)
)
check(
  '界面: 运行中的 0 退出码不谎称为正常退出',
  /容器仍在运行/.test(factsSrc),
  'exit_code 是 int，运行中也是 0，直接说正常退出是错的'
)

/* 所有容器列表都要有名称链接 + 结构化端口 */
const extContainersSrc = readFileSync(
  join(here, '..', 'src', 'components', 'stack', 'StackContainersTable.vue'),
  'utf8'
)
const stackDetailSrc = readFileSync(
  join(here, '..', 'src', 'views', 'StackDetailView.vue'),
  'utf8'
)

check(
  '外部项目容器表: 名称可点击进详情',
  /RouterLink/.test(extContainersSrc) && /agent-container-detail/.test(extContainersSrc)
)
check(
  '外部项目容器表: 复用同一个端口单元格',
  /ContainerPortCell/.test(extContainersSrc),
  '否则外部列表还是原始的 0.0.0.0:8080->80/tcp 文本'
)
check(
  '外部项目容器表: 不再解析 ports 文本',
  !/renderPort/.test(extContainersSrc) && !/split\(\s*['"],['"]\s*\)/.test(extContainersSrc)
)
check(
  '外部项目容器表: 接收 agentId 与直连地址',
  /agentId/.test(extContainersSrc) && /directAddress/.test(extContainersSrc)
)
check(
  '外部详情: 向容器表传入 agentId 与 directAddress',
  /:direct-address="agent\?\.direct_address \?\? ''"/.test(stackDetailSrc) && /<stack-containers-table/.test(stackDetailSrc)
)

/* 两类项目共用统一 summary 容器表，不再受旧 stack ps 无端口字段限制 */
check(
  '编排详情容器表: 名称可点击进详情',
  /StackContainersTable/.test(stackDetailSrc) && /agent-container-detail/.test(extContainersSrc)
)
check(
  '编排详情容器表: 统一 summary 提供结构化端口，不再调用 stack ps',
  /ContainerPortCell/.test(extContainersSrc) && !/listStackPs/.test(stackDetailSrc),
  '统一表必须消费 summary 的结构化端口，不能回退旧 ps 数据'
)

/* ───────── 结果 ───────── */

if (failures.length > 0) {
  console.error(`\n✗ 容器详情契约测试：${passed} 通过，${failures.length} 失败\n`)
  for (const f of failures) console.error(`  ✗ ${f}`)
  process.exit(1)
}
console.log(`\n✓ 容器详情契约测试：${passed} 通过，0 失败`)
