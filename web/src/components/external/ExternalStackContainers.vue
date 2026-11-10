<script setup lang="ts">
import StackContainersTable from '@/components/stack/StackContainersTable.vue'
import type { Container } from '@/types/container'
defineProps<{ containers: Container[]; agentId: string; directAddress: string }>()
</script>
<template>
  <stack-containers-table :containers="containers" :agent-id="agentId" :direct-address="directAddress" />
</template>
