<script setup lang="ts">
/**
 * 外部项目配置面板
 *
 * 独立成组件：它有四种截然不同的展示状态（无权限 / 读不到 / 正常 / 加载失败），
 * 放在页面里会与项目信息、容器表混在一起，很难读。
 *
 * ## 这个编辑器不碰服务器
 *
 * 「保存草稿」只写 Hub 草稿，**不改服务器上的文件**。
 * 所以这里没有任何「会不会影响运行」的确认——真正的动作是「登记到 Arkade」，
 * 那一步在父组件里，并且会先做预检。
 */
import { computed, ref, watch } from 'vue'
import { NAlert, NButton, NSpin, NTag } from 'naive-ui'
import CodeEditor from '@/components/common/CodeEditor.vue'
import type { ConfigState } from '@/composables/useExternalStack'
import type { ExternalStackDraft } from '@/types/externalStack'

interface Props {
  state: ConfigState
  rawContent: string
  reason: string
  errorText: string
  blockers: string[]
  draft: ExternalStackDraft | null
  saving: boolean
  draftError: string
  /** 是否允许写（manage 权限；无权限时按钮不给点） */
  canEdit: boolean
  /** 磁盘原文读不到、当前只能靠草稿编辑 */
  draftOnly: boolean
}

const props = defineProps<Props>()

const emit = defineEmits<{
  saveDraft: [content: string, expectedVersion: number]
  /** 当前编辑器内容变化（含草稿/磁盘原文的切换） */
  'update:content': [content: string]
}>()

/** 编辑器内容：优先草稿，其次磁盘原文 */
const content = ref('')

/** 是否正在看草稿而不是磁盘内容 */
const showingDraft = ref(false)

watch(
  () => [props.rawContent, props.draft] as const,
  () => {
    if (props.draft !== null) {
      content.value = props.draft.content
      showingDraft.value = true
    } else {
      content.value = props.rawContent
      showingDraft.value = false
    }
    emit('update:content', content.value)
  },
  { immediate: true }
)

/*
 * 编辑器里的实际内容必须向上同步。
 *
 * 复制配置时用的是「用户当前看到并编辑的内容」——如果父组件拿 rawContent，
 * 用户存了草稿或正在编辑改动，复制出去的还是磁盘原文，等于悄悄丢掉了他们的修改。
 */
watch(content, (value) => {
  emit('update:content', value)
})

/** 与「当前基准」不同即有未存改动 */
const baseline = computed(() => (props.draft !== null ? props.draft.content : props.rawContent))
const dirty = computed(() => content.value !== baseline.value)

/** 首次保存传 0，之后带当前版本做 CAS */
const expectedVersion = computed(() => props.draft?.version ?? 0)

const canSave = computed(() => props.canEdit && dirty.value && !props.saving)

/** 回到磁盘原文看看现在服务器上是什么 */
function showRaw(): void {
  content.value = props.rawContent
  showingDraft.value = false
}

/** 回到草稿 */
function showDraft(): void {
  if (props.draft === null) return
  content.value = props.draft.content
  showingDraft.value = true
}

function handleSave(): void {
  if (!canSave.value) return
  emit('saveDraft', content.value, expectedVersion.value)
}
</script>

