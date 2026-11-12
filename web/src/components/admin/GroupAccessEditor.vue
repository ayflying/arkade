<script setup lang="ts">
import { ref, shallowRef, watch } from 'vue';
import { NAlert, NButton, NModal, NSelect, NSpace, NTable } from 'naive-ui';
import { listUsers, groupMembers, setGroupMembers, groupGrants, setGroupGrants } from '@/api/management';
import { listAgents } from '@/api/agents';
import { PERMISSION_OPTIONS, type ManagedUser, type Permission, type GroupGrant } from '@/types/management';
import type { Agent } from '@/types/agent';
const props = defineProps<{
    groupId: string | null;
    name: string;
}>();
const emit = defineEmits<{
    close: [
    ];
    saved: [
    ];
}>();
const users = shallowRef<ManagedUser[]>([]), agents = shallowRef<Agent[]>([]);
const selected = ref<string[]>([]), grants = ref<Record<string, Permission>>({}), error = ref(''), notice = ref(''), busy = ref(false), loaded = ref(false);
let generation = 0;
async function load() {
    const id = props.groupId, current = ++generation;
    loaded.value = false;
    error.value = '';
    notice.value = '';
    if (!id)
        return;
    busy.value = true;
    try {
        const [u, a, members, permissions] = await Promise.all([listUsers(), listAgents(), groupMembers(id), groupGrants(id)]);
        if (current !== generation)
            return;
        users.value = u;
        agents.value = a;
        selected.value = members.map(member => member.id);
        grants.value = Object.fromEntries(permissions.map(grant => [grant.agent_id, grant.permission]));
        // Preserve grants to deleted/unlisted agents, rather than silently erasing them.
        for (const grant of permissions)
            if (!a.some(agent => agent.id === grant.agent_id))
                agents.value = [...agents.value, { id: grant.agent_id, name: `未列出的服务器（${grant.agent_id}）` } as Agent];
        loaded.value = true;
    }
    catch (cause) {
        if (current === generation)
            error.value = cause instanceof Error ? cause.message : '读取组授权失败';
    }
    finally {
        if (current === generation)
            busy.value = false;
    }
}
async function saveMembers() {
    if (busy.value || !props.groupId || !loaded.value)
        return;
    busy.value = true;
    error.value = '';
    notice.value = '';
    try {
        await setGroupMembers(props.groupId, selected.value);
        notice.value = '组成员已保存';
        emit('saved');
    }
    catch (cause) {
        error.value = cause instanceof Error ? cause.message : '保存组成员失败';
    }
    finally {
        busy.value = false;
    }
}
async function saveGrants() {
    if (busy.value || !props.groupId || !loaded.value)
        return;
    busy.value = true;
    error.value = '';
    notice.value = '';
    try {
        const payload = Object.entries(grants.value).filter(([, permission]) => permission !== 'none').map(([agent_id, permission]) => ({ agent_id, permission })) as GroupGrant[];
        await setGroupGrants(props.groupId, payload);
        notice.value = '服务器授权已保存';
        emit('saved');
    }
    catch (cause) {
        error.value = cause instanceof Error ? cause.message : '保存授权失败';
    }
    finally {
        busy.value = false;
    }
}
watch(() => props.groupId, load, { immediate: true });
</script>
<template>
  <n-modal :show="!!groupId" preset="card" :title="`${name} · 成员与服务器授权`" style="width:780px;max-width:95vw" :mask-closable="!busy" @update:show="v => { if (!v && !busy) emit('close') }">
    <n-alert v-if="error" type="error">{{ error }} <n-button text :disabled="busy" @click="load">重新读取</n-button>
  </n-alert>
  <n-alert v-if="notice" type="success">{{ notice }}</n-alert>
  <n-alert type="info">新增服务器默认无权限。查看、操作、管理逐级包含；用户属于多个组时取最高权限。成员和授权分别保存，不会隐式授予权限。</n-alert>
  <template v-if="loaded">
    <h3>组成员</h3>
    <n-select v-model:value="selected" multiple filterable :disabled="busy" placeholder="选择组成员" :options="users.map(u => ({label:`${u.display_name || u.username}（${u.username}）${u.disabled ? ' · 已禁用' : ''}`,value:u.id}))" />
    <p>
      <n-button :loading="busy" :disabled="busy" @click="saveMembers">保存组成员</n-button>
    </p>
    <h3>服务器权限矩阵</h3>
    <n-table>
      <thead>
        <tr>
          <th>服务器</th>
          <th>授权</th>
        </tr>
      </thead>
      <tbody>
        <tr v-for="agent in agents" :key="agent.id">
          <td>{{ agent.display_name || agent.name }}</td>
          <td>
            <n-select :value="grants[agent.id] || 'none'" :options="PERMISSION_OPTIONS" :disabled="busy" @update:value="value => grants[agent.id] = value" />
          </td>
        </tr>
      </tbody>
    </n-table>
    <p>
      <n-space>
        <n-button type="primary" :loading="busy" :disabled="busy" @click="saveGrants">保存服务器授权</n-button>
        <n-button :disabled="busy" @click="emit('close')">关闭</n-button>
      </n-space>
    </p>
  </template>
  </n-modal>
</template>
