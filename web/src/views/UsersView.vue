<script setup lang="ts">
import { onMounted, ref, shallowRef } from 'vue';
import { NAlert, NButton, NCard, NInput, NModal, NSpace, NTag } from 'naive-ui';
import { listUsers, deleteUser, resetUserPassword } from '@/api/management';
import type { ManagedUser } from '@/types/management';
import { confirmAction } from '@/utils/confirm';
import UserEditor from '@/components/admin/UserEditor.vue';
import UserPermissions from '@/components/admin/UserPermissions.vue';
import AdminListTable from '@/components/admin/AdminListTable.vue';
import AdminRowActions from '@/components/admin/AdminRowActions.vue';
const users = shallowRef<ManagedUser[]>([]), selected = shallowRef<ManagedUser | null>(null);
const show = ref(false), busy = ref(false), error = ref(''), success = ref(''), permissionsId = ref<string | null>(null);
const resetUser = shallowRef<ManagedUser | null>(null), newPassword = ref('');
async function load() {
    error.value = '';
    try {
        users.value = await listUsers();
    }
    catch (cause) {
        error.value = cause instanceof Error ? cause.message : '读取用户失败';
    }
}
function edit(user: ManagedUser | null) { selected.value = user; show.value = true; }
async function remove(user: ManagedUser) {
    if (busy.value || !await confirmAction({ title: '删除用户？', content: `删除「${user.username}」及其会话、组成员关系和第三方身份绑定。此操作不可撤销。`, danger: true }))
        return;
    busy.value = true;
    error.value = '';
    try {
        await deleteUser(user.id);
        await load();
    }
    catch (cause) {
        error.value = cause instanceof Error ? cause.message : '删除失败';
    }
    finally {
        busy.value = false;
    }
}
async function reset() {
    if (busy.value || !resetUser.value || !newPassword.value)
        return;
    busy.value = true;
    error.value = '';
    try {
        await resetUserPassword(resetUser.value.id, newPassword.value);
        newPassword.value = '';
        resetUser.value = null;
        success.value = '密码已重置，该用户的会话已撤销，重新登录后必须修改密码';
        await load();
    }
    catch (cause) {
        error.value = cause instanceof Error ? cause.message : '重置失败';
    }
    finally {
        busy.value = false;
    }
}
onMounted(load);
</script>
<template>
  <n-space vertical size="large">
    <n-space justify="space-between">
      <h2>用户管理</h2>
      <n-space>
        <n-button @click="load">刷新</n-button>
        <n-button type="primary" @click="edit(null)">创建用户</n-button>
      </n-space>
    </n-space>
    <n-alert v-if="error" type="error">{{ error }}</n-alert>
    <n-alert v-if="success" type="success">{{ success }}</n-alert>
    <n-card>
      <admin-list-table label="用户列表">
        <template #headers>
          <th scope="col">用户名</th>
          <th scope="col">显示名</th>
          <th scope="col" class="admin-list-compact">角色</th>
          <th scope="col" class="admin-list-compact">状态</th>
          <th scope="col" class="admin-list-compact admin-list-action">操作</th>
        </template>
        <tr v-for="user in users" :key="user.id">
          <td class="admin-list-text">{{ user.username }}</td>
          <td class="admin-list-text">{{ user.display_name }}</td>
          <td class="admin-list-compact">{{ user.role === 'admin' ? '管理员' : '普通用户' }}</td>
          <td class="admin-list-compact">
            <n-tag :type="user.disabled ? 'error' : 'success'">{{ user.disabled ? '已禁用' : '正常' }}</n-tag>
            <span v-if="user.must_change_password"> 待修改密码</span>
          </td>
          <td class="admin-list-compact admin-list-action">
            <admin-row-actions :row-label="user.username" :disabled="busy" :actions="[
              { label: '编辑', run: () => edit(user) },
              { label: '生效权限', run: () => { permissionsId = user.id } },
              { label: '重置密码', run: () => { resetUser = user; newPassword = '' } },
              { label: '删除', run: () => remove(user), danger: true }
            ]" />
          </td>
        </tr>
      </admin-list-table>
      <p v-if="!users.length">暂无用户</p>
    </n-card>
    <user-editor v-model:show="show" :user="selected" @saved="load" />
    <user-permissions :user-id="permissionsId" @close="permissionsId = null" />
    <n-modal :show="!!resetUser" preset="card" title="重置用户密码" style="width:460px;max-width:95vw" :mask-closable="!busy" @update:show="v => { if (!v && !busy) resetUser = null }">
      <n-alert v-if="error" type="error">{{ error }}</n-alert>
      <p>账户：{{ resetUser?.username }}。重置后撤销全部会话，并要求下次登录修改密码。</p>
      <n-input v-model:value="newPassword" type="password" :disabled="busy" placeholder="输入新的临时密码" :input-props="{autocomplete:'new-password'}" />
      <p>
        <n-button type="warning" :loading="busy" :disabled="busy || !newPassword" @click="reset">确认重置</n-button>
      </p>
    </n-modal>
  </n-space>
</template>
