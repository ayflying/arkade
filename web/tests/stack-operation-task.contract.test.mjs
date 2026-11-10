/**
 * 编排操作任务（所有编辑动作共用）契约测试
 *
 * 跑法：node tests/stack-operation-task.contract.test.mjs
 *
 * 覆盖 Lead 明确要求的几件事：
 * - 每个动作的标签/危险标记/参数（remove_volumes、include_volumes、version、create）
 * - 所有入口都**不再同步执行**，改成开弹窗
 * - 保存并部署带 version（乐观锁）+ env 三态语义 + hash 可选
 * - 敏感只读：输出经插值渲染、不用 v-html、不显示配置正文/密钥
 * - 删除只有 Hub 明确 record_removed 才能跳列表；202 不算成功
 */
import { readFileSync } from 'node:fs'
import { join, dirname } from 'node:path'
import { fileURLToPath, pathToFileURL } from 'node:url'
import ts from 'typescript'

const here = dirname(fileURLToPath(import.meta.url))
const SRC_ROOT = join(here, '..', 'src')
const STUB = `data:text/javascript,${encodeURIComponent('export default {}')}`

/** transpile + 重写说明符后 dataURL import（与其它测试同构） */
async function loadTs(relPath) {
  const src = readFileSync(join(here, '..', relPath), 'utf8')
  const js = ts.transpileModule(src, {
    compilerOptions: { module: ts.ModuleKind.ESNext, target: ts.ScriptTarget.ES2022 }
  }).outputText
  const rewritten = js.replace(/from\s+['"]([^'"]+)['"]/g, (whole, spec) => {
    if (spec.endsWith('.vue')) return `from '${STUB}'`
    if (spec.startsWith('@/')) {
      const target = spec.slice(2)
      if (target.endsWith('.vue')) return `from '${STUB}'`
      return `from '${pathToFileURL(join(SRC_ROOT, target.endsWith('.ts') ? target : `${target}.ts`)).href}'`
    }
    if (spec.startsWith('.')) {
      return `from '${pathToFileURL(join(SRC_ROOT, spec.replace(/^\.\//, '').replace(/^\.\.\//, ''))).href}'`
    }
    if (spec.includes(':')) return whole
    return `from '${import.meta.resolve(spec)}'`
  })
  return import(`data:text/javascript;base64,${Buffer.from(rewritten, 'utf8').toString('base64')}`)
}

const types = await loadTs('src/types/stackOperationTask.ts')
const output = await loadTs('src/utils/stackOperationOutput.ts')
const composable = await loadTs('src/composables/useStackOperationTask.ts')

let passed = 0
const failures = []
function check(name, ok, detail) {
  if (ok) passed += 1
  else failures.push(detail === undefined ? name : `${name} —— ${detail}`)
}

const runners = []
function track(r) {
  runners.push(r)
  return r
}

function read(rel) {
  return readFileSync(join(here, '..', rel), 'utf8')
}

/* ───────── 1. 动作枚举与元数据 ───────── */

{
  const EXPECTED = ['up', 'stop', 'restart', 'pull', 'rebuild', 'backup', 'remove']
  check(
    '动作: 枚举完整',
    types.STACK_OPERATION_ACTIONS.join(',') === EXPECTED.join(','),
    types.STACK_OPERATION_ACTIONS.join(',')
  )

  for (const action of EXPECTED) {
    const meta = types.STACK_OPERATION_META[action]
    check(`动作${action}: 有标签`, typeof meta?.label === 'string' && meta.label !== '')
    check(`动作${action}: 有说明`, typeof meta?.description === 'string' && meta.description.length > 10)
  }

  // 危险动作必须标 dangerous（决定是否二次确认）
  for (const action of ['restart', 'rebuild', 'remove']) {
    check(`动作${action}: 标记为危险`, types.STACK_OPERATION_META[action].dangerous === true)
  }
  for (const action of ['up', 'pull', 'backup']) {
    check(`动作${action}: 非危险`, types.STACK_OPERATION_META[action].dangerous === false)
  }

  // 更新镜像必须说清不重建
  check(
    '动作pull: 说明写明不自动重建',
    /不会自动重建/.test(types.STACK_OPERATION_META.pull.description),
    types.STACK_OPERATION_META.pull.description
  )
  // 删除必须说清默认保留卷
  check(
    '动作remove: 说明写明默认保留数据卷',
    /保留数据卷/.test(types.STACK_OPERATION_META.remove.description)
  )
}

