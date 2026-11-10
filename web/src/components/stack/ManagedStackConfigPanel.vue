<script setup lang="ts">
import { onMounted } from 'vue'
import { NAlert, NButton, NSpin } from 'naive-ui'
import CodeEditor from '@/components/common/CodeEditor.vue'
import StackEnvEditor from './StackEnvEditor.vue'
import { useManagedStackConfig } from '@/composables/useManagedStackConfig'
const props = defineProps<{ agentId: string; stackName: string }>()
const emit = defineEmits<{ changed: [] }>()
const config = useManagedStackConfig(props.agentId, props.stackName, () => emit('changed'))
onMounted(() => { void config.load() })
</script>

<template>
  <section class="config-panel">
    <n-alert type="warning" :bordered="false">保存配置会立即执行 compose up，可能创建或重建容器，不是保存草稿。</n-alert>
    <n-spin v-if="config.loading.value" size="small" />
    <n-alert v-else-if="config.error.value" type="info" :bordered="false">
      {{ config.error.value }} <n-button size="tiny" @click="config.load">重新加载</n-button>
    </n-alert>
    <template v-else-if="config.loaded.value">
      <div class="config-panel__meta">{{ config.dirty.value ? '有未保存的修改' : '内容与 Hub 一致' }} · 版本 v{{ config.version.value }}</div>
      <n-alert v-if="config.saveError.value" type="error">{{ config.saveError.value }}</n-alert>
      <n-alert v-if="config.syncWarning.value" type="warning">{{ config.syncWarning.value }}</n-alert>
      <label for="managed-compose">compose.yml</label>
      <code-editor
        id="managed-compose"
        v-model="config.content.value"
        aria-label="compose.yml 内容"
        :disabled="config.saving.value"
        :min-height="340"
        :max-height="560"
      />
      <stack-env-editor v-model="config.envContent.value" :disabled="config.saving.value" />
      <div class="config-panel__actions">
        <n-button type="primary" size="small" :loading="config.saving.value" :disabled="!config.dirty.value || config.saving.value" @click="config.save">保存并部署</n-button>
        <n-button size="small" :disabled="!config.dirty.value || config.saving.value" @click="config.reset">放弃修改</n-button>
      </div>
    </template>
  </section>
</template>

<style scoped>
.config-panel { display: flex; flex-direction: column; gap: 12px; }
.config-panel__actions { display: flex; gap: 8px; flex-wrap: wrap; }
.config-panel__meta { color: var(--arkade-text-tertiary); font-size: 12px; }
.config-panel :deep(textarea) { font-family: var(--arkade-mono-font); line-height: 1.6; tab-size: 2; }
</style>
