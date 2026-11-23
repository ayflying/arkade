import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import test from 'node:test'
import ts from 'typescript'
import { createMemoryHistory, createRouter } from 'vue-router'
import { buildBreadcrumbs } from '../src/router/breadcrumbs.ts'

// 使用真实路由定义和内存路由，不加载页面组件或连接 Hub。
const source = readFileSync(new URL('../src/router/index.ts', import.meta.url), 'utf8')
const administration = readFileSync(new URL('../src/router/administration.ts', import.meta.url), 'utf8')
const routesSource = source.slice(source.indexOf('const agentChildRoutes'), source.indexOf('export const router'))
const js = ts.transpileModule(administration.replace(/^import[^\n]+\n/, '').replace('export const', 'const') + routesSource + '\nexport { routes }', { compilerOptions: { target: ts.ScriptTarget.ESNext, module: ts.ModuleKind.ESNext } }).outputText
const { routes } = await import(`data:text/javascript;base64,${Buffer.from(js).toString('base64')}`)
const router = createRouter({ history: createMemoryHistory(), routes })
const id = 'local'
const base = '/agents/local'

function verify(path, expected, server = null) {
  const crumbs = buildBreadcrumbs(router.resolve(path), server, (to) => router.resolve(to).meta)
  assert.deepEqual(crumbs.map(({ label, to }) => [label, to ? router.resolve(to).path : null]), expected)
}

for (const [suffix, current, parent, parentPath] of [
  ['/stacks', '容器编排'], ['/stacks/act-runner', 'act-runner', '容器编排', '/stacks'],
  ['/stacks/new', '新建编排', '容器编排', '/stacks'], ['/containers', '容器'],
  ['/containers/test-id', '容器详情', '容器', '/containers'], ['/images', '本地镜像'],
  ['/networks', '网络'], ['/volumes', '存储卷'],
  ['/volumes/sample%20volume/files?path=notes', 'sample volume', '存储卷', '/volumes']
]) {
  test(`服务器导航 ${suffix}`, () => {
    const expected = [['服务器', '/agents'], ['本机', null]]
    if (parent) expected.push([parent, base + parentPath])
    expected.push([current, null])
    verify(base + suffix, expected, { id, label: '本机' })
  })
}

for (const [path, label] of Object.entries({ '/overview': '仪表盘', '/agents': '服务器', '/audit': '操作审计', '/users': '用户管理', '/groups': '用户组与授权', '/settings': '系统设置', '/account': '个人设置', '/forbidden': '无权访问' })) {
  test(`全局导航 ${path}`, () => verify(path, [[label, null]]))
}

test('卷内目录导航不混用卷列表与卷根目录', () => {
  const template = readFileSync(new URL('../src/components/volume/VolumeBreadcrumb.vue', import.meta.url), 'utf8')
  assert.match(template, /@click="emit\('navigate', ''\)"/)
  assert.match(template, /v-if="index === crumbs.length - 1"[^>]*aria-current="location"/)
  assert.doesNotMatch(template, /backToVolumes/)
})
