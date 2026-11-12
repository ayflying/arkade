import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { stripTypeScriptTypes } from 'node:module';
import test from 'node:test';

// 零新增依赖：Node 22.13+ 自带类型擦除；在仓库根目录或 web 下均可运行。
const read = name => readFileSync(new URL(`../src/components/admin/monitor/${name}`, import.meta.url), 'utf8');
const encode = source => `data:text/javascript;base64,${Buffer.from(source).toString('base64')}`;
const load = source => import(encode(stripTypeScriptTypes(source)));
const form = await load(read('./monitorForm.ts'));
const format = await load(read('./monitorFormat.ts'));
const view = {
  version: 7, config: form.defaultConfig(),
  channels: {
    dingtalk: { webhook: 'https://example.invalid/?access_token=never-echo', has_webhook: true, has_secret: true },
    email: { host: 'smtp.example.invalid', port: 587, tls_mode: 'starttls', username: 'sender', from: 'sender@example.invalid', to: ['reader@example.invalid'], has_password: true },
  },
};

test('默认规则、密钥不回填、空输入保持及 CAS 版本', () => {
  assert.deepEqual(form.defaultConfig(), {
    enabled: false, interval_seconds: 30, duration_seconds: 120, repeat_seconds: 1800,
    offline_seconds: 120, cpu_percent: 80, memory_percent: 80, recovery_notify: true,
    excluded_agents: [], excluded_containers: [], dingtalk_enabled: false, email_enabled: false,
  });
  const draft = form.channelDraft(view);
  assert.equal(draft.webhook, '');
  assert.equal(draft.secret, '');
  assert.equal(draft.password, '');
  const payload = form.monitorPayload(view.version, view.config, draft);
  assert.equal(payload.version, 7);
  assert.deepEqual(payload.channels.dingtalk, {});
  assert.equal('password' in payload.channels.email, false);
  assert.equal('has_password' in payload.channels.email, false);
  payload.channels.email.to.push('other@example.invalid');
  assert.equal(view.channels.email.to.length, 1);
});

test('替换与显式清除互斥，所有通道开关和排除项随配置保存', () => {
  const draft = form.channelDraft(view);
  Object.assign(draft, { webhook: 'new-address', secret: 'new-secret', password: 'new-password' });
  let payload = form.monitorPayload(8, { ...view.config, dingtalk_enabled: true, email_enabled: true, excluded_agents: ['a'], excluded_containers: ['c'] }, draft);
  assert.deepEqual(payload.channels.dingtalk, { webhook: 'new-address', secret: 'new-secret' });
  assert.equal(payload.channels.email.password, 'new-password');
  assert.equal(payload.config.dingtalk_enabled, true);
  assert.deepEqual(payload.config.excluded_agents, ['a']);
  assert.deepEqual(payload.config.excluded_containers, ['c']);
  Object.assign(draft, { clear_webhook: true, clear_secret: true, clear_password: true });
  payload = form.monitorPayload(8, view.config, draft);
  assert.deepEqual(payload.channels.dingtalk, { clear_webhook: true, clear_secret: true });
  assert.equal(payload.channels.email.clear_password, true);
  assert.equal('password' in payload.channels.email, false);
});

test('规则校验拒绝空数字及无效阈值，不把无统计误作零', () => {
  const draft = form.channelDraft(view);
  assert.equal(form.configIssue(view.config, draft), '');
  assert.match(form.configIssue({ ...view.config, interval_seconds: null }, draft), /整数秒/);
  assert.equal(form.configIssue({ ...view.config, cpu_percent: 200, duration_seconds: 0 }, draft), '');
  assert.match(form.configIssue({ ...view.config, cpu_percent: 12801 }, draft), /12800%/);
  assert.match(form.configIssue({ ...view.config, memory_percent: 101 }, draft), /100%/);
  for (const [key, value] of [['interval_seconds', 9], ['interval_seconds', 3601], ['duration_seconds', -1], ['repeat_seconds', 59], ['offline_seconds', 29]]) {
    assert.match(form.configIssue({ ...view.config, [key]: value }, draft), /整数秒/);
  }
  assert.match(form.configIssue({ ...view.config, email_enabled: true }, { ...draft, email: { ...draft.email, to: [] } }), /收件人/);
  assert.match(format.healthText('none'), /不代表健康/);
  assert.equal(format.timeText(0), '—');
  assert.equal(format.bytesText(1024), '1.0 KiB');
});

