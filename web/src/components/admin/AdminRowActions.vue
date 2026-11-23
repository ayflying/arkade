<script setup lang="ts">
import { computed } from 'vue'
import { NButton, NDropdown, type DropdownOption } from 'naive-ui'

interface RowAction {
  label: string
  run: () => void | Promise<void>
  danger?: boolean
}

const props = defineProps<{ rowLabel: string; disabled?: boolean; actions: RowAction[] }>()

const options = computed<DropdownOption[]>(() => props.actions.flatMap((action, index) => {
  const option: DropdownOption = { label: action.label, key: index }
  // 危险操作和常用操作分开，具体确认仍由业务页面负责。
  return action.danger && index > 0
    ? [{ type: 'divider', key: `divider-${index}` } as DropdownOption, option]
    : [option]
}))

function select(key: string | number) {
  if (!props.disabled && typeof key === 'number') void props.actions[key]?.run()
}
</script>

<template>
  <n-dropdown trigger="click" placement="bottom-end" :options="options" :disabled="disabled" @select="select">
    <n-button size="small" :disabled="disabled" :aria-label="`${rowLabel}：操作`">操作 ▾</n-button>
  </n-dropdown>
</template>
