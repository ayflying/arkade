<script setup lang="ts">
/**
 * 卷文件页的顶部工具条
 *
 * 单独成组件而不是留在页面里：页面已承载路由、保存、增删改，
 * 工具条纯粹是「面包屑 + 新建/上传按钮」，拆开后页面模板短一截。
 *
 * ## 上传用隐藏的 <input type="file">
 *
 * 不用拖拽区：拖拽在大目录页容易误触，而且这个页面直接改在线业务数据，
 * 上传应该是**明确的一次点击动作**，不是随手拖进来就发生。
 */
import { ref } from 'vue'
import { NButton, NDropdown } from 'naive-ui'
import VolumeBreadcrumb from './VolumeBreadcrumb.vue'

interface Props {
  volume: string
  path: string
  /** 服务器离线时禁用所有写入口 */
  disabled: boolean
  /** 上传进行中：禁用按钮并显示 loading，防止重复提交 */
  uploading?: boolean
}

const props = withDefaults(defineProps<Props>(), { uploading: false })

const emit = defineEmits<{
  navigate: [path: string]
  create: [kind: 'file' | 'dir']
  upload: [file: File]
}>()

/** 隐藏的文件选择器 */
const createOptions = [{ label: '文件', key: 'file' }, { label: '文件夹', key: 'dir' }]

function selectCreate(key: string | number): void {
  if (!props.disabled && (key === 'file' || key === 'dir')) emit('create', key)
}

const fileInputRef = ref<HTMLInputElement | null>(null)

/** 点「上传」先打开文件选择器 */
function pickFile(): void {
  if (props.disabled || props.uploading) return
  fileInputRef.value?.click()
}

/** 选中文件后交给父组件处理，并立刻清空 input，允许再次选同一个文件 */
function handleFileChange(event: Event): void {
  const input = event.target as HTMLInputElement
  const file = input.files?.[0]
  if (file === undefined) return
  emit('upload', file)
  // 不清空的话，用户「上传失败后想重试同一个文件」不会触发 change 事件
  input.value = ''
}
</script>

<template>
  <div class="vol-toolbar">
    <volume-breadcrumb
      :volume="volume"
      :path="path"
      @navigate="emit('navigate', $event)"
    />
    <div class="vol-toolbar__actions">
      <n-button size="small" :disabled="disabled || uploading" :loading="uploading" @click="pickFile">
        上传文件
      </n-button>
      <n-dropdown trigger="click" :options="createOptions" :disabled="disabled" @select="selectCreate">
        <n-button size="small" type="primary" :disabled="disabled">新建</n-button>
      </n-dropdown>
      <!-- 隐藏的原生文件选择器：只由上面的按钮触发 -->
      <input
        ref="fileInputRef"
        type="file"
        class="vol-toolbar__file"
        tabindex="-1"
        aria-hidden="true"
        @change="handleFileChange"
      />
    </div>
  </div>
</template>

<style scoped>
.vol-toolbar {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  flex-wrap: wrap;
  margin-bottom: 4px;
}

.vol-toolbar__actions {
  display: flex;
  align-items: center;
  gap: 8px;
  flex-wrap: wrap;
}

/*
  隐藏但保持可编程点击：0 尺寸 + opacity 0。
  不用 display 隐藏——部分浏览器对隐藏元素的 .click() 不触发文件选择器。
*/
.vol-toolbar__file {
  position: absolute;
  width: 0;
  height: 0;
  padding: 0;
  border: 0;
  opacity: 0;
  pointer-events: none;
}
</style>