test('首样本不显示虚假零速率，过期依据已保存采集间隔且不显示旧值', () => {
  const now = 1_000_000;
  assert.equal(format.rateText({ at: 999, rates_available: false }, 0, 0, 30, now), '速率暂不可用（需连续样本）');
  assert.equal(format.rateText({ at: 999, rates_available: true }, 1024, 2048, 30, now), '1.0 KiB/s / 2.0 KiB/s');
  assert.equal(format.rateText({ at: 800, rates_available: true }, 1024, 2048, 30, now), '已过期');
  assert.equal(format.metricExpired(800, 120, now), false);
  assert.equal(format.metricExpired(0, 120, now), true);
  const metrics = read('./MonitorMetrics.vue');
  assert.match(metrics, /snapshot_complete/);
  assert.match(metrics, /stale\(row\) \? '已过期'/);
  assert.match(read('./MonitorSettings.vue'), /:interval-seconds="savedInterval"/);
});

test('API 使用约定路由、PUT 版本及只发送已保存通道的测试请求', async () => {
  const calls = [];
  globalThis.__MONITOR_REQUEST__ = (...args) => { calls.push(args); return Promise.resolve(view); };
  const request = encode(`export const httpGet=(...args)=>globalThis.__MONITOR_REQUEST__('GET',...args);export const httpPut=(...args)=>globalThis.__MONITOR_REQUEST__('PUT',...args);export const httpPost=(...args)=>globalThis.__MONITOR_REQUEST__('POST',...args);`);
  const source = stripTypeScriptTypes(read('../../../api/monitor.ts')).replace("from './request'", `from '${request}'`);
  const api = await import(encode(source));
  await api.getMonitorConfig();
  assert.deepEqual(calls.at(-1), ['GET', '/monitor/config']);
  const payload = form.monitorPayload(7, view.config, form.channelDraft(view));
  await api.saveMonitorConfig(payload);
  assert.deepEqual(calls.at(-1), ['PUT', '/monitor/config', payload]);
  await api.testMonitorChannel('dingtalk');
  assert.deepEqual(calls.at(-1), ['POST', '/monitor/test', { channel: 'dingtalk' }]);
  await api.testMonitorChannel('email');
  assert.deepEqual(calls.at(-1), ['POST', '/monitor/test', { channel: 'email' }]);
  await api.getMonitorStatus();
  assert.deepEqual(calls.at(-1), ['GET', '/monitor/status']);
  delete globalThis.__MONITOR_REQUEST__;
});

test('tab 生命周期、轮询隔离、冲突保留及中文安全说明', () => {
  const settings = read('./MonitorSettings.vue'), status = read('./MonitorStatus.vue');
  const tab = read('../../../views/SettingsView.vue');
  assert.match(tab, /tab="监控与通知"/);
  assert.match(tab, /v-if="activeTab === 'monitor'"/);
  assert.match(status, /document\.hidden/);
  assert.match(status, /clearTimeout\(timer\)/);
  assert.match(status, /removeEventListener\('visibilitychange'/);
  assert.match(status, /if \(!mounted\) return/);
  assert.doesNotMatch(status, /getMonitorConfig|saveMonitorConfig|setInterval/);
  assert.match(settings, /cause\.status === 409/);
  assert.match(settings, /当前输入已保留/);
  assert.match(settings, /confirmAction/);
  assert.match(settings, /未配置健康检查不代表容器健康/);
  assert.match(settings, /失联不代表确定宕机/);
  assert.match(settings, /Hub 自身无法/);
  assert.match(read('./MonitorChannels.vue'), /测试仅使用已保存的配置/);
  assert.match(read('./MonitorChannels.vue'), /通知密钥加密保存/);
  for (const name of ['MonitorSettings', 'MonitorStatus', 'MonitorRules', 'MonitorChannels', 'MonitorMetrics', 'MonitorHistory']) {
    const source = read(`./${name}.vue`);
    assert.ok(source.split('\n').length < 180, name);
    assert.match(source, /<script setup lang="ts">/);
  }
});
