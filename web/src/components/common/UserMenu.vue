<script setup lang="ts">
/**
 * 顶栏用户区（用户名下拉 + 角色徽标 + 退出登录）
 *
 * 三档布局差异（功能一个不少，只调整呈现密度）：
 * - 桌面/平板：用户名下拉 + 角色徽标 + 独立的「退出登录」按钮；
 * - 手机      ：顶栏横向空间不足，把「退出登录」收进用户名下拉菜单里，
 *               头像/名字按钮始终可见，功能不丢失。
 *
 * 角色徽标的展示条件（沿用上一版结论）：
 * 仅当「角色有中文名」且「角色中文名 ≠ 显示名」时展示，
 * 避免 display_name 本身就叫「管理员」时出现「管理员 管理员」的重复。
 */
import { computed, h } from 'vue'
import { NAvatar, NButton, NDropdown, NIcon, NTag } from 'naive-ui'
import type { DropdownOption } from 'naive-ui'
import AppIcon from '@/components/common/AppIcon.vue'
import { useAuthStore } from '@/stores/auth'

interface Props {
  /** 手机档：把退出登录收进下拉菜单，顶栏只留一个紧凑入口 */
  compact?: boolean
  /** 退出登录进行中（禁用交互，防止连点） */
  loggingOut?: boolean
}

const props = withDefaults(defineProps<Props>(), {
  compact: false,
  loggingOut: false
})

const emit = defineEmits<{
  (e: 'logout'): void
}>()

const auth = useAuthStore()

/** 角色中文名；后端可能出现契约外的角色值，未知时不展示徽标而不是显示乱码 */
const ROLE_LABELS: Record<string, string> = {
  admin: '管理员',
  operator: '操作员',
  viewer: '只读'
}

const roleLabel = computed<string>(() => ROLE_LABELS[auth.user?.role ?? ''] ?? '')

const showRoleTag = computed<boolean>(
  () => roleLabel.value !== '' && roleLabel.value !== auth.displayName
)

/** 显示名首字，用作头像文字（中文取第一个字，英文取首字母大写） */
const avatarText = computed<string>(() => {
  const name = auth.displayName
  return name === '' ? '?' : name.slice(0, 1).toUpperCase()
})

/**
 * 下拉菜单项：
 * - 第一组展示只读的用户信息（用户名/角色），让用户在手机端也能看到自己的身份；
 * - 手机端追加「退出登录」，桌面端退出登录由独立按钮承担。
 */
const userMenuOptions = computed<DropdownOption[]>(() => {
  const infoGroup: DropdownOption[] = [
    {
      key: 'info',
      type: 'render',
      render: () =>
        h(
          'div',
          { class: 'user-menu-info' },
          [
            h('div', { class: 'user-menu-info__name' }, auth.displayName || '未命名用户'),
            h(
              'div',
              { class: 'user-menu-info__meta' },
              [auth.user?.username ?? '', roleLabel.value].filter((v) => v !== '').join(' · ')
            )
          ]
        )
    }
  ]
  const actionGroup: DropdownOption[] = props.compact
    ? [{ key: 'logout', label: '退出登录', icon: renderLogoutIcon }]
    : []
  return [
    ...infoGroup,
    { key: 'divider', type: 'divider' },
    ...actionGroup
  ]
})

/** 下拉项图标：用统一的 AppIcon，避免引入图标库 */
function renderLogoutIcon(): ReturnType<typeof h> {
  return h(NIcon, { size: 16 }, { default: () => h(AppIcon, { name: 'logout' }) })
}

function handleSelect(key: string): void {
  if (key === 'logout') {
    emit('logout')
  }
}
</script>

<template>
  <div class="user-area">
    <!-- 桌面/平板：角色徽标常驻 -->
    <n-tag v-if="showRoleTag && !compact" size="small" :bordered="false" type="info">
      {{ roleLabel }}
    </n-tag>

    <n-dropdown
      trigger="click"
      :options="userMenuOptions"
      :disabled="loggingOut"
      @select="handleSelect"
    >
      <n-button quaternary class="user-area__trigger">
        <span class="user-area__avatar">
          <n-avatar round :size="22">{{ avatarText }}</n-avatar>
        </span>
        <!-- 手机档隐藏文字，只留头像，避免顶栏过挤；名字在下拉里可见 -->
        <span v-if="!compact" class="user-area__name">{{ auth.displayName || '未命名用户' }}</span>
      </n-button>
    </n-dropdown>

    <!-- 桌面/平板：独立退出按钮；手机档已收进下拉 -->
    <n-button
      v-if="!compact"
      size="small"
      :loading="loggingOut"
      :disabled="loggingOut"
      @click="emit('logout')"
    >
      退出登录
    </n-button>
  </div>
</template>

<style scoped>
.user-area {
  display: flex;
  align-items: center;
  gap: 8px;
  flex-shrink: 0;
}

.user-area__trigger {
  display: inline-flex;
  align-items: center;
  gap: 6px;
}

.user-area__avatar {
  display: inline-flex;
}

.user-area__name {
  max-width: 120px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

/* 下拉里的用户信息块（通过 render 函数生成，需用 :deep 穿透 scoped） */
:deep(.user-menu-info) {
  padding: 8px 16px 4px;
  min-width: 160px;
}

:deep(.user-menu-info__name) {
  font-weight: 600;
  color: #1f2225;
}

:deep(.user-menu-info__meta) {
  margin-top: 2px;
  font-size: 12px;
  color: #8b9099;
}
</style>