/* ───────── 2. 状态归一化 ───────── */

{
  check('状态: running 识别', types.normalizeOperationStatus('running') === 'running')
  check('状态: 大写也能识别', types.normalizeOperationStatus('SUCCEEDED') === 'succeeded')
  check(
    '状态: 未知值归 unknown 而非成功',
    types.normalizeOperationStatus('canceled') === 'unknown',
    '未知状态绝不能乐观当成功'
  )
  check('状态: null 归 unknown', types.normalizeOperationStatus(null) === 'unknown')
  check('终态: succeeded', types.isOperationTerminal('succeeded') === true)
  check('终态: failed', types.isOperationTerminal('failed') === true)
  check('终态: running 不是终态', types.isOperationTerminal('running') === false)
  check('终态: interrupted 不是终态', types.isOperationTerminal('interrupted') === false)
}

/* ───────── 3. 任务归一化（含新字段） ───────── */

{
  const t = types.normalizeOperationTask({
    task_id: 'T1',
    name: 'app',
    action: 'backup',
    command: 'docker compose pull',
    working_dir: '/srv/app',
    status: 'succeeded',
    output: [{ seq: 1, stream: 'command', text: 'docker compose pull' }],
    next_cursor: 1,
    truncated: false,
    exit_code: 0,
    error: '',
    backup_path: 'backups/app-20260101.tar.gz',
    record_removed: false,
    stack_version: 7,
    started_at: 'a',
    finished_at: 'b'
  })
  check('归一化: action 保留', t.action === 'backup', String(t.action))
  check('归一化: exit_code 0 保留（0 是有效值）', t.exit_code === 0, String(t.exit_code))
  check('归一化: backup_path 保留', t.backup_path === 'backups/app-20260101.tar.gz')
  check('归一化: stack_version 保留', t.stack_version === 7)
  check('归一化: record_removed false 保留', t.record_removed === false)
  check('归一化: command 流保留', t.output[0].stream === 'command')

  const minimal = types.normalizeOperationTask({ task_id: 'T2' })
  check('归一化: 缺字段不崩', minimal !== null)
  check('归一化: 缺 working_dir → 空串（兼容旧 Agent）', minimal.working_dir === '')
  check('归一化: 缺 exit_code → null', minimal.exit_code === null)
  check('归一化: 缺 stack_version → null', minimal.stack_version === null)
  check('归一化: 缺 action → unknown', minimal.action === 'unknown')
  check('归一化: 缺 record_removed → false', minimal.record_removed === false)

  check('归一化: 无 task_id 返回 null', types.normalizeOperationTask({}) === null)
  check('归一化: 非对象返回 null', types.normalizeOperationTask(null) === null)

  const unknownAction = types.normalizeOperationTask({ task_id: 'T3', action: 'explode' })
  check('归一化: 未知动作归 unknown', unknownAction.action === 'unknown')
}

/* ───────── 4. 成功判定 / 删除判定 ───────── */

{
  const mk = (over) => ({ ...types.normalizeOperationTask({ task_id: 'X', status: 'succeeded' }), ...over })

  check('成功: succeeded 即成功', types.isConfirmedOperationSuccess(mk({})) === true)
  check('成功: running 不算成功', types.isConfirmedOperationSuccess(mk({ status: 'running' })) === false)
  check(
    '成功: succeeded 但 exit_code 非 0 → 不算成功',
    types.isConfirmedOperationSuccess(mk({ exit_code: 2 })) === false,
    '状态与退出码矛盾时按失败显示'
  )

  // 删除：必须 Hub 明确 record_removed
  check(
    '删除: 202 已受理不算完成（未终态）',
    types.isConfirmedRemove(mk({ action: 'remove', status: 'queued', record_removed: false })) === false,
    '不能把排队当成删除完成'
  )
  check(
    '删除: succeeded 但 record_removed=false 不算',
    types.isConfirmedRemove(mk({ action: 'remove', record_removed: false })) === false,
    'Hub 说没删掉就不能跳走'
  )
  check(
    '删除: succeeded + record_removed + exit 0 才算',
    types.isConfirmedRemove(mk({ action: 'remove', record_removed: true, exit_code: 0 })) === true
  )
  check(
    '删除: 缺 exit_code 不算完成',
    types.isConfirmedRemove(mk({ action: 'remove', record_removed: true, exit_code: null })) === false,
    '删除是不可逆操作，必须拿到明确 0'
  )
  check(
    '删除: 有 error 不算完成',
    types.isConfirmedRemove(mk({ action: 'remove', record_removed: true, exit_code: 0, error: '失败' })) === false
  )
  check(
    '删除: 状态 failed 不算完成',
    types.isConfirmedRemove(mk({ action: 'remove', status: 'failed', record_removed: true, exit_code: 0 })) === false
  )
  check('删除: null 任务不算完成', types.isConfirmedRemove(null) === false)
}

