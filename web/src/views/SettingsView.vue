<script setup lang="ts">
import { NCard, NSpace, NTabPane, NTabs } from 'naive-ui';
import SystemSettingsForm from '@/components/admin/SystemSettingsForm.vue';
import ProviderSettings from '@/components/admin/ProviderSettings.vue';
</script>
<template>
  <n-space vertical size="large">
    <h2>系统设置</h2>
    <n-card>
      <n-tabs type="line" animated>
        <n-tab-pane name="basic" tab="基本、安全与审计">
          <system-settings-form />
        </n-tab-pane>
        <n-tab-pane name="oidc" tab="第三方身份提供商">
          <provider-settings />
        </n-tab-pane>
      </n-tabs>
    </n-card>
  </n-space>
</template>
