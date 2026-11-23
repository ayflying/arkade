<script setup lang="ts">
import { NTable } from 'naive-ui'

defineProps<{ label: string }>()
</script>

<template>
  <div class="admin-list-scroll" tabindex="0" role="region" :aria-label="label">
    <n-table :single-line="false">
      <thead><tr><slot name="headers" /></tr></thead>
      <tbody><slot /></tbody>
    </n-table>
  </div>
</template>

<style scoped>
.admin-list-scroll {
  overflow-x: auto;
}

.admin-list-scroll :deep(.admin-list-compact) {
  width: 1%;
  white-space: nowrap;
}

.admin-list-scroll :deep(.admin-list-action) {
  text-align: center;
}

.admin-list-scroll :deep(td.admin-list-text) {
  min-width: 6rem;
  overflow-wrap: anywhere;
}

/* 长说明可换行，并优先使用从操作列释放的空间。 */
.admin-list-scroll :deep(td.admin-list-description) {
  width: 60%;
}
</style>
