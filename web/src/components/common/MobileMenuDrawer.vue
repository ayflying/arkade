<script setup lang="ts">
/**
 * 手机端抽屉菜单
 *
 * 独立成组件的原因：
 * 1. AppLayout 已经要负责「三档布局编排」，再塞进抽屉的头部/尾部/样式会继续膨胀；
 * 2. 抽屉与侧边栏装的是**同一份** SiteMenu，把它单独封装后，
 *    「抽屉里有哪些内容」这件事集中在一个文件，改动时不至于翻整个布局文件。
 *
 * 功能完整性（任务要求「移动端可简化展示，但不能丢功能」）：
 * - 菜单导航：与桌面完全一致（同一个 SiteMenu 组件、同一份配置）；
 * - 退出登录：抽屉底部保留按钮，且顶栏用户下拉里也有一份。
 */
import { NButton, NDrawer, NDrawerContent } from 'naive-ui'
import SiteMenu from '@/components/common/SiteMenu.vue'
import { SIDER_WIDTH_EXPANDED } from '@/composables/useResponsive'

interface Props {
  /** 是否显示（v-model:show） */
  show: boolean
  /** 当前高亮的菜单 key */
  activeKey: string | null
  /**
   * 当前选中的服务器 id。
   * 手机端必须一并传入——否则抽屉里看不到「容器/编排/镜像/网络/卷」操作菜单，
   * 移动端就成了残缺功能（任务明确要求抽屉里也要有）。
   */
  agentId: string | null
  /** 当前服务器名，展示在操作分组标题上，让用户确认自己点的是哪台机器 */
  agentName?: string
  /** 当前用户名，展示在抽屉底部 */
  userName: string
  /** 退出登录进行中 */
  loggingOut?: boolean
}

withDefaults(defineProps<Props>(), {
  agentName: '',
  loggingOut: false
})

const emit = defineEmits<{
  (e: 'update:show', value: boolean): void
  (e: 'navigate'): void
  (e: 'logout'): void
}>()

/**
 * 抽屉宽度：取 220px 与 85vw 的较小值。
 *
 * 理由：小屏手机（如 320px 宽）上固定 220px 会几乎占满全屏，
 * 用户看不到背后的页面，容易误以为整页都是菜单；
 * 保留 15vw 的可见区域能明确传达「这是一个浮层」。
 */
const drawerWidth = `min(${SIDER_WIDTH_EXPANDED}px, 85vw)`

function handleUpdateShow(value: boolean): void {
  emit('update:show', value)
}
</script>

<template>
  <n-drawer :show="show" :width="drawerWidth" placement="left" @update:show="handleUpdateShow">
    <n-drawer-content :native-scrollbar="false" body-content-style="padding: 0;" closable>
      <template #header>
        <div class="drawer-header">
          <img src="/favicon.svg" alt="Arkade 图标" class="drawer-header__logo" />
          <span class="drawer-header__title">Arkade</span>
          <!-- 选中服务器时在标题右侧显示机器名，避免用户在抽屉里忘了当前是哪台 -->
          <span v-if="agentName" class="drawer-header__agent">{{ agentName }}</span>
        </div>
      </template>

      <!-- 与桌面侧边栏共用同一份菜单；导航完成后由父组件关闭抽屉 -->
      <site-menu :active-key="activeKey" :agent-id="agentId" @navigate="emit('navigate')" />

      <template #footer>
        <div class="drawer-footer">
          <span class="drawer-footer__user">{{ userName || '未命名用户' }}</span>
          <n-button size="small" :loading="loggingOut" @click="emit('logout')">退出登录</n-button>
        </div>
      </template>
    </n-drawer-content>
  </n-drawer>
</template>

<style scoped>
.drawer-header {
  display: flex;
  align-items: center;
  gap: 8px;
  /*
   * 关键：不换行 + 允许收缩。
   * 起初没写 wrap，窄抽屉里「Arkade」被压成一列（Ark
   * ade），机器名还会叠到品牌名上。因此标题和图标都设为不收缩，
   * 只让机器名那段去省略。
   */
  flex-wrap: nowrap;
  min-width: 0;
}

.drawer-header__logo {
  width: 24px;
  height: 24px;
  flex-shrink: 0;
}

.drawer-header__title {
  font-size: 16px;
  font-weight: 600;
  color: var(--arkade-text-primary);
  /* 品牌名必须完整显示，绝不参与压缩 */
  flex-shrink: 0;
  white-space: nowrap;
}

/* 机器名可能很长，用省略号收尾，绝不把标题挤出抽屉 */
.drawer-header__agent {
  margin-left: 4px;
  padding: 1px 6px;
  font-size: 12px;
  color: var(--arkade-text-secondary);
  background: var(--arkade-page-bg);
  border-radius: 4px;
  /* 只有这一段可以被压缩，于是省略号总是落在它身上 */
  min-width: 0;
  max-width: 110px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.drawer-footer {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
}

/* 用户名可能很长，必须允许省略，否则会把退出按钮挤出抽屉 */
.drawer-footer__user {
  font-size: 13px;
  color: var(--arkade-text-tertiary);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
</style>
