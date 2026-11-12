<script setup lang="ts">
import { onMounted, ref } from 'vue';
import { NAlert, NButton, NCard, NFormItem, NInput, NSpace } from 'naive-ui';
import { getAccount, updateAccount } from '@/api/account';
import { useAuthStore } from '@/stores/auth';
import PasswordForm from '@/components/account/PasswordForm.vue';
import IdentitySettings from '@/components/account/IdentitySettings.vue';
const auth = useAuthStore();
const displayName = ref(''), error = ref(''), success = ref(''), busy = ref(false), loaded = ref(false);
async function load() {
    error.value = '';
    try {
        auth.user = await getAccount();
        displayName.value = auth.user.display_name;
        loaded.value = true;
    }
    catch (cause) {
        error.value = cause instanceof Error ? cause.message : '读取个人资料失败';
    }
}
async function save() {
    if (busy.value || auth.user?.must_change_password)
        return;
    busy.value = true;
    error.value = '';
    success.value = '';
    try {
        auth.user = await updateAccount(displayName.value);
        success.value = '个人资料已保存';
    }
    catch (cause) {
        error.value = cause instanceof Error ? cause.message : '保存失败';
    }
    finally {
        busy.value = false;
    }
}
onMounted(load);
</script>
<template>
  <n-space vertical size="large">
    <h2>个人设置</h2>
    <n-alert v-if="auth.user?.must_change_password" type="warning">管理员要求您先修改密码。完成前不能访问其他功能；仍可通过右上角退出登录。</n-alert>
    <n-alert v-if="error" type="error">{{ error }}<n-button text @click="load">重新读取</n-button>
  </n-alert>
  <n-alert v-if="success" type="success">{{ success }}</n-alert>
  <n-card v-if="loaded && !auth.user?.must_change_password" title="个人资料">
    <p>用户名：{{ auth.user?.username }}</p>
    <n-form-item label="显示名">
      <n-input v-model:value="displayName" :disabled="busy" />
    </n-form-item>
    <n-button type="primary" :loading="busy" :disabled="busy" @click="save">保存资料</n-button>
  </n-card>
  <n-alert v-if="loaded && auth.user?.local_password === false" type="info">当前账户仅使用第三方身份登录，暂不支持在此设置本地密码。请在身份提供商处管理密码，并保留至少一种可用登录方式。</n-alert>
  <n-card v-if="loaded && auth.user?.local_password === true" title="修改密码">
    <password-form />
  </n-card>
  <n-card v-if="loaded && !auth.user?.must_change_password" title="第三方身份绑定">
    <identity-settings :local-password="auth.user?.local_password === true" />
  </n-card>
  </n-space>
</template>
