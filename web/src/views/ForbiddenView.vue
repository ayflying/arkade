<script setup lang="ts">
import { NAlert, NButton } from 'naive-ui';
import { useRouter } from 'vue-router';
const router = useRouter();
</script>
<template>
  <n-alert type="error" title="无权访问">该页面仅管理员可访问。服务器授权不等于系统管理员权限。<p>
    <n-button @click="router.push('/overview')">返回概览</n-button>
  </p>
  </n-alert>
</template>
