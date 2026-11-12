<script setup lang="ts">
import { ref, shallowRef, watch } from 'vue';
import { NAlert, NButton, NModal, NTable } from 'naive-ui';
import { userPermissions, type EffectivePermissions } from '@/api/management';
const props = defineProps<{
    userId: string | null;
}>();
const emit = defineEmits<{
    close: [
    ];
}>();
const data = shallowRef<EffectivePermissions | null>(null), error = ref(''), loading = ref(false);
const labels: Record<string, string> = { none: '无权限', view: '查看', operate: '操作', manage: '管理' };
let generation = 0;
async function load() {
    const id = props.userId, current = ++generation;
    data.value = null;
    error.value = '';
    if (!id)
        return;
    loading.value = true;
    try {
        const result = await userPermissions(id);
        if (current === generation)
            data.value = result;
    }
    catch (cause) {
        if (current === generation)
            error.value = cause instanceof Error ? cause.message : '读取权限失败';
    }
    finally {
        if (current === generation)
            loading.value = false;
    }
}
watch(() => props.userId, load, { immediate: true });
</script>
<template>
  <n-modal :show="!!userId" preset="card" title="实际生效权限预览" style="width:760px;max-width:95vw" @update:show="emit('close')">
    <n-alert v-if="error" type="error">{{ error }}</n-alert>
    <n-alert type="info">以服务器实时计算结果为准。多个用户组取最高权限；管理员具有全局管理权限；禁用账户不具有生效权限。</n-alert>
    <p>
      <n-button :loading="loading" @click="load">重新计算</n-button>
    </p>
    <n-alert v-if="data?.disabled" type="warning">账户已禁用；下表仍保留授权来源说明。</n-alert>
    <n-table v-if="data" :single-line="false">
      <thead>
        <tr>
          <th>服务器</th>
          <th>生效权限</th>
          <th>用户组授权来源</th>
        </tr>
      </thead>
      <tbody>
        <tr v-for="agent in data.agents" :key="agent.agent_id">
          <td>{{ agent.agent_name || agent.agent_id }}</td>
          <td>{{ labels[agent.permission] || '未知' }}</td>
          <td>{{ agent.sources.map(s => `${s.group_name}：${labels[s.permission] || '未知'}`).join('；') || '无用户组授权' }}</td>
        </tr>
      </tbody>
    </n-table>
  </n-modal>
</template>
