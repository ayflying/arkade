<script setup lang="ts">
/**
 * 存储卷面包屑
 *
 * 展示「卷名 / dir / subdir」并可逐级点击回退。
 *
 * 为什么不用「返回上级」按钮就够：
 * 深目录下用户常常想直接跳回根目录或中间某一层，
 * 只能一级一级退会很难受。面包屑一次解决。
 */
import { computed } from 'vue'
import { NButton } from 'naive-ui'

interface Props {
  /** 卷名（面包屑第一级，点击回卷列表） */
  volume: string
  /** 当前相对路径；根目录为空串 */
  path: string
}

const props = defineProps<Props>()

const emit = defineEmits<{
  /** 点击卷名：回到卷列表 */
  backToVolumes: []
  /** 点击某一级：跳到该相对路径（根目录为空串） */
  navigate: [path: string]
}>()

/** 路径分段；空串表示根目录 */
const segments = computed<string[]>(() =>
  props.path === '' ? [] : props.path.split('/').filter((s) => s !== '')
)

/** 累积每一级的相对路径，供点击跳转 */
const crumbs = computed<{ name: string; path: string }[]>(() => {
  const out: { name: string; path: string }[] = []
  let acc = ''
  for (const seg of segments.value) {
    acc = acc === '' ? seg : `${acc}/${seg}`
    out.push({ name: seg, path: acc })
  }
  return out
})
</script>

<template>
  <nav class="breadcrumb" aria-label="目录路径">
    <n-button text class="breadcrumb__root" @click="emit('backToVolumes')">
      {{ volume }}
    </n-button>

    <template v-for="crumb in crumbs" :key="crumb.path">
      <span class="breadcrumb__sep">/</span>
      <n-button text class="breadcrumb__seg" @click="emit('navigate', crumb.path)">
        {{ crumb.name }}
      </n-button>
    </template>

    <!-- 根目录时给一个明确的位置感，否则只有一个卷名看起来像没在目录里 -->
    <template v-if="crumbs.length === 0">
      <span class="breadcrumb__sep">/</span>
      <span class="breadcrumb__current">根目录</span>
    </template>
  </nav>
</template>

<style scoped>
.breadcrumb {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: 2px;
  min-width: 0;
  font-size: 13px;
}

.breadcrumb__root {
  font-weight: 600;
}

.breadcrumb__sep {
  color: var(--arkade-text-tertiary);
  margin: 0 2px;
}

.breadcrumb__seg {
  font-weight: 400;
}

.breadcrumb__current {
  color: var(--arkade-text-tertiary);
}
</style>
