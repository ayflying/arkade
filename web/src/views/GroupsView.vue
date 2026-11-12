<script setup lang="ts">
import { onMounted, ref, shallowRef } from 'vue';
import { NAlert, NButton, NCard, NForm, NFormItem, NInput, NModal, NSpace, NTable } from 'naive-ui';
import { listGroups, createGroup, updateGroup, deleteGroup } from '@/api/management';
import type { Group } from '@/types/management';
import { confirmAction } from '@/utils/confirm';
import GroupAccessEditor from '@/components/admin/GroupAccessEditor.vue';
const groups = shallowRef<Group[]>([]), selected = shallowRef<Group | null>(null), access = shallowRef<Group | null>(null);
const show = ref(false), name = ref(''), description = ref(''), error = ref(''), busy = ref(false);
async function load() {
    try {
        groups.value = await listGroups();
    }
    catch (cause) {
        error.value = cause instanceof Error ? cause.message : '读取用户组失败';
    }
}
function edit(group: Group | null) { selected.value = group; name.value = group?.name || ''; description.value = group?.description || ''; error.value = ''; show.value = true; }
async function save() {
    if (busy.value)
        return;
    busy.value = true;
    error.value = '';
    try {
        const body = { name: name.value.trim(), description: description.value };
        if (selected.value)
            await updateGroup(selected.value.id, body);
        else
            await createGroup(body);
        show.value = false;
        await load();
    }
    catch (cause) {
        error.value = cause instanceof Error ? cause.message : '保存用户组失败';
    }
    finally {
        busy.value = false;
    }
}
async function remove(group: Group) {
    if (busy.value || !await confirmAction({ title: '删除用户组？', content: `删除「${group.name}」将撤销该组的全部服务器授权，但不会删除成员账户。`, danger: true }))
        return;
    busy.value = true;
    error.value = '';
    try {
        await deleteGroup(group.id);
        await load();
    }
    catch (cause) {
        error.value = cause instanceof Error ? cause.message : '删除用户组失败';
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
      <h2>用户组与授权</h2>
      <n-space>
        <n-button @click="load">刷新</n-button>
        <n-button type="primary" @click="edit(null)">创建用户组</n-button>
      </n-space>
    </n-space>
    <n-alert v-if="error" type="error">{{ error }}</n-alert>
    <n-card>
      <n-table>
        <thead>
          <tr>
            <th>用户组</th>
            <th>说明</th>
            <th>成员数</th>
            <th>授权数</th>
            <th>操作</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="group in groups" :key="group.id">
            <td>{{ group.name }}</td>
            <td>{{ group.description }}</td>
            <td>{{ group.member_count }}</td>
            <td>{{ group.grant_count }}</td>
            <td>
              <n-space>
                <n-button size="small" :disabled="busy" @click="edit(group)">编辑</n-button>
                <n-button size="small" :disabled="busy" @click="access = group">成员与授权</n-button>
                <n-button size="small" type="error" :disabled="busy" @click="remove(group)">删除</n-button>
              </n-space>
            </td>
          </tr>
        </tbody>
      </n-table>
      <p v-if="!groups.length">暂无用户组。普通用户未获授权时无法访问服务器。</p>
    </n-card>
    <group-access-editor :group-id="access?.id || null" :name="access?.name || ''" @close="access = null" @saved="load" />
    <n-modal v-model:show="show" preset="card" :title="selected ? '编辑用户组' : '创建用户组'" style="width:480px;max-width:95vw" :mask-closable="!busy">
      <n-alert v-if="error" type="error">{{ error }}</n-alert>
      <n-form label-placement="top" @submit.prevent="save">
        <n-form-item label="名称">
          <n-input v-model:value="name" :disabled="busy" />
        </n-form-item>
        <n-form-item label="说明">
          <n-input v-model:value="description" type="textarea" :disabled="busy" />
        </n-form-item>
        <n-button type="primary" attr-type="submit" :loading="busy" :disabled="busy || !name.trim()">保存</n-button>
      </n-form>
    </n-modal>
  </n-space>
</template>
