<script setup lang="ts">
/**
 * 业务页布局：顶栏 + 内容区
 *
 * 顶栏职责：品牌、当前页导航入口、当前用户名、退出登录。
 * 做成布局路由（/agents 的父级）而不是每个页面各写一份顶栏，
 * 保证所有业务页外观一致，且退出登录逻辑只实现一次。
 */
import { computed, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { NBreadcrumb, NBreadcrumbItem, NButton, NDropdown, NTag } from 'naive-ui'
import type { DropdownOption } from 'naive-ui'
import { useAuthStore } from '@/stores/auth'
import { toastError } from '@/utils/feedback'

const auth = useAuthStore()
const router = useRouter()
const route = useRoute()

/** 退出登录请求中，防止连点 */
const loggingOut = ref(false)

/**
 * 面包屑：根据当前路由推导。
 * /agents          → 服务器
 * /agents/:id      → 服务器 / 容器（id 由页面自己展示标题，面包屑只做层级）
 */
const breadcrumbs = computed<Array<{ label: string; to?: string }>>(() => {
  const items: Array<{ label: string; to?: string }> = [{ label: '服务器', to: '/agents' }]
  if (route.name === 'agent-containers') {
    items.push({ label: '容器' })
  }
  return items
})

/** 用户名下拉菜单：目前只有退出登录一项，后续可扩展个人设置 */
const userMenuOptions: DropdownOption[] = [{ label: '退出登录', key: 'logout' }]

/** 角色中文名；后端可能出现契约外的角色值，未知时不展示徽标而不是显示乱码 */
const ROLE_LABELS: Record<string, string> = {
  admin: '管理员',
  operator: '操作员',
  viewer: '只读'
}

const roleLabel = computed<string>(() => {
  const role = auth.user?.role ?? ''
  return ROLE_LABELS[role] ?? ''
})

/**
 * 是否展示角色徽标。
 * 只在「角色有中文名」且「角色名与显示名不相同」时展示：
 * 否则会出现「管理员 管理员」这种重复（display_name 本身就叫管理员时尤其明显）。
 */
const showRoleTag = computed<boolean>(() => {
  if (roleLabel.value === '') return false
  return roleLabel.value !== auth.displayName
})

async function handleLogout(): Promise<void> {
  if (loggingOut.value) return
  loggingOut.value = true
  try {
    await auth.logout()
    await router.replace('/login')
  } catch {
    toastError('退出登录失败，已清理本地登录状态，请刷新页面')
  } finally {
    loggingOut.value = false
  }
}

function handleUserMenuSelect(key: string): void {
  if (key === 'logout') {
    void handleLogout()
  }
}
</script>

<template>
  <div class="app-layout">
    <header class="app-header">
      <div class="app-header__inner">
        <div class="app-header__left">
          <router-link to="/agents" class="app-header__brand">
            <img src="/favicon.svg" alt="Arkade 图标" class="app-header__logo" />
            <span class="app-header__title">Arkade</span>
          </router-link>
          <n-breadcrumb class="app-header__breadcrumb">
            <n-breadcrumb-item v-for="item in breadcrumbs" :key="item.label">
              <router-link v-if="item.to" :to="item.to">{{ item.label }}</router-link>
              <span v-else>{{ item.label }}</span>
            </n-breadcrumb-item>
          </n-breadcrumb>
        </div>

        <div class="app-header__right">
          <n-dropdown
            trigger="click"
            :options="userMenuOptions"
            :disabled="loggingOut"
            @select="handleUserMenuSelect"
          >
            <n-button quaternary>
              {{ auth.displayName || '未命名用户' }}
            </n-button>
          </n-dropdown>
          <!-- 角色徽标独立于用户名：仅当角色不是默认的普通用户时才展示，避免「管理员 管理员」重复 -->
          <n-tag
            v-if="showRoleTag"
            size="small"
            :bordered="false"
            type="info"
            class="app-header__role"
          >
            {{ roleLabel }}
          </n-tag>
          <n-button size="small" :loading="loggingOut" :disabled="loggingOut" @click="handleLogout">
            退出登录
          </n-button>
        </div>
      </div>
    </header>

    <main class="app-main">
      <div class="app-main__inner">
        <router-view />
      </div>
    </main>
  </div>
</template>

<style scoped>
.app-layout {
  display: flex;
  flex-direction: column;
  min-height: 100vh;
  background: var(--arkade-page-bg);
}

.app-header {
  position: sticky;
  top: 0;
  z-index: 10;
  height: var(--arkade-header-height);
  background: #fff;
  border-bottom: 1px solid #eef0f3;
}

.app-header__inner {
  display: flex;
  align-items: center;
  justify-content: space-between;
  height: 100%;
  max-width: var(--arkade-content-max-width);
  margin: 0 auto;
  padding: 0 24px;
}

.app-header__left {
  display: flex;
  align-items: center;
  gap: 20px;
  min-width: 0;
}

.app-header__brand {
  display: flex;
  align-items: center;
  gap: 8px;
  text-decoration: none;
}

.app-header__logo {
  width: 24px;
  height: 24px;
}

.app-header__title {
  font-size: 16px;
  font-weight: 600;
  color: #1f2225;
}

.app-header__breadcrumb {
  font-size: 13px;
}

.app-header__right {
  display: flex;
  align-items: center;
  gap: 8px;
}

/* 角色徽标不参与收缩，避免窄屏时被挤成一行数字 */
.app-header__role {
  flex-shrink: 0;
}

.app-main {
  flex: 1;
}

.app-main__inner {
  max-width: var(--arkade-content-max-width);
  margin: 0 auto;
  padding: 24px;
}
</style>
