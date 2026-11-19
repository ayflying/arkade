<script setup lang="ts">
/**
 * .env 编辑区（新建编排 / 编辑编排共用）
 *
 * ## 这个组件解决什么问题
 *
 * compose 里写 `${DB_PASSWORD}` 是标准做法，但只给一个 compose 编辑器时，
 * 用户把它贴进去后服务起不来——因为插值值没地方放，报错还只说「变量未设置」。
 * 所以 compose 旁边必须有 .env 的位置。
 *
 * ## 三条硬性约束
 *
 * 1. **默认隐藏**
 *    .env 里通常就是密码、token。默认折叠，用户要编辑才展开，
 *    避免密码在别人路过屏幕时随手可见。也避免「我没想改它，但它一直显示着」的心理负担。
 *
 * 2. **绝不写入任何持久化位置**
 *    不写 Pinia、不写 localStorage / sessionStorage、不进 URL、不打印控制台。
 *    内容只存在于本组件的局部 ref 与父组件提交时的请求体里。
 *    `onUnmounted` 里还会主动清空，防止离开页面后仍被闭包引用住。
 *
 * 3. **不假装它会被注入容器**
 *    很多用户以为 .env 里的变量会自动进容器环境。实际只有 compose 中
 *    显式写了 `${VAR}` 的才会被插值进去。这个说明必须写在组件里，
 *    否则用户会以为「我在这里加了变量，容器里就该有」。
 *
 * ## 为什么用 v-model 而不是父组件直接读 ref
 * 父组件需要在提交时拿到内容，也需要在「用户是否改动过」的判断上有依据。
 * 通过 v-model 双向绑定最简单，且「改动过」由父组件用初始值对比即可判断，
 * 不需要本组件额外暴露状态。
 */
import { computed, onUnmounted, ref, watch } from 'vue'
import { NAlert, NButton, NInput, NTag } from 'naive-ui'

interface Props {
  /** .env 文本内容（v-model） */
  modelValue: string
  /** 是否默认展开；新建页默认收起，详情页同样收起 */
  defaultOpen?: boolean
  /** 只读（例如查看态） */
  readonly?: boolean
  /** 提交中：禁止编辑与切换，避免内容在请求中途被改 */
  disabled?: boolean
}

const props = withDefaults(defineProps<Props>(), {
  defaultOpen: false,
  readonly: false,
  disabled: false
})

const emit = defineEmits<{
  (e: 'update:modelValue', value: string): void
}>()

/** 是否展开编辑区。默认收起是安全默认值，不是可选项 */
const open = ref(props.defaultOpen)

/** 是否处于「显示明文」状态。默认打码，用户显式点开才显示 */
const reveal = ref(false)

const value = computed({
  get: () => props.modelValue,
  set: (v: string) => emit('update:modelValue', v)
})

/** 行数提示：让用户知道当前有多少条变量，不必展开也能判断「有没有配」 */
const lineCount = computed(() => {
  const text = props.modelValue
  if (text.trim() === '') return 0
  return text.split('\n').filter((line) => line.trim() !== '' && !line.trim().startsWith('#')).length
})

/**
 * 打码后的展示文本。
 *
 * 只用于「收起状态下想瞄一眼结构」的场景：变量名保留，值全部替换成 ****。
 * 这样既不泄露密码，又能确认「变量名对不对、有没有这一条」。
 */
const maskedPreview = computed(() => {
  return props.modelValue
    .split('\n')
    .map((line) => {
      const trimmed = line.trim()
      if (trimmed === '' || trimmed.startsWith('#')) return line
      const eq = line.indexOf('=')
      if (eq <= 0) return line
      return `${line.slice(0, eq + 1)}******`
    })
    .join('\n')
})

/** .env 内容变化时，如果处于「显示明文」且内容被清空，自动收回到打码态 */
watch(
  () => props.modelValue,
  (v) => {
    if (v === '') reveal.value = false
  }
)

/**
 * 卸载时清空父组件绑定的值。
 *
 * 父组件通常会在自己的 onUnmounted 里清，但那是父组件的责任；
 * 本组件至少不能自己再留一份引用。这里只清「显示明文」这个状态，
 * 内容由父组件负责清（因为 v-model 是父组件的 ref，本组件无权越界改写）。
 */
onUnmounted(() => {
  reveal.value = false
  open.value = false
})

/** 给父组件用：当前是否有内容（判断要不要在提交时带上 env_content） */
defineExpose({ hasContent: computed(() => props.modelValue.trim() !== '') })
</script>

