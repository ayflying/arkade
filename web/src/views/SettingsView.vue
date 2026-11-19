<script setup lang="ts">
import { ref } from 'vue';
import { NCard, NSpace, NTabPane, NTabs } from 'naive-ui';
import MonitorSettings from '@/components/admin/monitor/MonitorSettings.vue';
import SystemSettingsForm from '@/components/admin/SystemSettingsForm.vue';
import ProviderSettings from '@/components/admin/ProviderSettings.vue';
import McpKeySettings from '@/components/admin/McpKeySettings.vue';
import { useAuthStore } from '@/stores/auth';

const activeTab = ref('basic');
const auth = useAuthStore();
</script>
<template>
  <n-space vertical size="large">
    <h2>系统设置</h2>
    <n-card>
      <n-tabs v-model:value="activeTab" type="line" animated>
        <n-tab-pane name="basic" tab="基本、安全与审计">
          <system-settings-form />
        </n-tab-pane>
        <n-tab-pane name="oidc" tab="第三方身份提供商">
          <provider-settings />
        </n-tab-pane>
        <n-tab-pane name="monitor" tab="监控与通知" display-directive="if">
          <monitor-settings v-if="activeTab === 'monitor'" />
        </n-tab-pane>
        <n-tab-pane v-if="auth.isAdmin" name="mcp" tab="MCP 接入" display-directive="if">
          <mcp-key-settings v-if="activeTab === 'mcp'" :key="auth.user?.id" />
        </n-tab-pane>
      </n-tabs>
    </n-card>
  </n-space>
</template>