/* ───────── 5. 输出：command 分段 ───────── */

{
  const chunks = [
    { seq: 1, stream: 'command', text: 'docker compose build' },
    { seq: 2, stream: 'stdout', text: 'building' },
    { seq: 3, stream: 'command', text: 'docker compose up -d' },
    { seq: 4, stream: 'stdout', text: 'started' }
  ]
  const segs = output.segmentOutput(chunks)
  check('分段: 两条命令分两段', segs.length === 2, String(segs.length))
  check('分段: 第一步命令正确', segs[0].command === 'docker compose build')
  check('分段: 第一步输出归属正确', segs[0].chunks.length === 1 && segs[0].chunks[0].text === 'building')
  check('分段: 第二步命令正确', segs[1].command === 'docker compose up -d')

  // 命令前的前置输出
  const withPreamble = output.segmentOutput([
    { seq: 1, stream: 'stdout', text: 'preparing' },
    { seq: 2, stream: 'command', text: 'cmd' }
  ])
  check('分段: 首个命令前输出单独成段', withPreamble.length === 2 && withPreamble[0].command === null)

  check('分段: 空输入返回空', output.segmentOutput([]).length === 0)

  // command 事件不能被当成普通输出重复计入
  const merged = output.mergeOutput([], chunks)
  check('分段: merge 后仍保留 4 条', merged.chunks.length === 4)
  check(
    '分段: command 仍在流里（供分段用）',
    merged.chunks.filter((c) => c.stream === 'command').length === 2
  )
}

/* ───────── 5b. stage 流：独立进度文字，不做命令样式 ───────── */

{
  const norm = types.normalizeOperationTask({
    task_id: 'S',
    output: [
      { seq: 1, stream: 'stage', text: '正在复制数据卷…' },
      { seq: 2, stream: 'command', text: 'tar -cf x.tar' },
      { seq: 3, stream: 'stdout', text: 'ok' }
    ]
  })
  check('stage: 归一化保留 stage 流', norm.output[0].stream === 'stage', String(norm.output[0].stream))
  check(
    'stage: 未知流仍归 other',
    types.normalizeOperationTask({ task_id: 'S', output: [{ seq: 1, stream: 'weird', text: '' }] })
      .output[0].stream === 'other'
  )

  /*
   * stage 不开新段：它是阶段说明而非命令。
   * 若开了段，会出现「第 N 步」却没有命令的空段，反而更混乱。
   */
  const segs = output.segmentOutput([
    { seq: 1, stream: 'stage', text: '正在复制数据卷…' },
    { seq: 2, stream: 'command', text: 'tar -cf x.tar' },
    { seq: 3, stream: 'stdout', text: 'ok' }
  ])
  check('stage: 不产生空命令段', segs.length === 2, String(segs.length))
  check('stage: 归到首个命令前的段', segs[0].command === null && segs[0].chunks[0].stream === 'stage')

  const modal = readFileSync(new URL('../src/components/stack/StackOperationModal.vue', import.meta.url), 'utf8')
  check(
    'stage: 有独立渲染样式',
    /op-modal__stage/.test(modal),
    'stage 不能混进命令输出'
  )
  check(
    'stage: 不与 command 共用 \$ 前缀样式',
    /op-modal__stage[^}]*font-style:\s*italic/.test(modal),
    'stage 是进度说明，不是命令'
  )
  check(
    'stage: 不渲染百分比进度',
    !/\bprogress\b|percentage|%\s*<\/|aria-valuenow/.test(modal),
    '没有可靠进度来源，不能画假进度'
  )
}

/* ───────── 6. 敏感：输出只读渲染，不进 v-html ───────── */

