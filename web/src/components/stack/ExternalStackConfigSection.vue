<script setup lang="ts">
import { NAlert, NButton } from 'naive-ui'
import ExternalStackConfigPanel from '@/components/external/ExternalStackConfigPanel.vue'
import ExternalStackCopyModal from '@/components/external/ExternalStackCopyModal.vue'
import { useExternalStackConfigActions } from '@/composables/useExternalStackConfigActions'
const props = defineProps<{ agentId: string; stackName: string }>()
const emit = defineEmits<{ changed: [] }>()
const { store, applying, preview, applyError, alreadyManaged, copyShow, copying, copyError, agentList, editorContent, stackName, handleSaveDraft, handleApply, goManagedDetail, openCopy, handleCopy } = useExternalStackConfigActions(props, () => emit('changed'))
</script>
<template>
  <section>
          <external-stack-config-panel
            :state="store.configState.value"
            :raw-content="store.rawContent.value"
            :reason="store.configReason.value"
            :error-text="store.configError.value"
            :blockers="store.blockers.value"
            :draft="store.draft.value"
            :saving="store.savingDraft.value"
            :draft-error="store.draftError.value"
            :can-edit="store.configState.value === 'ready'"
            :draft-only="store.draftOnly.value"
            @save-draft="handleSaveDraft"
            @update:content="editorContent = $event"
          />

          <!--
            登记与复制只在对配置可读时出现：没有配置内容就没什么可登记。
            两者都需要 manage 权限，403 由后端判定并提示。
          -->
          <div v-if="store.configReadable.value" class="ext-detail__actions">
            <n-button
              size="small"
              :loading="applying"
              :disabled="store.hasBlockers.value && preview !== null && !preview.can_import"
              @click="handleApply(editorContent)"
            >
              登记到 Arkade
            </n-button>
            <n-button size="small" secondary @click="openCopy">复制配置到其他服务器</n-button>
            <span class="ext-detail__action-hint">
              登记只把配置收进 Hub，不启动也不重启容器。
            </span>
          </div>

          <!-- 预检阻塞项：如实展示，不擅自放行 -->
          <n-alert
            v-if="preview !== null && !preview.can_import"
            type="warning"
            :bordered="false"
            class="ext-detail__preview"
            title="这份配置暂时不能登记到 Arkade"
          >
            <ul class="ext-detail__preview-list">
              <li v-for="item in preview.blockers" :key="item">{{ item }}</li>
            </ul>
          </n-alert>
          <!--
            409 可能是同名已登记，也可能是校验/运行时冲突。前端分不清，
            所以只说「冲突」并给确认入口，**不宣称已登记**。
          -->
          <n-alert
            v-if="alreadyManaged"
            type="warning"
            :bordered="false"
            class="ext-detail__preview"
            title="登记未完成"
          >
            如果该项目已被自动登记，可以到已纳管列表确认；否则请按上面的提示处理后重试。
            <div class="ext-detail__already">
              <n-button size="small" @click="goManagedDetail">查看已纳管项目</n-button>
            </div>
          </n-alert>

          <p v-if="applyError !== ''" class="ext-detail__error">{{ applyError }}</p>
    <external-stack-copy-modal
      v-model:show="copyShow"
      :source-name="stackName.value"
      :content="editorContent"
      :agent-id="agentId ?? ''"
      :agents="agentList"
      :loading="copying"
      :error-text="copyError"
      @submit="handleCopy"
    />
  </section>
</template>

<style scoped>
.ext-detail {
  display: flex;
  flex-direction: column;
  gap: 12px;
}

.ext-detail__notice {
  font-size: 13px;
}

.ext-detail__containers {
  margin-top: 18px;
}

.ext-detail__containers-head {
  margin-bottom: 8px;
  font-size: 13px;
  font-weight: 600;
  color: var(--arkade-text-primary);
}

.ext-detail__actions {
  display: flex;
  align-items: center;
  gap: 10px;
  flex-wrap: wrap;
  margin-top: 12px;
}

.ext-detail__action-hint {
  font-size: 12px;
  color: var(--arkade-text-tertiary);
}

.ext-detail__preview-list {
  margin: 4px 0 0;
  padding-left: 18px;
}

.ext-detail__error {
  margin: 0;
  font-size: 13px;
  color: var(--arkade-error-color, #d03050);
}
</style>
