<script setup lang="ts">
import { onMounted } from 'vue'
import { NAlert, NButton, NSpin } from 'naive-ui'
import CodeEditor from '@/components/common/CodeEditor.vue'
import StackEnvEditor from './StackEnvEditor.vue'
import StackOperationModal from './StackOperationModal.vue'
import { useManagedStackConfig } from '@/composables/useManagedStackConfig'
import { decideDeployRequest } from '@/utils/stackDeployRequest'
const props = defineProps<{ agentId: string; stackName: string; online?: boolean }>()
const emit = defineEmits<{ changed: [] }>()
const config = useManagedStackConfig(props.agentId, props.stackName, () => emit('changed'))
onMounted(() => { void config.load() })

/**
 * 部署任务结束后：
 * - 成功 → 基准对齐、清掉"未保存"；
 * - 失败但配置已入库 → 刷新 version，草稿保留；
 * - 无论成败都通知父级刷新（容器状态可能已变）。
 *
 * `stack_version` 由 Hub 在任务里给出，用来同步乐观锁版本号，
 * 避免拿旧版本重复保存一直撞锁。
 */
function handleDeployFinished(task: unknown): void {
  const t = task as { status?: string; stack_version?: number | null } | null
  const succeeded = t?.status === 'succeeded'
  const version = typeof t?.stack_version === 'number' ? t.stack_version : null
  config.handleDeployed(succeeded, version)
}

/**
 * 弹窗关闭：**不清空 pendingDeploy**。
 *
 * 清空会让承载任务的那个弹窗实例失去 props 来源，进而被父级条件卸载，
 * 任务实例与 request_id 一起丢掉；再点保存就是新 request_id → 重复部署。
 * 所以快照留着，关窗只是不看。
 */
function handleDeployShow(value: boolean): void {
  config.deployShow.value = value
}

/**
 * 点「保存并部署」：决定是**只查看已有任务**还是**起一轮全新任务**。
 *
 * 弹窗是常驻实例（关闭只隐藏，任务与 request_id 必须留着）。于是有个矛盾：
 * - 旧任务还在跑/状态未知 → 绝不能起新任务（换 request_id = 重复部署）；
 * - 旧任务已确认结束、用户又改了内容并点保存 → 必须真的起新任务，
 *   否则弹窗只会显示上一次的日志，用户会以为自己改的内容已经部署了。
 *
 * 判断交给纯函数 `decideDeployRequest`（可穷举测试）：
 * 只有「确认终态 + 有新内容」才走 new-run，由 composable 升 generation
 * 让弹窗重新挂载 → 新 runner → 新 request_id → 一次新 POST。
 */
function openDeployModal(): void {
  const decision = decideDeployRequest({
    dirty: config.dirty.value,
    inFlight: config.deployInFlight.value,
    settled: config.deploySettled.value,
    attempted: config.deployAttempted.value
  })

  if (decision === 'view') {
    config.deployShow.value = true
    return
  }
  void config.save()
}
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
        <!--
          没有改动时也要能点开看已有任务（成功/失败/状态未知都可能）。
          只靠 dirty 禁用会把"关掉窗口后再看进度"这条路彻底堵死。
          有发起过就允许打开；文案随之变成「查看部署日志」。
        -->
        <n-button
          type="primary"
          size="small"
          :loading="config.saving.value"
          :disabled="(!config.dirty.value && !config.deployAttempted.value) || config.saving.value"
          @click="openDeployModal"
        >
          {{ config.dirty.value ? '保存并部署' : '查看部署日志' }}
        </n-button>
        <n-button size="small" :disabled="!config.dirty.value || config.saving.value" @click="config.reset">放弃修改</n-button>
      </div>
    </template>

    <!--
      保存并部署走操作任务：Hub 校验版本 → 保存 → 下发 spec 执行 up，
      弹窗展示真实命令与实时输出。确认对话框已经点过，这里 auto-start 直接执行。
    -->
    <!--
      常驻实例（不 v-if）：关闭仅隐藏。若用 v-if 卸载，正在/已完成的部署任务
      连同 request_id 一起丢失，再点保存会起第二个任务。
    -->
    <stack-operation-modal
      :key="`deploy-${agentId}/${stackName}/${config.deployGeneration.value}`"
      :show="config.deployShow.value"
      :agent-id="agentId"
      :stack-name="stackName"
      action="up"
      :online="online !== false"
      :content="config.pendingDeploy.value?.content"
      :env-content="config.pendingDeploy.value?.envContent"
      :version="config.pendingDeploy.value?.version"
      :expected-config-hash="config.pendingDeploy.value?.expectedConfigHash"
      auto-start
      @finished="handleDeployFinished"
      @busy="config.setDeployBusy"
      @update:show="handleDeployShow"
    />
  </section>
</template>

<style scoped>
.config-panel { display: flex; flex-direction: column; gap: 12px; }
.config-panel__actions { display: flex; gap: 8px; flex-wrap: wrap; }
.config-panel__meta { color: var(--arkade-text-tertiary); font-size: 12px; }
.config-panel :deep(textarea) { font-family: var(--arkade-mono-font); line-height: 1.6; tab-size: 2; }
</style>