{
  const modal = read('src/components/stack/StackOperationModal.vue')
  check(
    '安全: 弹窗不使用 v-html 指令',
    !/v-html\s*=/.test(modal),
    '日志含用户可控文本，必须插值渲染'
  )
  check(
    '安全: 日志区用 pre 插值',
    /*
     * pre 带 v-if 且有换行，所以不能要求插值紧跟标签。
     * 关键是：pre 内部是 {{ }} 插值（文本节点渲染，天然转义），而非 v-html。
     */
    /<pre[^>]*>[\s\S]{0,300}?seg\.chunks[\s\S]{0,300}?<\/pre>/.test(modal) &&
      !/v-html\s*=/.test(modal),
    '纯文本 + 插值，天然转义'
  )
  check('安全: 输出区有 role="log"', /role="log"/.test(modal))
  check('安全: 输出区 aria-live polite', /aria-live="polite"/.test(modal))

  // 命令只展示，不可编辑/不可执行
  check('安全: 命令用 code 展示', /<code[^>]*class="op-modal__command-text"/.test(modal))
  check(
    '安全: 没有把命令塞进可编辑控件',
    !/<(input|textarea)[^>]*(command|cmd)/i.test(modal)
  )
  check('安全: 不展示配置正文', !/task\.content/.test(modal))
  check('安全: 不展示 env 正文', !/task\.env_content/.test(modal))
}

/** 配置保存 composable（多个检查共用） */
const config = read('src/composables/useManagedStackConfig.ts')

/* ───────── 7. 所有入口不再同步执行 ───────── */