<template>
  <div class="env-editor">
    <div class="env-editor__bar">
      <button type="button" class="env-editor__toggle" :disabled="disabled" @click="open = !open">
        <span class="env-editor__chevron" :class="{ 'env-editor__chevron--open': open }">▸</span>
        <span class="env-editor__title">.env</span>
        <n-tag v-if="lineCount > 0" size="small" :bordered="false" type="info">
          {{ lineCount }} 个变量
        </n-tag>
        <n-tag v-else size="small" :bordered="false">未设置</n-tag>
        <span class="env-editor__hint">可选 · 用于 compose 里的 ${VAR} 插值</span>
      </button>

      <div v-if="open" class="env-editor__actions">
        <!-- 显示明文是显式动作，默认打码 -->
        <n-button
          size="tiny"
          quaternary
          :disabled="disabled"
          @click="reveal = !reveal"
        >
          {{ reveal ? '隐藏内容' : '显示内容' }}
        </n-button>
      </div>
    </div>

    <template v-if="open">
      <!--
        只在有内容且未显示明文时才给打码预览。
        没内容时这块是空的，用户展开后看不到任何可输入的地方，
        会以为"这组件坏了"——所以下面必须有"开始填写"按钮兜住。
      -->
      <pre v-if="!reveal && lineCount > 0" class="env-editor__masked">{{ maskedPreview }}</pre>

      <!--
        打码态下没有可输入的输入框，必须有一个明确的入口切到编辑态。
        以前只能靠右上角那颗「显示内容」按钮，它看起来像"展示用途"而不是
        "开始编辑"，于是用户展开后点不进任何地方，以为编辑不了。
      -->
      <div v-if="!reveal" class="env-editor__enter">
        <n-button size="small" type="primary" ghost :disabled="disabled" @click="reveal = true">
          {{ lineCount > 0 ? '编辑内容' : '开始填写 .env' }}
        </n-button>
        <span class="env-editor__enter-hint">
          {{ lineCount > 0 ? '内容默认打码，点此显示明文并编辑' : '还没有变量，点此展开输入框' }}
        </span>
      </div>

      <n-input
        v-if="reveal"
        :value="value"
        type="textarea"
        class="env-editor__input"
        placeholder="DB_PASSWORD=change-me&#10;API_KEY=&#10;LOG_LEVEL=info"
        :autosize="{ minRows: 4, maxRows: 12 }"
        :readonly="readonly"
        :disabled="disabled"
        spellcheck="false"
        @update:value="(v: string) => (value = v)"
      />

      <n-alert type="info" :bordered="false" class="env-editor__note">
        <!--
          这里必须说准，说错会直接误导用户排查方向：
          1. .env 的**默认**作用是给 compose 做 ${VAR} 插值，而且不止 environment，
             image、ports、volumes 里都能用——这些是「构建/解析期」就替换掉的；
          2. 变量**进入容器**是另一回事：靠 environment 声明，
             或用 env_file 显式加载 .env 这个文件；
          3. env_file 可以指向**任意文件列表**，不一定是 .env ——
             说成「写了 env_file: .env 才行」会让人以为 env_file 只能用 .env。
        -->
        <code>.env</code> 与 compose.yml 放在同一目录，默认用于 Compose 的
        <code>${VAR}</code> <strong>变量插值</strong>（例如镜像名、端口、卷路径）。
        变量要<strong>传入容器</strong>需要另行声明：在 <code>environment</code> 中列出，
        或用 <code>env_file</code> 显式加载 <code>.env</code>。
        <br />
        内容不会被浏览器保存，离开本页即丢弃；密码请勿使用示例值。
      </n-alert>
    </template>
  </div>
</template>

<style scoped>
.env-editor {
  border: 1px solid var(--arkade-border-color);
  border-radius: 6px;
  overflow: hidden;
}

.env-editor__bar {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
  padding: 8px 10px;
  background: var(--arkade-bg-secondary);
  flex-wrap: wrap;
}

/* 用原生 button 而不是 n-button：整条标题栏都要可点，n-button 会撑出多余样式 */
.env-editor__toggle {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 0;
  border: none;
  background: none;
  cursor: pointer;
  font-size: 13px;
  color: var(--arkade-text-primary);
  text-align: left;
  flex-wrap: wrap;
}

.env-editor__toggle:disabled {
  cursor: not-allowed;
  opacity: 0.6;
}

.env-editor__chevron {
  display: inline-block;
  transition: transform 0.15s ease;
  color: var(--arkade-text-tertiary);
}

.env-editor__chevron--open {
  transform: rotate(90deg);
}

.env-editor__title {
  font-family: var(--arkade-mono-font);
  font-weight: 600;
}

.env-editor__hint {
  font-size: 12px;
  color: var(--arkade-text-tertiary);
}

.env-editor__actions {
  display: flex;
  gap: 4px;
}

/* 打码预览：等宽字体 + 保留换行，看起来就是 .env 的样子 */
.env-editor__masked {
  margin: 0;
  padding: 10px 12px;
  background: var(--arkade-bg-primary);
  font-family: var(--arkade-mono-font);
  font-size: 12px;
  line-height: 1.6;
  white-space: pre-wrap;
  word-break: break-all;
  color: var(--arkade-text-secondary);
  border-top: 1px solid var(--arkade-border-color);
}

/* 打码态下的进入编辑入口：按钮 + 一句为什么要点它 */
.env-editor__enter {
  display: flex;
  align-items: center;
  gap: 10px;
  flex-wrap: wrap;
  padding: 10px 12px;
  background: var(--arkade-bg-primary);
  border-top: 1px solid var(--arkade-border-color);
}

.env-editor__enter-hint {
  font-size: 12px;
  color: var(--arkade-text-tertiary);
}

.env-editor__input {
  border-top: 1px solid var(--arkade-border-color);
}

.env-editor__input :deep(textarea) {
  font-family: var(--arkade-mono-font);
  font-size: 12px;
  line-height: 1.6;
}

.env-editor__note {
  margin: 0;
  border-top: 1px solid var(--arkade-border-color);
  border-radius: 0;
  font-size: 12px;
  line-height: 1.7;
}

.env-editor__note code {
  padding: 1px 4px;
  font-family: var(--arkade-mono-font);
  background: rgba(0, 0, 0, 0.06);
  border-radius: 3px;
}

@media (max-width: 767.98px) {
  .env-editor__hint {
    display: none;
  }
}
</style>