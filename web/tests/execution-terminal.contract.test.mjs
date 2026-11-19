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
console.log('执行终端契约测试通过')
