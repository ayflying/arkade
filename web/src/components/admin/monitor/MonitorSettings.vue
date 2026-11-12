<script setup lang="ts">
import { computed, onMounted, ref } from 'vue';
import { NAlert, NButton, NDivider, NForm, NSpace } from 'naive-ui';
import { getMonitorConfig, saveMonitorConfig, testMonitorChannel, type Channel, type Config, type ConfigView } from '@/api/monitor';
import { ApiError } from '@/types/api';
import { confirmAction } from '@/utils/confirm';
import { channelDraft, configIssue, defaultConfig, monitorPayload, type ChannelDraft } from './monitorForm';
import MonitorRules from './MonitorRules.vue';
import MonitorChannels from './MonitorChannels.vue';
import MonitorStatus from './MonitorStatus.vue';

const config = ref<Config>(defaultConfig()), draft = ref<ChannelDraft | null>(null);
const savedInterval = ref(30);
const version = ref(0), baseline = ref(''), busy = ref(false), error = ref(''), notice = ref('');
const serialized = computed(() => JSON.stringify({ config: config.value, draft: draft.value }));
const dirty = computed(() => draft.value !== null && serialized.value !== baseline.value);
function apply(view: ConfigView) {
  config.value = { ...view.config, excluded_agents: [...(view.config.excluded_agents ?? [])], excluded_containers: [...(view.config.excluded_containers ?? [])] };
  draft.value = channelDraft(view);
  version.value = view.version;
  savedInterval.value = view.config.interval_seconds;
  baseline.value = serialized.value;
}
async function load() {
  if (busy.value) return;
  if (dirty.value && !await confirmAction({ title: '放弃未保存的监控设置？', content: '重新读取会丢弃当前输入（包括尚未保存的通知密钥），并载入服务器上的最新配置。', danger: true })) return;
  busy.value = true;
  error.value = ''; notice.value = '';
  try { apply(await getMonitorConfig()); }
  catch (cause) { error.value = cause instanceof Error ? cause.message : '读取监控设置失败'; }
  finally { busy.value = false; }
}
async function save() {
  if (busy.value || !draft.value) return;
  error.value = configIssue(config.value, draft.value);
  notice.value = '';
  if (error.value) return;
  busy.value = true;
  try {
    apply(await saveMonitorConfig(monitorPayload(version.value, config.value, draft.value)));
    notice.value = '监控与通知设置已保存';
  } catch (cause) {
    error.value = cause instanceof ApiError && cause.status === 409
      ? '配置版本冲突：其他管理员已修改配置。当前输入已保留；请先记录修改，再重新读取最新设置后重试。'
      : cause instanceof Error ? cause.message : '保存监控设置失败';
  } finally { busy.value = false; }
}
async function test(channel: Channel) {
  if (busy.value || dirty.value || !draft.value) return;
  busy.value = true; error.value = ''; notice.value = '';
  try {
    await testMonitorChannel(channel);
    notice.value = `${channel === 'email' ? '邮件' : '钉钉'}测试请求已完成，请检查接收端及下方投递记录。`;
  } catch (cause) { error.value = cause instanceof Error ? cause.message : '测试通知失败'; }
  finally { busy.value = false; }
}
onMounted(load);
</script>
<template>
  <n-space vertical size="large">
    <n-alert type="info" title="监控能力与边界">
      未配置健康检查不代表容器健康；服务器失联不代表确定宕机。Hub 自身无法在停止运行时自报警，请配合外部可用性监控。
    </n-alert>
    <n-alert v-if="error" type="error">{{ error }}</n-alert>
    <n-alert v-if="notice" type="success">{{ notice }}</n-alert>
    <n-space align="center">
      <n-button :loading="busy" :disabled="busy" @click="load">{{ draft ? '重新读取设置' : '读取设置' }}</n-button>
      <span v-if="draft">版本 {{ version }}{{ dirty ? ' · 有未保存修改' : ' · 已与服务器同步' }}</span>
    </n-space>
    <n-form v-if="draft" label-placement="top" @submit.prevent="save">
      <monitor-rules v-model="config" :disabled="busy" />
      <n-divider />
      <monitor-channels v-model:config="config" v-model:draft="draft" :disabled="busy" :dirty="dirty" @test="test" />
      <p><n-button type="primary" attr-type="submit" :loading="busy" :disabled="busy">保存监控与通知设置</n-button></p>
    </n-form>
    <n-divider />
    <monitor-status :interval-seconds="savedInterval" />
  </n-space>
</template>
