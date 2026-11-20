import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import ts from 'typescript'

const read = (path) => readFileSync(new URL(`../src/${path}`, import.meta.url), 'utf8')
const { outputText } = ts.transpileModule(read('utils/terminalCommand.ts'), {
  compilerOptions: { target: ts.ScriptTarget.ES2022, module: ts.ModuleKind.ESNext }
})
const { highlightTerminalCommand } = await import(`data:text/javascript;base64,${Buffer.from(outputText).toString('base64')}`)

const command = `docker  compose --project-name arkade --env-file '/data/local/with space/.env' pull`
const tokens = highlightTerminalCommand(command)
assert.equal(tokens.map(({ text }) => text).join(''), command)
assert.deepEqual(tokens.filter(({ kind }) => kind !== 'plain'), [
  { text: 'docker', kind: 'executable' },
  { text: '--project-name', kind: 'option' },
  { text: '--env-file', kind: 'option' },
  { text: "'/data/local/with space/.env'", kind: 'value' }
])
for (const input of ['', ' \t ', 'docker \"未闭合', String.raw`docker /a\ b C:\a\b`, 'docker <img src=x>']) {
  assert.equal(highlightTerminalCommand(input).map(({ text }) => text).join(''), input)
}

const terminal = read('components/common/ExecutionTerminal.vue')
assert.doesNotMatch(terminal, /v-html\s*=/)
assert.match(terminal, /<pre[^>]*>\{\{ line.text \}\}<\/pre>/)
assert.match(terminal, /<code[^>]*>[\s\S]+?\{\{ token.text \}\}[\s\S]+?<\/code>/)
assert.match(terminal, /role="log"\s+aria-live="polite"/)
assert.match(terminal, /el\.scrollHeight - el\.scrollTop - el\.clientHeight > 24\) return\s+await nextTick\(\)/)
assert.match(terminal, /\.terminal__line--stage\s*\{[^}]+font-style: italic/)
assert.match(terminal, /if \(stream === 'stage'\) return 'stage'/)

/*
 * 结束横幅：所有命令跑完之后必须给一句"可以关了"的结论。
 *
 * 约束：
 *   - 不在执行中显示（提前说"完成"会让人直接关窗口，把问题掩盖掉）；
 *   - 成功与失败用不同样式，不能都写成成功；
 *   - 仍然是纯文本插值，不引入 v-html。
 */
assert.match(terminal, /completion\?:\s*\{\s*text: string;\s*ok: boolean\s*\}\s*\|\s*null/)
assert.match(terminal, /v-if="completion"[\s\S]{0,400}role="status"/)
assert.match(terminal, /terminal__completion--ok/)
assert.match(terminal, /terminal__completion--failed/)
assert.doesNotMatch(terminal, /v-html\s*=/)

const opModal = read('components/stack/StackOperationModal.vue')
// 成功文案：deploy 说"更新完成"，其余动作说"执行完成"；结尾必须提示可以关窗
assert.match(opModal, /props\.action === 'deploy' \? '更新完成' : '执行完成'/)
assert.match(opModal, /如日志无异常再关闭此窗口！/)
// 只有真实终态才给横幅：done 成功、failed 失败，其余（含 interrupted/unknown）为 null
assert.match(opModal, /if \(phase\.value === 'done'\)[\s\S]{0,200}ok: true/)
assert.match(opModal, /if \(phase\.value === 'failed'\)[\s\S]{0,200}ok: false/)
assert.match(opModal, /:completion="completion"/)

const migrateModal = read('components/stack/StackMigrateModal.vue')
assert.match(migrateModal, /迁移完成，如日志无异常再关闭此窗口！/)
assert.match(migrateModal, /:completion="completion"/)

console.log('执行终端契约测试通过')
