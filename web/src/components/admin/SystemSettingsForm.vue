<script setup lang="ts">
import { onMounted, ref } from 'vue';
import { NAlert, NButton, NForm, NFormItem, NInput, NInputNumber, NSelect, NSwitch } from 'naive-ui';
import { getSettings, saveSettings } from '@/api/settings';
import { listGroups } from '@/api/management';
import type { SystemSettings } from '@/types/settings';
const settings = ref<SystemSettings | null>(null), error = ref(''), notice = ref(''), busy = ref(false);
const groups = ref<{
    label: string;
    value: string;
}[]>([]);
async function load() {
    if (busy.value)
        return;
    busy.value = true;
    error.value = '';
    notice.value = '';
    try {
        const [value, list] = await Promise.all([getSettings(), listGroups()]);
        settings.value = value;
        groups.value = [{ label: '不加入用户组（无服务器权限）', value: '' }, ...list.map(g => ({ label: g.name, value: g.id }))];
    }
    catch (cause) {
        error.value = cause instanceof Error ? cause.message : '读取系统设置失败';
    }
    finally {
        busy.value = false;
    }
}
async function save() {
    if (busy.value || !settings.value)
        return;
    busy.value = true;
    error.value = '';
    notice.value = '';
    try {
        settings.value = await saveSettings({ ...settings.value });
        notice.value = '系统设置已保存';
    }
    catch (cause) {
        error.value = cause instanceof Error ? cause.message : '保存系统设置失败';
    }
    finally {
        busy.value = false;
    }
}
onMounted(load);
</script>
<template>
  <n-alert v-if="error" type="error">{{ error }}。如设置版本冲突，请先保留当前修改，再重新读取；不会自动覆盖他人设置。</n-alert>
  <n-alert v-if="notice" type="success">{{ notice }}</n-alert>
  <p>
    <n-button :disabled="busy" @click="load">重新读取设置</n-button>
  </p>
  <n-form v-if="settings" label-placement="top" @submit.prevent="save">
    <n-form-item label="站点名称">
      <n-input v-model:value="settings.site_name" :disabled="busy" />
    </n-form-item>
    <n-form-item label="登录公告（纯文本）">
      <n-input v-model:value="settings.login_notice" type="textarea" :disabled="busy" />
    </n-form-item>
    <n-form-item label="开放账户注册">
      <n-switch v-model:value="settings.registration_enabled" :disabled="busy" />
    </n-form-item>
    <n-form-item label="注册后加入用户组">
      <n-select v-model:value="settings.registration_group_id" :options="groups" :disabled="busy" />
    </n-form-item>
    <n-form-item label="会话有效期（秒）">
      <n-input-number v-model:value="settings.session_ttl_seconds" :min="1" :disabled="busy" />
    </n-form-item>
    <n-form-item label="密码最短长度（8–72位；UTF-8总长度不超过72字节）">
      <n-input-number v-model:value="settings.password_min_length" :min="8" :max="72" :disabled="busy" />
    </n-form-item>
    <n-form-item label="登录限流窗口内最多尝试次数">
      <n-input-number v-model:value="settings.login_max_attempts" :min="1" :disabled="busy" />
    </n-form-item>
    <n-form-item label="登录限流窗口（秒）">
      <n-input-number v-model:value="settings.login_window_seconds" :min="1" :disabled="busy" />
    </n-form-item>
    <n-form-item label="审计日志保留天数">
      <n-input-number v-model:value="settings.audit_retention_days" :min="1" :disabled="busy" />
    </n-form-item>
    <n-alert type="info">本地登录始终启用，避免身份提供商故障后所有管理员无法登录。开放注册不等于获得服务器访问权限。</n-alert>
    <p>
      <n-button type="primary" attr-type="submit" :loading="busy" :disabled="busy">保存设置</n-button>
    </p>
  </n-form>
</template>
