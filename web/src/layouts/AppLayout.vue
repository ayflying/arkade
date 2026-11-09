<script setup lang="ts">
/**
 * 业务页布局：左菜单 + 顶栏 + 内容区
 *
 * 三档响应式策略（断点理由见 composables/useResponsive.ts）：
 * - 桌面 >=1024px：NLayoutSider 展开 220px，可手动折叠；
 * - 平板 768–1023px：NLayoutSider 自动折叠为 64px 图标栏（仍可手动展开）；
 * - 手机 <768px   ：不渲染 NLayoutSider，改用 NDrawer 抽屉 + 顶栏汉堡按钮唤出。
 *
 * 关于「为什么手机端用 v-if 而不是让 NLayoutSider 自己隐藏」：
 * NLayoutSider 的 collapse-mode="width" 只是收窄为窄栏，并不会消失；
 * 在 375px 屏幕上留 64px 窄栏会严重挤压内容，因此手机端直接不渲染侧边栏。
 *
 * 关于「折叠状态由谁说了算」：
 * 用户手动折叠的意图需要保留，但跨越断点时应以断点为准
 * （手机→平板要自动收起，平板→桌面要自动展开）。因此用 watch 在档位切换时
 * 重设 collapsed，平时则允许用户手动切换。
 */
import { computed, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { NButton, NIcon, NLayout, NLayoutContent, NLayoutSider } from 'naive-ui'
import AppBreadcrumb from '@/components/common/AppBreadcrumb.vue'
import AppIcon from '@/components/common/AppIcon.vue'
import MobileMenuDrawer from '@/components/common/MobileMenuDrawer.vue'
import SidebarBrand from '@/components/common/SidebarBrand.vue'
import SiteMenu from '@/components/common/SiteMenu.vue'
import UserMenu from '@/components/common/UserMenu.vue'
import { useCurrentAgent } from '@/composables/useCurrentAgent'
import { SIDER_WIDTH_COLLAPSED, SIDER_WIDTH_EXPANDED, useResponsive } from '@/composables/useResponsive'
import { resolveActiveMenuKey } from '@/layouts/menuConfig'
import { useAuthStore } from '@/stores/auth'
import { toastError } from '@/utils/feedback'

const auth = useAuthStore()
const router = useRouter()
const route = useRoute()

const { isMobile, isTablet } = useResponsive()

/**
 * 当前选中的服务器 id。
 *
 * 改造后侧边菜单是「两层」的：顶部「服务器」入口常驻，
 * 只有选中某台服务器（URL 命中 /agents/:id/*）后，下方才展开
 * 容器/编排/镜像/网络/卷这 5 个操作菜单。
 * 因此 agentId 必须传给 SiteMenu，由它决定渲染哪些菜单项，
 * 并在点击操作菜单时把 id 带进跳转参数。
 */
const { agentId, agent, ensureAgentsLoaded } = useCurrentAgent()

/**
 * 深链进入时补拉服务器列表。
 *
 * 服务器列表原本只在列表页拉取，但用户可以直接打开
 * `/agents/xxx/stacks` 之类的深链，此时 store 是空的，
 * 各页面会误判「服务器离线」并禁用所有操作按钮（实测踩到的 bug）。
 *
 * 这里在布局层兜住：只要 URL 里带了服务器 id 而列表还没加载过，就补拉一次。
 * 放在布局而不是各页面，是因为布局是所有服务器页面的公共父级，只写一处即可。
 * immediate 保证首次挂载就检查；agentId 变化时也再查一次（切到列表页再切回来）。
 */
watch(
  () => agentId.value,
  (id) => {
    if (id !== null) void ensureAgentsLoaded()
  },
  { immediate: true }
)

/** 当前应高亮的菜单 key */
const activeMenuKey = computed<string | null>(() => resolveActiveMenuKey(route.name))

/**
 * 侧边栏是否折叠。
 * 默认按档位给初值：桌面展开、平板收起；之后允许用户手动切换。
 */
const sidebarCollapsed = ref(false)

/**
 * 手机端抽屉是否打开。
 * 切换路由、点击菜单项、窗口变宽离开手机档时都必须关闭，
 * 否则抽屉会挡住新页面。
 */
const drawerVisible = ref(false)

/** 退出登录请求中，防止连点 */
const loggingOut = ref(false)

/**
 * 档位切换时重置折叠状态。
 *
 * 只在档位真正变化时执行（watch 默认非 deep，比较布尔即可），
 * 避免用户在桌面手动折叠后，任何一次无关重渲染又把状态覆盖掉。
 *
 * 规则：
 * - 进入手机档：抽屉关闭（手机端不使用 collapsed）；
 * - 平板：默认收起为图标栏；
 * - 桌面：默认展开。
 * 这三个分支都依赖 isTablet / isMobile 的互斥性（见 useResponsive 的推导逻辑）。
 */
watch(
  [isMobile, isTablet],
  ([mobileNow, tabletNow]) => {
    if (mobileNow) {
      drawerVisible.value = false
      return
    }
    sidebarCollapsed.value = tabletNow
  },
  { immediate: true }
)

/** 路由变化时关闭抽屉（覆盖浏览器前进/后退、外部跳转等非菜单点击场景） */
watch(
  () => route.fullPath,
  () => {
    drawerVisible.value = false
  }
)

/** 汉堡按钮：打开抽屉 */
function openDrawer(): void {
  drawerVisible.value = true
}

/** 抽屉关闭回调 */
function closeDrawer(): void {
  drawerVisible.value = false
}

/** 手动折叠/展开侧边栏（仅宽屏可用） */
function toggleSidebar(): void {
  sidebarCollapsed.value = !sidebarCollapsed.value
}

/**
 * 退出登录。
 * 行为与上一版完全一致：清本地登录态 → 跳登录页；失败也给用户明确反馈。
 */
async function handleLogout(): Promise<void> {
  if (loggingOut.value) return
  loggingOut.value = true
  try {
    await auth.logout()
    closeDrawer()
    await router.replace('/login')
  } catch {
    toastError('退出登录失败，已清理本地登录状态，请刷新页面')
  } finally {
    loggingOut.value = false
  }
}
</script>

<template>
  <n-layout class="app-shell" has-sider>
    <!-- ===== 宽屏（>=768px）：左侧固定侧边栏 ===== -->
    <n-layout-sider
      v-if="!isMobile"
      class="app-sider"
      bordered
      collapse-mode="width"
      :collapsed="sidebarCollapsed"
      :collapsed-width="SIDER_WIDTH_COLLAPSED"
      :width="SIDER_WIDTH_EXPANDED"
      :native-scrollbar="false"
      @collapse="sidebarCollapsed = true"
      @expand="sidebarCollapsed = false"
    >
      <sidebar-brand :collapsed="sidebarCollapsed" />

      <div class="app-sider__menu">
        <site-menu
          :active-key="activeMenuKey"
          :agent-id="agentId"
          :collapsed="sidebarCollapsed"
        />
      </div>

      <!-- 底部折叠按钮：窄栏时只显示图标按钮，居中 -->
      <div class="app-sider__footer">
        <n-button
          quaternary
          size="small"
          class="app-sider__toggle"
          :class="{ 'app-sider__toggle--collapsed': sidebarCollapsed }"
          @click="toggleSidebar"
        >
          <template #icon>
            <n-icon :size="18" :class="{ 'app-sider__toggle-icon--collapsed': sidebarCollapsed }">
              <app-icon name="collapse" />
            </n-icon>
          </template>
          <span v-show="!sidebarCollapsed">收起菜单</span>
        </n-button>
      </div>
    </n-layout-sider>

    <!-- ===== 右侧：顶栏 + 内容区 ===== -->
    <n-layout class="app-body">
      <header class="app-header">
        <!-- 手机端汉堡按钮：唤出抽屉菜单 -->
        <n-button
          v-if="isMobile"
          quaternary
          circle
          class="app-header__hamburger"
          aria-label="打开菜单"
          @click="openDrawer"
        >
          <template #icon>
            <n-icon :size="20"><app-icon name="menu" /></n-icon>
          </template>
        </n-button>

        <!-- 手机端顶栏放品牌，宽屏时品牌在侧边栏里，这里省略避免重复 -->
        <span v-if="isMobile" class="app-header__brand">Arkade</span>

        <app-breadcrumb :compact="isMobile" class="app-header__crumbs" />

        <div class="app-header__spacer" />

        <user-menu :compact="isMobile" :logging-out="loggingOut" @logout="handleLogout" />
      </header>

      <n-layout-content class="app-content" :native-scrollbar="false">
        <div class="app-content__inner">
          <router-view />
        </div>
      </n-layout-content>
    </n-layout>
  </n-layout>

  <!-- ===== 手机端抽屉菜单（具体实现见 MobileMenuDrawer） ===== -->
  <mobile-menu-drawer
    v-model:show="drawerVisible"
    :active-key="activeMenuKey"
    :agent-id="agentId"
    :agent-name="agent?.name ?? ''"
    :user-name="auth.displayName"
    :logging-out="loggingOut"
    @navigate="closeDrawer"
    @logout="handleLogout"
  />
</template>

<style scoped>
/* 外层容器撑满视口高度，内部各自滚动 */
.app-shell {
  height: 100vh;
  background: var(--arkade-page-bg);
}

.app-sider {
  /* 侧边栏背景与顶栏统一为白色，内容区用浅灰，形成层次 */
  background: var(--arkade-surface-bg);
}

.app-sider__menu {
  flex: 1;
  overflow-y: auto;
  overflow-x: hidden;
  padding-top: 8px;
  /* flex 子项默认 min-height:auto 会被内容撑开，导致底部按钮被顶出视口 */
  min-height: 0;
}

.app-sider__footer {
  flex-shrink: 0;
  padding: 8px;
  border-top: 1px solid var(--arkade-border-color);
}

.app-sider__toggle {
  width: 100%;
  justify-content: flex-start;
}

/* 折叠态：按钮只剩图标，此时居中对齐才不显得偏左 */
.app-sider__toggle--collapsed {
  justify-content: center;
}

/*
 * 关键：NLayoutSider 设置 :native-scrollbar="false" 后，内部真实结构是
 *   aside.n-layout-sider > div.n-scrollbar > div.n-scrollbar-container > div.n-scrollbar-content
 * 其中 .n-scrollbar-content 高度只等于内容高度（实测 206px），不会被撑满，
 * 因此必须让「它」成为撑满高度的纵向 flex 容器，
 * .app-sider__menu 的 flex:1 才能把「收起菜单」按钮压到侧边栏底部。
 *
 * 踩过的坑：只给外层 .n-layout-sider 加 flex 不生效（外层只负责宽度与背景），
 * 按钮会紧贴菜单、下方留一大片空白。
 */
.app-sider :deep(.n-scrollbar-content) {
  display: flex;
  flex-direction: column;
  min-height: 100%;
}

/* 折叠时把双向箭头旋转，直观表达「再次点击将展开」 */
.app-sider__toggle-icon--collapsed {
  transform: rotate(180deg);
}

.app-body {
  /* 关键：flex 子项默认 min-width:auto，内部宽表格会把侧边栏挤扁 */
  min-width: 0;
  height: 100vh;
  display: flex;
  flex-direction: column;
}

.app-header {
  display: flex;
  align-items: center;
  gap: 12px;
  flex-shrink: 0;
  height: var(--arkade-header-height);
  padding: 0 16px;
  background: var(--arkade-surface-bg);
  border-bottom: 1px solid var(--arkade-border-color);
  /* 保证顶栏在小屏也不因内容超出而撑出横向滚动 */
  min-width: 0;
}

.app-header__hamburger {
  flex-shrink: 0;
}

.app-header__brand {
  font-size: 16px;
  font-weight: 600;
  color: var(--arkade-text-primary);
  flex-shrink: 0;
}

.app-header__crumbs {
  min-width: 0;
  overflow: hidden;
}

/* 把用户区推到最右侧，替代原来的 space-between */
.app-header__spacer {
  flex: 1;
  min-width: 0;
}

/*
 * 内容区撑满剩余高度。
 *
 * NLayoutContent 内部又是一层 .n-scrollbar > .n-scrollbar-container > .n-scrollbar-content，
 * 它们默认都是 height: auto（按内容计算），导致内容区只占内容高度（实测 351px），
 * 页面下方出现一大块与侧边栏不对齐的空白，滚动也发生在错误的层级上。
 *
 * 解决办法：让这条链路每一层都 height:100%、min-height:0，
 * 滚动交给最内层的 .n-scrollbar-container 处理。
 */
.app-content {
  flex: 1 1 auto;
  min-height: 0;
  height: 100%;
  background: var(--arkade-page-bg);
}

.app-content :deep(.n-scrollbar),
.app-content :deep(.n-scrollbar-container),
.app-content :deep(.n-scrollbar-content) {
  height: 100%;
  min-height: 0;
}

.app-content__inner {
  /* 内容区自身也要 min-width: 0，才能让内部表格的横向滚动生效而不是撑破外层 */
  min-width: 0;
  max-width: var(--arkade-content-max-width);
  margin: 0 auto;
  padding: 24px;
}

/* 手机端减少留白，把有限的宽度让给内容 */
@media (max-width: 767.98px) {
  .app-content__inner {
    padding: 12px;
  }
}
</style>
