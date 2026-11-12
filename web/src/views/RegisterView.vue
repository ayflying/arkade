<script setup lang="ts">
import { ref } from 'vue';
import { useRouter } from 'vue-router';
import { NAlert, NButton, NCard, NForm, NFormItem, NInput } from 'naive-ui';
import { useAuthOptions } from '@/composables/useAuthOptions';
import { register } from '@/api/account';
import { passwordIssue } from '@/utils/passwordPolicy';
const router = useRouter();
const { options, error, loading, load } = useAuthOptions();
const username = ref(''), displayName = ref(''), password = ref(''), repeated = ref('');
const busy = ref(false), failure = ref('');
async function submit() {
    if (busy.value || options.value?.registration_enabled !== true)
        return;
    failure.value = '';
    if (!username.value.trim() || password.value !== repeated.value || Array.from(password.value).length < (options.value.password_min_length || 8)) {
        failure.value = '请填写用户名，并确认两次密码一致且满足长度要求';
        return;
    }
    const issue = passwordIssue(password.value, options.value.password_min_length);
    if (issue) {
        failure.value = issue;
        return;
    }
    busy.value = true;
    try {
        await register({ username: username.value.trim(), display_name: displayName.value, password: password.value });
        password.value = '';
        repeated.value = '';
        await router.replace('/login?registered=1');
    }
    catch (cause) {
        failure.value = cause instanceof Error ? cause.message : '注册失败';
    }
    finally {
        busy.value = false;
    }
}
</script>
<template>
  <main style="max-width:440px;margin:8vh auto;padding:20px">
    <n-card :title="`${options?.site_name || 'Arkade'} · 注册账户`">
      <n-alert v-if="error" type="error">{{ error }}<n-button text @click="load">重试</n-button>
    </n-alert>
    <n-alert v-if="options && !options.registration_enabled" type="warning">当前未开放注册，请联系管理员。</n-alert>
    <n-alert v-if="failure" type="error">{{ failure }}</n-alert>
    <n-form v-if="options?.registration_enabled" label-placement="top" @submit.prevent="submit">
      <n-form-item label="用户名">
        <n-input v-model:value="username" :disabled="busy" :input-props="{autocomplete:'username'}" />
      </n-form-item>
      <n-form-item label="显示名">
        <n-input v-model:value="displayName" :disabled="busy" />
      </n-form-item>
      <n-form-item :label="`密码（至少 ${options.password_min_length} 位）`">
        <n-input v-model:value="password" type="password" :disabled="busy" :input-props="{autocomplete:'new-password'}" />
      </n-form-item>
      <n-form-item label="确认密码">
        <n-input v-model:value="repeated" type="password" :disabled="busy" :input-props="{autocomplete:'new-password'}" />
      </n-form-item>
      <n-button type="primary" attr-type="submit" :loading="busy" :disabled="busy || loading">注册</n-button>
    </n-form>
    <p>
      <n-button text @click="router.push('/login')">返回登录</n-button>
    </p>
  </n-card>
  </main>
</template>
