/**
 * 容器「检查」与「控制台」契约测试
 *
 * 纯 Node，无框架。跑法：node tests/container-exec.contract.test.mjs
 *
 * 覆盖重点（都是「猜错会误导用户」的地方）：
 * - exec 请求：容器身份只走路径、命令必须保持向量、空可选字段不提交
 * - exec 响应：exit_code 缺失给 -1（未知），绝不兜底成 0
 * - 命令拆分：引号/转义正确，未闭合引号明确报错而不是猜
 * - 详情页挂上两个页签，且控制台把"一次性执行"的边界讲清楚
 * - 控制台不渲染 HTML（命令输出不可信，必须当文本）
 */
import { registerHooks } from 'node:module'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { dirname, join } from 'node:path'

const here = dirname(fileURLToPath(import.meta.url))
const root = join(here, '..')

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
  const err = globalThis.__CE_STUB__.error
  globalThis.__CE_STUB__.error = null
  if (err !== null) throw err
  return globalThis.__CE_STUB__.response
}
export async function httpGet(url) {
  globalThis.__CE_STUB__.record({ method: 'GET', url })
  return take()
}
export async function httpPost(url, body) {
  globalThis.__CE_STUB__.record({ method: 'POST', url, body })
  return take()
}
export async function httpPut(url, body) {
  globalThis.__CE_STUB__.record({ method: 'PUT', url, body })
  return take()
}
export async function httpDelete(url) {
  globalThis.__CE_STUB__.record({ method: 'DELETE', url })
  return take()
}
export async function httpPatch(url, body) {
  globalThis.__CE_STUB__.record({ method: 'PATCH', url, body })
  return take()
}
`

const stubUrl = `data:text/javascript;base64,${Buffer.from(requestStub, 'utf8').toString('base64')}`

registerHooks({
  resolve(specifier, context, nextResolve) {
    if (specifier === './request' && context.parentURL?.includes('/api/containers.ts')) {
      return { url: stubUrl, shortCircuit: true }
    }
    return nextResolve(specifier, context)
  }
})

globalThis.__CE_STUB__ = {
  response: null,
  error: null,
  record: (call) => {
    lastCall = call
  }
}

function setResponse(value) {
  globalThis.__CE_STUB__.response = value
  globalThis.__CE_STUB__.error = null
}

const containersApi = await import('../src/api/containers.ts')
const { splitCommandLine } = await import('../src/utils/commandLine.ts')

/* ───────── exec 请求形状 ───────── */

setResponse({ result: { exit_code: 0, stdout: 'ok\n', stderr: '', truncated: false, timed_out: false, duration_ms: 12 } })

const execResult = await containersApi.execContainer('agent-1', 'web/1', {
  command: ['/bin/sh', '-c', 'echo ok'],
  user: 'root',
  workdir: '',
  timeout_sec: 0
})

check('exec: 走 POST .../exec', lastCall?.method === 'POST' && lastCall?.url === '/agents/agent-1/containers/web%2F1/exec',
  JSON.stringify(lastCall))
check('exec: 容器 ID 做 URL 编码（斜杠不能出现在路径里）', lastCall?.url?.includes('web%2F1') === true)
check('exec: 命令保持向量不拼接', Array.isArray(lastCall?.body?.command) && lastCall.body.command.length === 3,
  JSON.stringify(lastCall?.body))
check('exec: 空 workdir/timeout 不提交（表达"沿用镜像默认"）',
  lastCall?.body?.workdir === undefined && lastCall?.body?.timeout_sec === undefined,
  JSON.stringify(lastCall?.body))
check('exec: 显式用户会提交', lastCall?.body?.user === 'root')
check('exec: 结果原样返回', execResult.stdout === 'ok\n' && execResult.exit_code === 0)

// 缺字段：退出码必须是"未知"（-1），不能是 0
setResponse({ result: { stdout: 'partial' } })
const degraded = await containersApi.execContainer('a', 'c', { command: ['sh'] })
check('exec: 缺 exit_code 归一为 -1（未知）而不是 0', degraded.exit_code === -1, String(degraded.exit_code))
check('exec: 缺 timed_out/truncated 一律为 false',
  degraded.timed_out === false && degraded.truncated === false)
check('exec: 超时结果不伪造退出码', degraded.exit_code !== 0)

// 响应整体缺失也不能抛
setResponse({})
const empty = await containersApi.execContainer('a', 'c', { command: ['sh'] })
check('exec: 空响应不崩溃且退出码未知', empty.exit_code === -1 && empty.stdout === '')

/* ───────── 命令拆分 ───────── */

const cases = [
  ['ls -la /data', ['ls', '-la', '/data']],
  ['sh -c "echo hi"', ['sh', '-c', 'echo hi']],
  ["sh -c 'ps aux | head'", ['sh', '-c', 'ps aux | head']],
  ['echo "a b" c', ['echo', 'a b', 'c']],
  ['echo ""', ['echo', '']],
  ['echo a\\ b', ['echo', 'a b']],
  ['echo "he said \\"hi\\""', ['echo', 'he said "hi"']],
  ['  spaced   out  ', ['spaced', 'out']],
  ['', []]
]
for (const [input, want] of cases) {
  const { args, error } = splitCommandLine(input)
  check(`split: ${JSON.stringify(input)}`, error === '' && JSON.stringify(args) === JSON.stringify(want),
    error !== '' ? error : JSON.stringify(args))
}

for (const bad of [`echo 'unclosed`, 'echo "unclosed', 'echo trailing\\']) {
  const { args, error } = splitCommandLine(bad)
  check(`split: 非法输入明确报错 ${JSON.stringify(bad)}`, error !== '' && args.length === 0, error)
}

