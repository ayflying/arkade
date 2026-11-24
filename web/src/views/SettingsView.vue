<script setup lang="ts">
import { ref } from 'vue';
import { NCard, NSpace, NTabPane, NTabs } from 'naive-ui';
import MonitorSettings from '@/components/admin/monitor/MonitorSettings.vue';
import SystemSettingsForm from '@/components/admin/SystemSettingsForm.vue';
import ProviderSettings from '@/components/admin/ProviderSettings.vue';

const activeTab = ref('basic');
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
      </n-tabs>
    </n-card>
  </n-space>
</template>

<!--
  MCP 接入不在这里：MCP 密钥是每个用户自己的凭据，只能按持有者本人的权限生效，
  因此统一放在「个人设置 → MCP 接入」。系统设置里再放一份会让人误以为它是全局开关，
  管理员也会以为配一次就对所有人生效——实际并不会。
-->
