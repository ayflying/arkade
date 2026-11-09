<script setup lang="ts">
/**
 * 侧边栏品牌区（Logo + 名称）
 *
 * 折叠态（平板）只显示 Logo 图标，展开态显示完整名称。
 * 单独成组件是因为桌面/平板侧边栏与手机抽屉都要用它，
 * 且折叠动画（文字淡出）只应在这里实现一次。
 */
interface Props {
  /** 是否折叠为图标（仅显示 Logo） */
  collapsed?: boolean
  /** 是否点击可返回首页（抽屉内使用，点击后需关闭抽屉） */
  clickable?: boolean
}

withDefaults(defineProps<Props>(), { collapsed: false, clickable: true })

const emit = defineEmits<{
  (e: 'navigate'): void
}>()
</script>

<template>
  <div class="brand" :class="{ 'brand--collapsed': collapsed }">
    <router-link to="/agents" class="brand__link" @click="emit('navigate')">
      <img src="/favicon.svg" alt="Arkade 图标" class="brand__logo" />
      <!-- 折叠时用 v-show 而非 v-if：保留 DOM 让宽度过渡更平滑，且避免频繁创建销毁 -->
      <span v-show="!collapsed" class="brand__title">Arkade</span>
    </router-link>
  </div>
</template>

<style scoped>
.brand {
  display: flex;
  align-items: center;
  height: var(--arkade-header-height);
  padding: 0 18px;
  border-bottom: 1px solid var(--arkade-border-color);
  overflow: hidden;
  flex-shrink: 0;
}

/* 折叠态：收窄内边距并居中 Logo，避免 Logo 偏左看起来像没对齐 */
.brand--collapsed {
  padding: 0;
  justify-content: center;
}

.brand__link {
  display: flex;
  align-items: center;
  gap: 8px;
  text-decoration: none;
  white-space: nowrap;
}

.brand__logo {
  width: 24px;
  height: 24px;
  flex-shrink: 0;
}

.brand__title {
  font-size: 16px;
  font-weight: 600;
  color: var(--arkade-text-primary);
}
</style>
