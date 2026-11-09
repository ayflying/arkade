<script setup lang="ts">
/**
 * 顶栏「自动刷新」开关
 *
 * 放在顶栏而不是各页面：这是**全局**偏好，关掉后所有轮询一起停。
 * 如果每页各放一个开关，用户要在三个地方分别关，且容易误以为已经全局关掉了。
 *
 * 手机上顶栏空间紧张：只显示图标 + 短文案或纯图标，
 * 具体说明放进 tooltip，功能不丢。
 */
import { computed } from 'vue'
import { NIcon, NSwitch, NTooltip } from 'naive-ui'
import AppIcon from '@/components/common/AppIcon.vue'
import { useUiSettingsStore } from '@/stores/uiSettings'

interface Props {
  /** 手机档：更紧凑（不显示文字标签） */
  compact?: boolean
}

const props = withDefaults(defineProps<Props>(), { compact: false })

const ui = useUiSettingsStore()

/** 开关状态提示文案 */
const hint = computed<string>(() =>
  ui.autoRefresh
    ? '自动刷新已开启：页面数据会定时更新，切换标签页时自动暂停。'
    : '自动刷新已关闭：页面不会自动更新，需要手动点击刷新。'
)

function handleChange(value: boolean): void {
  ui.setAutoRefresh(value)
}
</script>

<template>
  <n-tooltip trigger="hover" placement="bottom">
    <template #trigger>
      <div class="auto-refresh" :class="{ 'auto-refresh--off': !ui.autoRefresh }">
        <n-icon :size="16" class="auto-refresh__icon">
          <app-icon name="refresh" />
        </n-icon>
        <span v-if="!props.compact" class="auto-refresh__label">自动刷新</span>
        <n-switch
          :value="ui.autoRefresh"
          size="small"
          :aria-label="hint"
          @update:value="handleChange"
        />
      </div>
    </template>
    {{ hint }}
  </n-tooltip>
</template>

<style scoped>
.auto-refresh {
  display: flex;
  align-items: center;
  gap: 6px;
  padding: 4px 8px;
  border-radius: 6px;
  /* 与顶栏其他元素视觉重量一致，不抢注意力 */
  color: var(--arkade-text-secondary, #5c6169);
}

/* 关闭时图标变淡，一眼能看出「现在不会自动更新」 */
.auto-refresh--off .auto-refresh__icon {
  opacity: 0.45;
}

.auto-refresh__icon {
  flex-shrink: 0;
  display: inline-flex;
}

.auto-refresh__label {
  font-size: 13px;
  white-space: nowrap;
}

@media (max-width: 767.98px) {
  .auto-refresh {
    padding: 4px 4px;
    gap: 4px;
  }
}
</style>
