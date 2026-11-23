import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import test from 'node:test';
import { parse, compileScript, compileTemplate } from 'vue/compiler-sfc';

const read = name => readFileSync(new URL(`../src/${name}`, import.meta.url), 'utf8');
const actions = read('components/agent/AgentRowActions.vue');
const list = read('views/AgentListView.vue');

test('内置本机隐藏网络密钥/删除/禁用，启用，但保留查看编辑', () => {
  assert.match(actions, /props\.agent\.id === 'agent-local'/);
  assert.match(actions, /<n-tooltip v-if="!isLocal"[^>]*>[\s\S]*?emit\('reset-token'\)/);
  assert.match(actions, /<n-tooltip v-if="!isLocal"[^>]*>[\s\S]*?emit\('delete'\)/);
  assert.match(actions, /<n-tooltip v-if="!isLocal"[^>]*>[\s\S]*?emit\('toggle-disabled'\)/);
  assert.match(actions, /emit\('enter'\)/);
  assert.match(actions, /emit\('edit'\)/);
  assert.doesNotMatch(actions, /!isLocal \|\| disabled|解除旧本机禁用/);
});

test('操作处理器也阻止本机危险入口，包括启用与禁用', () => {
  for (const name of ['handleDelete', 'handleResetToken', 'handleToggleDisabled']) {
    const body = list.slice(list.indexOf(`async function ${name}(`));
    assert.match(body, /if \(agent\.id === 'agent-local'\) \{[\s\S]*?return/);
  }
  assert.doesNotMatch(list, /agent\.id === 'agent-local' && !disabled/);
  assert.match(list, /disabled \? await enableAgent\(agent\.id\) : await disableAgent\(agent\.id\)/);
  assert.match(list, /启用状态由 Hub 的 local\.enabled 配置管理/);
  assert.match(list, /内置本机不使用网络接入密钥/);
  assert.doesNotMatch(list, /fetch\([^\n]*restart|restartHub|is_local:/);
});

test('各服务器更新互不影响：只锁自己那一行，不牵连其它服务器', () => {
  // 页面按 id 记录在途更新，而不是一个共享的 busy 标记。
  assert.match(list, /updatingIds = ref\(new Set<string>\(\)\)/);
  assert.match(list, /isUpdating: \(agentId: string\) => updatingIds\.value\.has\(agentId\)/);
  // 更新占用自己那一行，不写共享 busyId（否则会禁用整张表）。
  const body = list.slice(list.indexOf('async function handleSelfUpdate('));
  const updateBody = body.slice(0, body.indexOf('\nasync function '));
  assert.doesNotMatch(updateBody, /busyId\.value\s*=/);
  assert.match(updateBody, /updatingIds\.value = new Set\(updatingIds\.value\)\.add\(agent\.id\)/);
  // 每台一个观察信号，卸载时全部中止。
  assert.match(list, /for \(const observer of selfUpdateObservers\.values\(\)\) observer\.abort\(\)/);

  // 行组件只把 updating 作用到「更新」按钮。
  assert.match(actions, /props\.updating \|\| disabled\.value \|\| !isOnline\.value/);
  assert.match(actions, /\{\{ updating \? '更新中' : '更新' \}\}/);
  assert.match(read('composables/agentColumns.ts'), /isUpdating: \(agentId: string\) => boolean/);
});

test('两个真实 SFC 编译通过且不增加 API 字段', () => {
  for (const [filename, source] of [['AgentRowActions.vue', actions], ['AgentListView.vue', list]]) {
    const { descriptor, errors } = parse(source);
    assert.deepEqual(errors, [], filename);
    compileScript(descriptor, { id: filename });
    assert.deepEqual(compileTemplate({ source: descriptor.template.content, filename, id: filename }).errors, [], filename);
  }
  assert.doesNotMatch(read('types/agent.ts'), /is_local|embedded_local/);
});
