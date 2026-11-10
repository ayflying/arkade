<script setup lang="ts">
// Compatibility for imports outside the router; the route itself redirects canonically.
import { useRoute, useRouter } from 'vue-router'
const route = useRoute()
void useRouter().replace({ name: 'agent-stack-detail', params: { id: route.params.id, name: route.params.name }, query: route.query, hash: route.hash })
</script>
<template><span /></template>