check('split: 不做变量展开/通配符（属于容器内 shell 的职责）',
  JSON.stringify(splitCommandLine('echo $HOME *.log').args) === JSON.stringify(['echo', '$HOME', '*.log']))

/* ───────── 详情页接线与文案边界 ───────── */

const detailSrc = readFileSync(join(root, 'src/views/ContainerDetailView.vue'), 'utf8')
check('详情页: 新增「检查」页签', /name="inspect"[^>]*tab="检查"/.test(detailSrc))
check('详情页: 新增「控制台」页签', /name="console"[^>]*tab="控制台"/.test(detailSrc))
check('详情页: 两个页签都用组件渲染（不是内联乱写）',
  /<container-inspect-panel/.test(detailSrc) && /<container-console/.test(detailSrc))
check('详情页: 控制台拿到在线状态', /:online="agentOnline"/.test(detailSrc))

const consoleSrc = readFileSync(join(root, 'src/components/container/ContainerConsole.vue'), 'utf8')
check('控制台: 明确说明"每条命令都是独立进程"', /独立进程/.test(consoleSrc))
check('控制台: 说明交互式程序不可用', /vi、top|vi\/top/.test(consoleSrc))
check('控制台: 超时文案不暗示进程已停止', /可能仍在运行/.test(consoleSrc))
check('控制台: 不渲染 HTML（命令输出不可信）',
  !/v-html/.test(consoleSrc) && !/innerHTML/.test(consoleSrc))
check('控制台: 退出码未知不显示成 0', /退出码未知/.test(consoleSrc))
check('控制台: 提供自定义命令开关', /自定义命令/.test(consoleSrc))
check('控制台: 提供用户输入（默认不硬编码非 root 的猜测）', /用户/.test(consoleSrc))

const inspectSrc = readFileSync(join(root, 'src/components/container/ContainerInspectPanel.vue'), 'utf8')
check('检查: 说明清单不含 Env/Labels/Secrets', /不含环境变量/.test(inspectSrc) && /Secrets/.test(inspectSrc))
check('检查: 不渲染 HTML', !/v-html/.test(inspectSrc))
check('检查: 提供复制入口', /copyToClipboard/.test(inspectSrc))

/* ───────── 结果 ───────── */

if (failures.length > 0) {
  console.error(`\n✗ 容器检查/控制台契约测试：${passed} 通过，${failures.length} 失败\n`)
  for (const f of failures) console.error(`  ✗ ${f}`)
  process.exit(1)
}
console.log(`\n✓ 容器检查/控制台契约测试：${passed} 通过，0 失败`)