<template>
  <div class="ext-config">
    <n-spin v-if="state === 'loading'" size="small" />

    <!--
      403：能看详情但没有 manage。这是权限设计的正常结果，
      措辞要说「需要权限」，不能说「加载失败」——后者会让人反复重试。
    -->
    <n-alert v-else-if="state === 'forbidden'" type="warning" :bordered="false" title="无权限查看配置">
      {{ reason }}
    </n-alert>

    <!-- 有权限但读不到：必须给出原因，绝不能显示成空白编辑器（保存会毁掉它） -->
    <n-alert
      v-else-if="state === 'unreadable'"
      type="info"
      :bordered="false"
      title="配置内容不可读"
    >
      {{ reason === '' ? '无法读取该项目的 compose 文件。' : reason }}
    </n-alert>

    <n-alert v-else-if="state === 'error'" type="error" :bordered="false" title="配置加载失败">
      {{ errorText }}
    </n-alert>

    <template v-else-if="state === 'ready'">
      <!--
        磁盘原文读不到、只剩草稿：必须讲清楚，否则用户会以为看到的就是服务器现状。
        草稿存在 Hub 数据库里，正是这种情况下的唯一幸存副本，所以照常可编辑。
      -->
      <n-alert v-if="draftOnly" type="warning" :bordered="false" title="服务器上的文件当前不可读">
        {{ reason === '' ? '无法读取该项目的 compose 文件。' : reason }}
        下面显示的是保存在 Hub 中的草稿，服务器上的文件没有被改动。
      </n-alert>

      <!-- 草稿状态条：让人清楚现在编辑的是草稿还是磁盘原文 -->
      <div v-if="draft !== null && !draftOnly" class="ext-config__draft">
        <n-tag size="small" :bordered="false" type="info">存在草稿</n-tag>
        <span class="ext-config__draft-text">草稿只存在 Hub，服务器上的文件没有被改动。</span>
        <n-button text size="tiny" @click="showDraft">编辑草稿</n-button>
        <n-button text size="tiny" @click="showRaw">查看磁盘原文</n-button>
      </div>
      <div v-else-if="!showingDraft" class="ext-config__draft">
        <n-tag size="small" :bordered="false">磁盘原文</n-tag>
      </div>

      <!--
        projectKey 区分「磁盘原文」与「草稿」两份不同文档：
        切换时清空撤销栈，避免在新草稿里 Ctrl+Z 把磁盘原文调回来。
      -->
      <code-editor
        v-model="content"
        aria-label="compose.yml 配置内容"
        :disabled="!canEdit"
        :project-key="showingDraft ? 'draft' : 'raw'"
        :min-height="340"
        :max-height="560"
      />

      <!--
        阻塞项只影响「登记到 Arkade」，不影响查看和存草稿。
        措辞要明确，否则用户会以为这块内容完全不能碰。
      -->
      <n-alert
        v-if="blockers.length > 0"
        type="warning"
        :bordered="false"
        title="这份配置暂时不能登记到 Arkade"
        class="ext-config__blockers"
      >
        <ul class="ext-config__blocker-list">
          <li v-for="item in blockers" :key="item">{{ item }}</li>
        </ul>
        <p class="ext-config__blocker-hint">仍然可以继续编辑并保存草稿。</p>
      </n-alert>

      <p v-if="draftError !== ''" class="ext-config__error">{{ draftError }}</p>

      <div class="ext-config__footer">
        <span v-if="dirty" class="ext-config__dirty">有未保存的改动</span>
        <n-button
          size="small"
          type="primary"
          :loading="saving"
          :disabled="!canSave"
          @click="handleSave"
        >
          保存草稿
        </n-button>
      </div>
      <p class="ext-config__hint">保存草稿只写入 Hub，不会修改服务器上的文件，也不会重启任何容器。</p>
    </template>
  </div>
</template>

<style scoped>
.ext-config {
  display: flex;
  flex-direction: column;
  gap: 10px;
}

.ext-config__draft {
  display: flex;
  align-items: center;
  gap: 8px;
  flex-wrap: wrap;
}

.ext-config__draft-text {
  font-size: 12px;
  color: var(--arkade-text-tertiary);
}

.ext-config__editor {
  font-family: var(--arkade-mono-font);
}

.ext-config__blocker-list {
  margin: 4px 0 0;
  padding-left: 18px;
}

.ext-config__blocker-hint {
  margin: 6px 0 0;
  font-size: 12px;
  color: var(--arkade-text-tertiary);
}

.ext-config__error {
  margin: 0;
  font-size: 13px;
  color: var(--arkade-error-color, #d03050);
}

.ext-config__footer {
  display: flex;
  align-items: center;
  justify-content: flex-end;
  gap: 10px;
}

.ext-config__dirty {
  font-size: 12px;
  color: var(--arkade-text-secondary);
}

.ext-config__hint {
  margin: 0;
  font-size: 12px;
  color: var(--arkade-text-tertiary);
}
</style>
