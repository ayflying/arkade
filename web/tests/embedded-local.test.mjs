import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import test from 'node:test';
import { parse, compileScript, compileTemplate } from 'vue/compiler-sfc';

const read = name => readFileSync(new URL(`../src/${name}`, import.meta.url), 'utf8');
const actions = read('components/agent/AgentRowActions.vue');
const list = read('views/AgentListView.vue');

test('内置本机隐藏网络密钥/删除/禁用，但保留查看编辑和旧禁用恢复', () => {
  assert.match(actions, /props\.agent\.id === 'agent-local'/);
  assert.match(actions, /<n-tooltip v-if="!isLocal"[^>]*>[\s\S]*?emit\('reset-token'\)/);
  assert.match(actions, /<n-tooltip v-if="!isLocal"[^>]*>[\s\S]*?emit\('delete'\)/);
  assert.match(actions, /v-if="!isLocal \|\| disabled"/);
  assert.match(actions, /emit\('enter'\)/);
  assert.match(actions, /emit\('edit'\)/);
  assert.match(actions, /解除旧本机禁用后需重启 Hub/);
});

test('操作处理器也阻止本机危险入口，解除禁用不假称已经重启', () => {
  for (const name of ['handleDelete', 'handleResetToken']) {
    const body = list.slice(list.indexOf(`async function ${name}(`));
    assert.match(body, /if \(agent\.id === 'agent-local'\) \{[\s\S]*?return/);
  }
  assert.match(list, /agent\.id === 'agent-local' && !disabled/);
  assert.match(list, /disabled \? await enableAgent\(agent\.id\) : await disableAgent\(agent\.id\)/);
  assert.match(list, /已解除本机禁用；请确认 local\.enabled 已开启并重启 Hub/);
  assert.match(list, /内置本机不使用网络接入密钥/);
  assert.doesNotMatch(list, /fetch\([^\n]*restart|restartHub|is_local:/);
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
