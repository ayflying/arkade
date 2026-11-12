<script setup lang="ts">
import { ref, watch } from 'vue';
import { NAlert, NButton, NForm, NFormItem, NInput, NModal, NSelect, NSwitch } from 'naive-ui';
import { createUser, updateUser } from '@/api/management';
import { ROLE_OPTIONS, type ManagedUser } from '@/types/management';
const props = defineProps<{
    show: boolean;
    user: ManagedUser | null;
}>();
const emit = defineEmits<{
    'update:show': [
        boolean
    ];
    saved: [
    ];
}>();
const username = ref(''), displayName = ref(''), password = ref(''), role = ref<'admin' | 'user'>('user'), disabled = ref(false), busy = ref(false), error = ref('');
watch(() => props.show, show => { if (show) {
    username.value = props.user?.username || '';
    displayName.value = props.user?.display_name || '';
    role.value = props.user?.role === 'admin' ? 'admin' : 'user';
    disabled.value = props.user?.disabled === true;
    password.value = '';
    error.value = '';
} });
async function save() {
    if (busy.value)
        return;
    busy.value = true;
    error.value = '';
    try {
        if (props.user)
            await updateUser(props.user.id, { display_name: displayName.value, role: role.value, disabled: disabled.value });
        else
            await createUser({ username: username.value.trim(), display_name: displayName.value, password: password.value, role: role.value });
        password.value = '';
        emit('saved');
        emit('update:show', false);
    }
    catch (cause) {
        error.value = cause instanceof Error ? cause.message : '保存用户失败';
    }
    finally {
        busy.value = false;
    }
}
</script>
<template>
  <n-modal :show="show" preset="card" :title="user ? '编辑用户' : '创建用户'" style="width:480px;max-width:95vw" :mask-closable="!busy" @update:show="v => { if (!busy) emit('update:show', v) }">
    <n-alert v-if="error" type="error">{{ error }}</n-alert>
    <n-form label-placement="top" @submit.prevent="save">
      <n-form-item label="用户名">
        <n-input v-model:value="username" :disabled="busy || !!user" />
      </n-form-item>
      <n-form-item label="显示名">
        <n-input v-model:value="displayName" :disabled="busy" />
      </n-form-item>
      <n-form-item v-if="!user" label="初始密码">
        <n-input v-model:value="password" type="password" :disabled="busy" :input-props="{autocomplete:'new-password'}" />
      </n-form-item>
      <n-form-item label="角色">
        <n-select v-model:value="role" :options="ROLE_OPTIONS" :disabled="busy" />
      </n-form-item>
      <n-form-item v-if="user" label="禁用账户">
        <n-switch v-model:value="disabled" :disabled="busy" />
      </n-form-item>
      <n-alert type="info">普通用户默认无服务器权限，通过用户组授权。最后一个可用本地管理员不能被禁用或降权。</n-alert>
      <p>
        <n-button type="primary" attr-type="submit" :loading="busy" :disabled="busy">保存</n-button>
      </p>
    </n-form>
  </n-modal>
</template>
