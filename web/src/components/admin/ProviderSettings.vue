<script setup lang="ts">
import { onMounted, ref, shallowRef } from 'vue';
import { NAlert, NButton, NCard, NSpace, NTable } from 'naive-ui';
import { listProviders, deleteProvider, testProvider, type OIDCProvider, type DiscoveryResult } from '@/api/settings';
import ProviderEditor from './ProviderEditor.vue';
import { confirmAction } from '@/utils/confirm';
const providers = shallowRef<OIDCProvider[]>([]), selected = shallowRef<OIDCProvider | null>(null), result = shallowRef<DiscoveryResult | null>(null);
const show = ref(false), busy = ref(false), error = ref(''), testedName = ref('');
async function load() {
    error.value = '';
    try {
        providers.value = await listProviders();
    }
    catch (cause) {
        error.value = cause instanceof Error ? cause.message : '读取身份提供商失败';
    }
}
function edit(provider: OIDCProvider | null) { selected.value = provider; show.value = true; }
async function remove(provider: OIDCProvider) {
    if (busy.value || !await confirmAction({ title: '删除身份提供商？', content: `删除「${provider.display_name}」会使对应第三方身份无法登录。依赖该身份的用户可能失去访问方式，请先确认替代登录方式。`, danger: true }))
        return;
    busy.value = true;
    error.value = '';
    try {
        await deleteProvider(provider.id);
        await load();
    }
    catch (cause) {
        error.value = cause instanceof Error ? cause.message : '删除失败';
    }
    finally {
        busy.value = false;
    }
}
async function test(provider: OIDCProvider) {
    if (busy.value)
        return;
    busy.value = true;
    error.value = '';
    result.value = null;
    testedName.value = provider.display_name;
    try {
        result.value = await testProvider(provider.id);
    }
    catch (cause) {
        error.value = cause instanceof Error ? cause.message : '发现端点测试失败';
    }
    finally {
        busy.value = false;
    }
}
onMounted(load);
</script>
<template>
  <n-space vertical>
    <n-space>
      <n-button :disabled="busy" @click="load">刷新列表</n-button>
      <n-button type="primary" :disabled="busy" @click="edit(null)">新增身份提供商</n-button>
    </n-space>
    <n-alert v-if="error" type="error">{{ error }}</n-alert>
    <n-alert type="info">第三方自动注册默认关闭。启用自动注册但未选用户组时，新账户无任何服务器权限。本地登录始终保留。</n-alert>
    <n-table>
      <thead>
        <tr>
          <th>名称</th>
          <th>签发者</th>
          <th>状态</th>
          <th>密钥</th>
          <th>操作</th>
        </tr>
      </thead>
      <tbody>
        <tr v-for="provider in providers" :key="provider.id">
          <td>{{ provider.display_name }}</td>
          <td>{{ provider.issuer }}</td>
          <td>{{ provider.enabled ? '已启用' : '已停用' }}</td>
          <td>{{ provider.has_client_secret ? '已配置' : '未配置' }}</td>
          <td>
            <n-space>
              <n-button size="small" :disabled="busy" @click="edit(provider)">编辑</n-button>
              <n-button size="small" :disabled="busy" @click="test(provider)">测试发现端点</n-button>
              <n-button size="small" type="error" :disabled="busy" @click="remove(provider)">删除</n-button>
            </n-space>
          </td>
        </tr>
      </tbody>
    </n-table>
    <n-card v-if="result" :title="`${testedName} · 发现结果`">
      <n-alert :type="result.reachable ? 'success' : 'warning'">{{ result.reachable ? '发现端点可达（不等于实际登录成功）' : '发现端点不可达' }}</n-alert>
      <p>签发者：{{ result.issuer }}</p>
      <p>授权端点：{{ result.authorization_endpoint }}</p>
      <p>令牌端点：{{ result.token_endpoint }}</p>
      <p>公钥端点：{{ result.jwks_uri }}</p>
    </n-card>
    <provider-editor v-model:show="show" :provider="selected" @saved="load" />
  </n-space>
</template>
