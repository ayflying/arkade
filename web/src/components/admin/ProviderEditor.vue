<script setup lang="ts">
import { ref, watch } from 'vue';
import { NAlert, NButton, NForm, NFormItem, NInput, NModal, NSelect, NSwitch } from 'naive-ui';
import { createProvider, updateProvider, providerPayload, type OIDCProvider } from '@/api/settings';
import { listGroups } from '@/api/management';
const props = defineProps<{
    show: boolean;
    provider: OIDCProvider | null;
}>();
const emit = defineEmits<{
    'update:show': [
        boolean
    ];
    saved: [
    ];
}>();
const displayName = ref(''), issuer = ref(''), clientId = ref(''), secret = ref(''), scopes = ref('openid profile email'), group = ref('');
const enabled = ref(false), autoRegister = ref(false), busy = ref(false), error = ref('');
const groups = ref<{
    label: string;
    value: string;
}[]>([]);
let version = 0;
watch(() => props.show, async (show) => {
    if (!show) {
        secret.value = '';
        return;
    }
    const p = props.provider;
    displayName.value = p?.display_name || '';
    issuer.value = p?.issuer || '';
    clientId.value = p?.client_id || '';
    secret.value = '';
    scopes.value = p?.scopes || 'openid profile email';
    group.value = p?.default_group_id || '';
    enabled.value = p?.enabled ?? false;
    autoRegister.value = p?.auto_register ?? false;
    version = p?.version || 0;
    error.value = '';
    try {
        groups.value = [{ label: '不加入用户组（无服务器权限）', value: '' }, ...(await listGroups()).map(g => ({ label: g.name, value: g.id }))];
    }
    catch (cause) {
        error.value = cause instanceof Error ? cause.message : '读取用户组失败';
    }
});
async function save() {
    if (busy.value)
        return;
    error.value = '';
    try {
        if (new URL(issuer.value).protocol !== 'https:')
            throw new Error();
    }
    catch {
        error.value = '签发者地址必须为有效的 HTTPS URL';
        return;
    }
    busy.value = true;
    try {
        const payload = providerPayload({ display_name: displayName.value, issuer: issuer.value, client_id: clientId.value, client_secret: secret.value, scopes: scopes.value, default_group_id: group.value, enabled: enabled.value, auto_register: autoRegister.value, ...(props.provider ? { version } : {}) });
        if (props.provider)
            await updateProvider(props.provider.id, payload);
        else
            await createProvider(payload);
        secret.value = '';
        emit('saved');
        emit('update:show', false);
    }
    catch (cause) {
        error.value = cause instanceof Error ? cause.message : '保存身份提供商失败';
    }
    finally {
        busy.value = false;
    }
}
</script>
<template>
  <n-modal :show="show" preset="card" :title="provider ? '编辑身份提供商' : '新增身份提供商'" style="width:580px;max-width:95vw" :mask-closable="!busy" @update:show="v => { if (!busy) emit('update:show', v) }">
    <n-alert v-if="error" type="error">{{ error }}。版本冲突时请关闭后重新读取再编辑。</n-alert>
    <n-form label-placement="top" @submit.prevent="save">
      <n-form-item label="显示名称">
        <n-input v-model:value="displayName" :disabled="busy" />
      </n-form-item>
      <n-alert v-if="provider" type="info">有账号绑定后不能修改签发者和客户端标识，迁移请新增提供商；仍可修改显示名称、密钥及其他设置。</n-alert>
      <n-form-item label="签发者地址（HTTPS）">
        <n-input v-model:value="issuer" :disabled="busy" />
      </n-form-item>
      <n-form-item label="客户端标识">
        <n-input v-model:value="clientId" :disabled="busy" />
      </n-form-item>
      <n-form-item :label="provider ? `客户端密钥（${provider.has_client_secret ? '已配置' : '未配置'}，留空保持不变）` : '客户端密钥'">
        <n-input v-model:value="secret" type="password" :disabled="busy" :input-props="{autocomplete:'new-password'}" />
      </n-form-item>
      <n-form-item label="授权范围（空格分隔）">
        <n-input v-model:value="scopes" :disabled="busy" />
      </n-form-item>
      <n-form-item label="允许首次登录自动注册">
        <n-switch v-model:value="autoRegister" :disabled="busy" />
      </n-form-item>
      <n-form-item label="新账户加入用户组">
        <n-select v-model:value="group" :options="groups" :disabled="busy" />
      </n-form-item>
      <n-form-item label="启用第三方登录">
        <n-switch v-model:value="enabled" :disabled="busy" />
      </n-form-item>
      <n-alert v-if="provider" type="info">固定回调地址（由服务配置生成）：{{ provider.callback_url || '服务未配置公开地址' }}。请原样配置到身份提供商，不要根据当前浏览器地址推测。</n-alert>
      <n-alert v-else type="info">保存后显示固定回调地址。密钥仅用于提交替换，页面不会读取或回显密钥。</n-alert>
      <p>
        <n-button type="primary" attr-type="submit" :loading="busy" :disabled="busy">保存</n-button>
      </p>
    </n-form>
  </n-modal>
</template>
