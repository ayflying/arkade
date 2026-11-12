<script setup lang="ts">
import { ref } from 'vue';
import { useRouter } from 'vue-router';
import { NAlert, NButton, NForm, NFormItem, NInput } from 'naive-ui';
import { changePassword } from '@/api/account';
import { useAuthStore } from '@/stores/auth';
import { passwordIssue } from '@/utils/passwordPolicy';
const auth = useAuthStore(), router = useRouter();
const oldPassword = ref(''), password = ref(''), repeated = ref(''), error = ref(''), busy = ref(false);
async function submit() {
    if (busy.value)
        return;
    error.value = '';
    if (!oldPassword.value || !password.value || password.value !== repeated.value) {
        error.value = '请填写当前密码，并确认新密码输入一致';
        return;
    }
    const issue = passwordIssue(password.value);
    if (issue) {
        error.value = issue;
        return;
    }
    busy.value = true;
    try {
        await changePassword(oldPassword.value, password.value);
        oldPassword.value = '';
        password.value = '';
        repeated.value = '';
        auth.clearLocal();
        await router.replace('/login?password_changed=1');
    }
    catch (cause) {
        error.value = cause instanceof Error ? cause.message : '修改密码失败';
    }
    finally {
        busy.value = false;
    }
}
</script>
<template>
  <n-alert v-if="error" type="error">{{ error }}</n-alert>
  <n-alert type="info">修改成功后，当前及其他设备上的所有登录会话均失效，需要重新登录。</n-alert>
  <n-form label-placement="top" @submit.prevent="submit">
    <n-form-item label="当前密码">
      <n-input v-model:value="oldPassword" type="password" :disabled="busy" :input-props="{autocomplete:'current-password'}" />
    </n-form-item>
    <n-form-item label="新密码">
      <n-input v-model:value="password" type="password" :disabled="busy" :input-props="{autocomplete:'new-password'}" />
    </n-form-item>
    <n-form-item label="确认新密码">
      <n-input v-model:value="repeated" type="password" :disabled="busy" :input-props="{autocomplete:'new-password'}" />
    </n-form-item>
    <n-button type="primary" attr-type="submit" :loading="busy" :disabled="busy">修改密码并重新登录</n-button>
  </n-form>
</template>
