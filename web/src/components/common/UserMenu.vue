<script setup lang="ts">
import { computed, h } from 'vue'
import { useRouter } from 'vue-router'
import { NAvatar, NButton, NDropdown, NIcon } from 'naive-ui'
import type { DropdownOption } from 'naive-ui'
import AppIcon from '@/components/common/AppIcon.vue'
import type { IconName } from '@/components/common/iconNames'
import { useAuthStore } from '@/stores/auth'

const props = defineProps<{
  /** 手机顶栏只留头像，完整账户信息在下拉中显示。 */
  compact?: boolean
  loggingOut?: boolean
}>()
const emit = defineEmits<{ logout: [] }>()

const router = useRouter()
const auth = useAuthStore()
const displayName = computed(() => auth.displayName || '未命名用户')
const avatarText = computed(() => Array.from(displayName.value)[0]?.toUpperCase() ?? '?')

const roleLabels: Record<string, string> = { admin: '管理员', user: '普通用户' }
const accountMeta = computed(() => {
  const username = auth.user?.username
  const role = roleLabels[auth.user?.role ?? '']
  // 不重复显示与主标题相同的用户名或角色。
  return [...new Set([username, role].filter((value) => value && value !== displayName.value))].join(' · ')
})

function renderIcon(name: IconName) {
  return () => h(NIcon, { size: 16 }, { default: () => h(AppIcon, { name }) })
}

const userMenuOptions: DropdownOption[] = [
  {
    key: 'info',
    type: 'render',
    render: () => h('div', { class: 'arkade-user-menu-info' }, [
      h('div', { class: 'arkade-user-menu-info__name' }, displayName.value),
      accountMeta.value ? h('div', { class: 'arkade-user-menu-info__meta' }, accountMeta.value) : null
    ])
  },
  { key: 'divider', type: 'divider' },
  { key: 'account', label: '个人设置', icon: renderIcon('user') },
  { key: 'logout', label: '退出登录', icon: renderIcon('logout') }
]

function handleSelect(key: string): void {
  if (key === 'account') void router.push('/account')
  if (key === 'logout' && !props.loggingOut) emit('logout')
}
</script>

<template>
  <n-dropdown
    trigger="click"
    placement="bottom-end"
    :options="userMenuOptions"
    :disabled="loggingOut"
    @select="handleSelect"
  >
    <n-button quaternary class="user-menu-trigger" :disabled="loggingOut" aria-label="账户菜单">
      <span class="user-menu-trigger__content">
        <n-avatar round :size="24" aria-hidden="true">{{ avatarText }}</n-avatar>
        <span v-if="!compact" class="user-menu-trigger__name">{{ displayName }}</span>
      </span>
    </n-button>
  </n-dropdown>
</template>

<style scoped>
.user-menu-trigger {
  flex-shrink: 0;
}

.user-menu-trigger__content {
  display: inline-flex;
  align-items: center;
  gap: 8px;
}

.user-menu-trigger__name {
  max-width: 120px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
</style>

<!-- Naive UI 将下拉菜单传送到顶栏之外，账户信息区使用独立的全局类名。 -->
<style>
.arkade-user-menu-info {
  box-sizing: border-box;
  width: min(208px, calc(100vw - 24px));
  padding: 10px 14px 8px;
  line-height: 1.5;
  overflow-wrap: anywhere;
}

.arkade-user-menu-info__name {
  font-weight: 600;
  color: var(--arkade-text-primary);
}

.arkade-user-menu-info__meta {
  margin-top: 3px;
  font-size: 12px;
  color: var(--arkade-text-secondary);
}
</style>
