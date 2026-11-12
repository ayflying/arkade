<script setup lang="ts">
import { onMounted, ref, shallowRef } from 'vue';
import { NAlert, NButton, NInput, NSelect, NSpace } from 'naive-ui';
import { listIdentities, bindIdentity, unbindIdentity, type Identity } from '@/api/identities';
import { useAuthOptions } from '@/composables/useAuthOptions';
import { confirmAction } from '@/utils/confirm';
const props = defineProps<{
    localPassword: boolean;
}>();
const { options } = useAuthOptions();
const items = shallowRef<Identity[]>([]), error = ref(''), password = ref(''), provider = ref<string | null>(null), busy = ref(false);
async function load() {
    try {
        items.value = await listIdentities();
    }
    catch (cause) {
        error.value = cause instanceof Error ? cause.message : '读取绑定失败';
    }
}
async function bind() {
    if (busy.value || !provider.value || !password.value || !props.localPassword)
        return;
    busy.value = true;
    error.value = '';
    try {
        const result = await bindIdentity(provider.value, password.value);
        password.value = '';
        const url = new URL(result.authorization_url);
        if (url.protocol !== 'https:')
            throw new Error('身份提供商返回了非 HTTPS 授权地址');
        window.location.assign(url.href);
    }
    catch (cause) {
        error.value = cause instanceof Error ? cause.message : '绑定失败';
    }
    finally {
        busy.value = false;
        password.value = '';
    }
}
async function remove(item: Identity) {
    if (busy.value || !await confirmAction({ title: '解除第三方绑定？', content: '解除后将无法使用该身份登录。不能移除最后一种可用登录方式。', danger: true }))
        return;
    busy.value = true;
    error.value = '';
    try {
        await unbindIdentity(item.id);
        await load();
    }
    catch (cause) {
        error.value = cause instanceof Error ? cause.message : '解除绑定失败';
    }
    finally {
        busy.value = false;
    }
}
onMounted(load);
</script>
<template>
  <n-alert v-if="error" type="error">{{ error }}</n-alert>
  <p v-for="item in items" :key="item.id">
    <n-space justify="space-between">
      <span>{{ options?.providers.find(p => p.id === item.provider_id)?.display_name || item.provider_id }}</span>
      <n-button :disabled="busy" @click="remove(item)">解除绑定</n-button>
    </n-space>
  </p>
  <p v-if="!items.length">尚未绑定第三方身份。</p>
  <template v-if="localPassword">
    <n-select v-model:value="provider" :disabled="busy" placeholder="选择身份提供商" :options="(options?.providers || []).map(p => ({label:p.display_name,value:p.id}))" />
    <p>
      <n-input v-model:value="password" type="password" :disabled="busy" placeholder="输入当前本地密码确认身份" :input-props="{autocomplete:'current-password'}" />
    </p>
    <n-button :disabled="busy || !provider || !password" :loading="busy" @click="bind">验证密码并绑定</n-button>
  </template>
  <n-alert v-else type="info">纯第三方账户暂不支持追加绑定；请保留至少一种登录方式。</n-alert>
</template>