{
  const actions = read('src/components/stack/ManagedStackActions.vue')
  check(
    '入口: 不再调用 runStackAction',
    !/runStackAction\(/.test(actions),
    '所有动作应改走弹窗任务'
  )
  check(
    '入口: 不再调用 deleteStack',
    !/deleteStack\(/.test(actions),
    '删除也要走弹窗，才能看到真实输出与 Hub 确认'
  )
  check('入口: 使用 StackOperationModal', /StackOperationModal/.test(actions))
  check(
    '入口: 所有动作按钮共用一个弹窗',
    (actions.match(/<stack-operation-modal/g) ?? []).length === 1,
    '多份弹窗实例会各持一份状态，容易串'
  )

  // 危险动作仍要二次确认
  check('入口: 有确认调用', /confirmAction\(/.test(actions))
  check('入口: 删除有带勾选的确认', /confirmWithOption\(/.test(actions))
  check(
    '入口: 删除卷选项文案保留',
    /同时删除数据卷（永久丢失且无法恢复）/.test(actions),
    '勾选不可丢'
  )
  check('入口: 确认结果传给弹窗', /:remove-volumes="removeVolumes"/.test(actions))
  check(
    '入口: 确认后立即开始（不再点第二次）',
    /openModal\(action, true\)/.test(actions) && /autoStart/.test(actions),
    '确认完要直接执行'
  )

  // 删除跳列表只由 removed 事件驱动
  check(
    '入口: 跳列表只在 handleRemoved 里',
    /function handleRemoved[\s\S]{0,300}router\.replace/.test(actions),
    '不能在任何成功分支直接跳'
  )
  check(
    '入口: removed 由弹窗的 Hub 确认驱动',
    /@removed="handleRemoved"/.test(actions)
  )

  check(
    '入口: 保存并部署不再同步 updateStack',
    !/await updateStack\(/.test(config),
    '必须走任务，避免"已保存未部署"的不一致'
  )
  check('入口: 保存并部署打开弹窗', /deployShow\.value = true/.test(config))

  const create = read('src/views/StackCreateView.vue')
  check(
    '入口: 新建不再同步 createStack',
    !/await createStack\(/.test(create),
    '新建也要走任务'
  )
  check('入口: 新建使用弹窗', /stack-operation-modal/.test(create))
  check('入口: 新建传 create 标记', /\n\s+create\n/.test(create))
  check(
    '入口: 新建只在 succeeded 才跳路由',
    /handleCreateFinished[\s\S]{0,400}status === 'succeeded'/.test(create) &&
      /status === 'succeeded'[\s\S]{0,200}router\.replace/.test(create),
    '202 已受理不算成功'
  )
}

/* ───────── 8. 参数：version / env / create / 卷 ───────── */

{
  const api = read('src/api/stackOperationTasks.ts')
  check('参数: body 带 version', /body\.version = options\.version/.test(api))
  check('参数: body 带 create', /body\.create = true/.test(api))
  check('参数: body 带 remove_volumes', /body\.remove_volumes/.test(api))
  check('参数: body 带 include_volumes', /body\.include_volumes/.test(api))
  check('参数: body 带 expected_config_hash', /body\.expected_config_hash/.test(api))
  check(
    '参数: env_content 未提供时字段缺失',
    /if \(options\.envContent !== undefined\) body\.env_content/.test(api),
    'undefined=保留，空串=清空，不能混'
  )

  const modal = read('src/components/stack/StackOperationModal.vue')
  check('参数: 弹窗传 version', /version: props\.version/.test(modal))
  /*
   * 从快照取值而非 live props：弹窗开着时父组件仍在编辑，
   * 直接读 props 会让"确认的"与"提交的"不是同一份。
   */
  check('参数: 弹窗传 create', /create: snap\.create \? true : undefined/.test(modal))
  check(
    '参数: 只有 remove 才传 remove_volumes',
    /props\.action === 'remove' \? removeVolumesLocal\.value : undefined/.test(modal)
  )
  check(
    '参数: 只有 backup 才传 include_volumes',
    /props\.action === 'backup' \? includeVolumesLocal\.value : undefined/.test(modal)
  )

  // 保存并部署：版本号必填，不能只用 hash
  const panel = read('src/components/stack/ManagedStackConfigPanel.vue')
  check(
    '参数: 配置面板传 version',
    /:version="config\.pendingDeploy\.value\?\.version"/.test(panel),
    '快照可能为 null（未开始过），用可选链'
  )
  /*
   * 版本号必须取读取时的乐观锁版本，不能用 hash 顶替：
   * Hub 的 version 是主并发保护，hash 只是可选的额外校验。
   */
  check(
    '参数: 版本号来自读取时的乐观锁版本',
    /version: payload\.version/.test(config) && /version: form\.value\.version|version\.value/.test(config)
  )
}

/* ───────── 9. 状态机：不换 key / 断线非终态 / 503 已保存 ───────── */

{
  const { useStackOperationTask } = composable

  // 网络不确定 → 重试复用同一 request_id
  const ids = []
  let attempt = 0
  const r = track(
    useStackOperationTask({
      start: async (requestId) => {
        ids.push(requestId)
        attempt += 1
        if (attempt === 1) {
          const err = new Error('fetch failed')
          throw err // 无 status → 不确定
        }
        return types.normalizeOperationTask({ task_id: 'A', name: 'n', status: 'running', next_cursor: 0 })
      },
      fetch: async () => types.normalizeOperationTask({ task_id: 'A', name: 'n', status: 'running' }),
      expectedName: 'n'
    })
  )
  await r.start()
  check('断线: 标记 uncertain', r.uncertain.value === true)
  check('断线: 不是终态', r.finished.value === false)
  await r.resume()
  check('断线: 重试成功', r.task.value?.task_id === 'A', String(r.task.value?.task_id))
  check('断线: request_id 未变（幂等）', new Set(ids).size === 1, `ids=${ids.join('|')}`)
  check('断线: 重试后不再 uncertain', r.uncertain.value === false)
  r.dispose()
}

{
  const { useStackOperationTask } = composable

  // 503 已保存未执行：配置在 Hub，草稿不能清
  const r503 = track(
    useStackOperationTask({
      start: async () => {
        const err = new Error('配置已保存，尚未执行；同请求重试')
        err.status = 503
        throw err
      },
      fetch: async () => types.normalizeOperationTask({ task_id: 'B' }),
      // 保存类请求：503 才可能意味着"配置已入库但没执行"
      savesConfig: true
    })
  )
  await r503.start()
  check(
    '503(保存类): 标记已保存未部署',
    r503.savedNotDeployed.value === true,
    '保存类请求 503 通常是配置已入库'
  )
  check('503(保存类): 提示点明已保存', /已保存/.test(r503.errorText.value), r503.errorText.value)
  r503.dispose()
}

{
  const { useStackOperationTask } = composable

  /*
   * 非保存动作（pull/stop/...）的 503 不能声称"配置已保存"——
   * 它根本不写配置，而且 503 可能只是 Agent 超时、任务其实已受理。
   * 正确表述是"无法确认"，让用户用同一个 key 重试确认。
   */
  const r503b = track(
    useStackOperationTask({
      start: async () => {
        const err = new Error('unavailable')
        err.status = 503
        throw err
      },
      fetch: async () => types.normalizeOperationTask({ task_id: 'B2' }),
      savesConfig: false
    })
  )
  await r503b.start()
  check('503(非保存): 不声称已保存', r503b.savedNotDeployed.value === false, String(r503b.savedNotDeployed.value))
  check('503(非保存): 标记为状态未知', r503b.uncertain.value === true)
  check(
    '503(非保存): 文案说无法确认',
    /无法确认/.test(r503b.errorText.value),
    r503b.errorText.value
  )
  check(
    '503(非保存): 不说"未执行"',
    !/未执行/.test(r503b.errorText.value),
    'Agent 超时也可能已受理，断言未执行会误导'
  )
  r503b.dispose()

}

{
  const { useStackOperationTask } = composable

  // 403 永久错误：不每秒重试
  let calls = 0
  const r = track(
    useStackOperationTask({
      start: async () =>
        types.normalizeOperationTask({ task_id: 'C', name: 'n', status: 'running', next_cursor: 0 }),
      fetch: async () => {
        calls += 1
        const err = new Error('forbidden')
        err.status = 403
        throw err
      },
      expectedName: 'n'
    })
  )
  await r.start()
  await new Promise((res) => setTimeout(res, 1300))
  const after = calls
  await new Promise((res) => setTimeout(res, 1200))
  check('403: 识别为永久错误', r.permanentError.value === true)
  check('403: 停止自动重试', calls === after, `calls ${String(after)} → ${String(calls)}`)
  check('403: 提示权限', /权限/.test(r.errorText.value))
  r.dispose()
}

{
  const { useStackOperationTask } = composable

  // 连点 5 次只发 1 次 POST，且同一 request_id
  const ids = []
  let posts = 0
  const r = track(
    useStackOperationTask({
      start: async (requestId) => {
        ids.push(requestId)
        posts += 1
        await new Promise((res) => setTimeout(res, 10))
        return types.normalizeOperationTask({
          task_id: 'D',
          name: 'n',
          status: 'running',
          next_cursor: 0
        })
      },
      fetch: async () => types.normalizeOperationTask({ task_id: 'D', name: 'n', status: 'running' }),
      expectedName: 'n'
    })
  )
  await Promise.all([r.start(), r.start(), r.start(), r.start(), r.start()])
  check('连点: 只发 1 次 POST', posts === 1, String(posts))
  check('连点: request_id 唯一', new Set(ids).size === 1)
  r.dispose()
}

{
  const { useStackOperationTask } = composable

  // 关窗后重开：能读到最终状态（任务继续跑）
  let status = 'running'
  const made = track(
    useStackOperationTask({
      start: async () =>
        types.normalizeOperationTask({ task_id: 'E', name: 'n', status: 'running', next_cursor: 0 }),
      fetch: async () => types.normalizeOperationTask({ task_id: 'E', name: 'n', status, next_cursor: 0 }),
      expectedName: 'n'
    })
  )
  await made.start()
  made.pause()
  status = 'succeeded'
  await made.resume()
  check('重开: 读到终态', made.phase.value === 'done', String(made.phase.value))
  check('重开: 状态来自 GET 而非本地猜测', made.task.value?.status === 'succeeded')
  made.dispose()
}

/* ───────── 10. 输出上限与 ANSI（沿用泛化实现） ───────── */

{
  const big = 'x'.repeat(300 * 1024)
  const res = output.mergeOutput([], [{ seq: 1, stream: 'stdout', text: big }])
  check('上限: 单条超大不返回空', res.chunks.length === 1, '整条丢弃会让日志全空')
  check('上限: 截断后不超过 256KB', output.byteLength(res.chunks[0].text) <= 256 * 1024)
  check('上限: 标记 dropped', res.dropped === true)

  const many = Array.from({ length: 2100 }, (_, i) => ({ seq: i + 1, stream: 'stdout', text: `l${String(i)}` }))
  const kept = output.mergeOutput([], many)
  check('上限: 条数限 2000', kept.chunks.length === 2000, String(kept.chunks.length))
  check('上限: 保留最新', kept.chunks[kept.chunks.length - 1].seq === 2100)

  check('ANSI: 去色', output.stripAnsi('\u001B[31mred\u001B[0m') === 'red')
  check('ANSI: \\r 转 \\n', output.stripAnsi('a\rb').includes('\n'))
}

/* ───────── 结果 ───────── */

for (const r of runners) r.dispose()

if (failures.length > 0) {
  console.error(`\n✗ 编排操作任务测试：${passed} 通过，${failures.length} 失败\n`)
  for (const f of failures) console.error(`  ✗ ${f}`)
  process.exit(1)
}
console.log(`\n✓ 编排操作任务测试：${passed} 通过，0 失败`)
