<script setup lang="ts">
import { NCheckbox, NDynamicTags, NFormItem, NInputNumber, NSwitch } from 'naive-ui';
import type { Config } from '@/api/monitor';
const config = defineModel<Config>({ required: true });
defineProps<{ disabled: boolean }>();
const times = [
  { key: 'interval_seconds', label: '采集间隔（秒）', min: 10, max: 3600 },
  { key: 'duration_seconds', label: '超限持续时间（秒，0 为立即）', min: 0, max: 86400 },
  { key: 'repeat_seconds', label: '重复通知间隔（秒）', min: 60, max: 604800 },
  { key: 'offline_seconds', label: '服务器失联判定时间（秒）', min: 30, max: 86400 },
] as const;
</script>
<template>
  <section>
    <h3>监控规则</h3>
    <n-form-item label="启用后台监控"><n-switch v-model:value="config.enabled" :disabled="disabled" /></n-form-item>
    <div class="rule-grid">
      <n-form-item v-for="item in times" :key="item.key" :label="item.label">
        <n-input-number v-model:value="config[item.key]" :min="item.min" :max="item.max" :precision="0" :disabled="disabled" />
      </n-form-item>
      <n-form-item label="CPU 告警阈值（%，一个核为 100%）"><n-input-number v-model:value="config.cpu_percent" :min="0.01" :max="12800" :disabled="disabled" /></n-form-item>
      <n-form-item label="内存告警阈值（%）"><n-input-number v-model:value="config.memory_percent" :min="0.01" :max="100" :disabled="disabled" /></n-form-item>
    </div>
    <n-form-item><n-checkbox v-model:checked="config.recovery_notify" :disabled="disabled">恢复后发送通知</n-checkbox></n-form-item>
    <n-form-item label="排除服务器 ID（输入后按回车）"><n-dynamic-tags v-model:value="config.excluded_agents" :disabled="disabled" /></n-form-item>
    <n-form-item label="排除完整容器 ID 或精确名称（输入后按回车，同名跨服务器生效）"><n-dynamic-tags v-model:value="config.excluded_containers" :disabled="disabled" /></n-form-item>
  </section>
</template>
<style scoped>
.rule-grid { display: grid; grid-template-columns: repeat(auto-fit, minmax(210px, 1fr)); gap: 0 20px; }
</style>
