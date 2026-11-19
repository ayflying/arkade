<script setup lang="ts">
import { onBeforeUnmount, onMounted, ref, shallowRef } from 'vue'
import { NAlert, NButton, NInput, NModal, NSpace, NTable } from 'naive-ui'
import { createMcpKey, listMcpKeys, revokeMcpKey, type McpKey } from '@/api/mcpKeys'
import { ApiError } from '@/types/api'
import { confirmAction } from '@/utils/confirm'
import { toastError, toastSuccess } from '@/utils/feedback'
import { formatAbsoluteTime } from '@/utils/formatTime'
import { mcpClientConfig, mcpEndpoint, mcpKeyName } from '@/utils/mcpClientConfig'

const endpoint = mcpEndpoint(window.location.origin)
const example = mcpClientConfig(endpoint)
const keys = shallowRef<McpKey[]>([])
const name = ref('')
const error = ref('')
const loaded = ref(false)
const busy = ref<'list' | 'create' | 'revoke' | null>(null)
const token = ref('')
let disposed = false

function dismissToken() {
  token.value = ''
}

onBeforeUnmount(() => {
  disposed = true
  dismissToken()
})

function errorText(cause: unknown, fallback: string): string {
  // Never render raw response/error text from credential-management calls.
  if (cause instanceof ApiError) {
    if (cause.status === 401) return '登录已过期，请重新登录。'
    if (cause.status === 403) return '当前账户无权执行此操作。'
    if (cause.status === 0) return `无法连接服务器。${fallback}`
  }
  return fallback
}

async function load() {
  if (busy.value) return
  busy.value = 'list'
  error.value = ''
  try {
    const items = await listMcpKeys()
    if (disposed) return
    keys.value = items
    loaded.value = true
  } catch (cause) {
    error.value = errorText(cause, '无法读取密钥列表，请刷新重试。')
  } finally {
    busy.value = null
  }
}

async function create() {
  if (busy.value || token.value) return
  const validName = mcpKeyName(name.value)
  if (validName === null) {
    error.value = '请输入 1–80 个字符的密钥名称，不能包含控制字符。'
    return
  }
  busy.value = 'create'
  error.value = ''
  try {
    const created = await createMcpKey(validName)
    if (disposed) return
    keys.value = [created.key, ...keys.value]
    name.value = ''
    token.value = created.token
  } catch (cause) {
    error.value = errorText(cause, '未能确认密钥已创建。请先刷新列表，再决定是否重试。')
  } finally {
    busy.value = null
  }
}

async function revoke(key: McpKey) {
  if (busy.value) return
  busy.value = 'revoke'
  try {
    if (!await confirmAction({
      title: '撤销 MCP 密钥？',
      content: `撤销「${key.name}」后，使用该密钥的客户端将无法再连接。此操作无法撤回。`,
      positiveText: '撤销密钥',
      danger: true
    }) || disposed) return
    error.value = ''
    await revokeMcpKey(key.id)
    keys.value = keys.value.filter((item) => item.id !== key.id)
    if (!disposed) toastSuccess('MCP 密钥已撤销')
  } catch (cause) {
    if (cause instanceof ApiError && cause.status === 404) {
      keys.value = keys.value.filter((item) => item.id !== key.id)
      error.value = '此密钥已不可用，列表已更新。'
    } else {
      error.value = errorText(cause, '无法撤销密钥，请刷新列表重试。')
    }
  } finally {
    busy.value = null
  }
}

async function copy(text: string, label: string) {
  if (!text) return
  try {
    await navigator.clipboard.writeText(text)
    toastSuccess(`${label}已复制`)
  } catch {
    toastError('无法访问剪贴板，请手动选中文本复制。')
  }
}

onMounted(load)
</script>

<template>
  <div class="mcp-settings">
    <n-alert type="warning" title="MCP 客户端使用你当前的权限">
      持有密钥的客户端可在你当前的权限范围内，通过 Arkade MCP 工具读取数据和执行操作。Arkade 不会逐次要求批准；如果你有运维权限，错误指令可能影响正在运行的服务。只将密钥交给可信客户端，不再使用或发现泄露时立即撤销。
    </n-alert>

    <section class="mcp-section">
      <h3>连接 MCP 客户端</h3>
      <div class="mcp-row">
        <code class="mcp-endpoint">{{ endpoint }}</code>
        <n-button size="small" @click="copy(endpoint, '端点')">复制端点</n-button>
      </div>
      <p class="mcp-note">选择 Streamable HTTP，配置 <code>Authorization: Bearer &lt;MCP_KEY&gt;</code>。请使用客户端可访问的 HTTPS 地址。</p>
      <details>
        <summary>查看 JSON 配置示例</summary>
        <p class="mcp-note">将 <code>&lt;MCP_KEY&gt;</code> 换成创建时显示的密钥。配置字段因客户端而异。</p>
        <pre class="mcp-code"><code>{{ example }}</code></pre>
        <n-button size="small" @click="copy(example, '配置示例')">复制配置示例</n-button>
      </details>
    </section>

    <section class="mcp-section">
      <h3>我的 MCP 密钥</h3>
      <form class="mcp-row" @submit.prevent="create">
        <label for="mcp-key-name">密钥名称</label>
        <n-input
          v-model:value="name"
          class="mcp-name"
          :input-props="{ id: 'mcp-key-name' }"
          placeholder="例如：值班助手"
          :disabled="!!busy"
        />
        <n-button attr-type="submit" type="primary" :disabled="!!busy" :loading="busy === 'create'">创建密钥</n-button>
        <n-button :disabled="!!busy" :loading="busy === 'list'" @click="load">刷新列表</n-button>
      </form>
      <n-alert v-if="error" type="error">{{ error }}</n-alert>
      <div v-if="keys.length" class="mcp-table-wrap">
        <n-table size="small" :single-line="false" aria-label="有效 MCP 密钥">
          <thead><tr><th scope="col">名称</th><th scope="col">前缀</th><th scope="col">创建时间</th><th scope="col">操作</th></tr></thead>
          <tbody>
            <tr v-for="key in keys" :key="key.id">
              <td class="mcp-key-name">{{ key.name }}</td>
              <td><code>{{ key.prefix }}…</code></td>
              <td><time :datetime="key.createdAt">{{ formatAbsoluteTime(Date.parse(key.createdAt) / 1000) }}</time></td>
              <td><n-button size="small" type="error" secondary :disabled="!!busy" @click="revoke(key)">撤销</n-button></td>
            </tr>
          </tbody>
        </n-table>
      </div>
      <p v-else-if="loaded && busy !== 'list'" class="mcp-note">没有有效密钥。为每个客户端创建单独的密钥，便于单独撤销。</p>
      <p v-else-if="busy === 'list'" class="mcp-note" role="status">正在读取密钥列表…</p>
    </section>

    <n-modal
      :show="!!token"
      preset="card"
      title="MCP 密钥已创建"
      :style="{ width: 'min(620px, 92vw)' }"
      :closable="false"
      :close-on-esc="false"
      :mask-closable="false"
    >
      <n-space v-if="token" vertical>
        <n-alert type="warning">明文密钥只显示这一次。请立即保存到安全位置；离开后无法找回。</n-alert>
        <pre class="mcp-code"><code>{{ token }}</code></pre>
        <details>
          <summary>查看含密钥的 JSON 配置</summary>
          <pre class="mcp-code"><code>{{ mcpClientConfig(endpoint, token) }}</code></pre>
        </details>
        <n-space>
          <n-button @click="copy(token, '密钥')">复制密钥</n-button>
          <n-button @click="copy(mcpClientConfig(endpoint, token), '含密钥的配置')">复制含密钥的配置</n-button>
          <n-button type="primary" @click="dismissToken">关闭并隐藏密钥</n-button>
        </n-space>
      </n-space>
    </n-modal>
  </div>
</template>

<style scoped>
.mcp-settings {
  display: grid;
  gap: 20px;
  min-width: 0;
}

.mcp-section {
  display: grid;
  gap: 12px;
  min-width: 0;
  border-top: 1px solid var(--arkade-border-strong);
  padding-top: 16px;
}

.mcp-section h3,
.mcp-note,
.mcp-code {
  margin: 0;
}

.mcp-note {
  color: var(--arkade-text-secondary);
}

.mcp-row {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 8px;
}

.mcp-name {
  width: min(260px, 100%);
}

.mcp-endpoint,
.mcp-code {
  padding: 8px 12px;
  background: var(--arkade-code-bg);
  color: var(--arkade-code-text);
  border-radius: 4px;
  font-family: var(--arkade-mono-font);
  white-space: pre-wrap;
  overflow-wrap: anywhere;
}

.mcp-endpoint {
  flex: 1 1 240px;
  min-width: 0;
}

.mcp-code code {
  font-family: inherit;
}

summary {
  width: fit-content;
  cursor: pointer;
}

details > :not(summary) {
  margin-top: 8px;
}

.mcp-table-wrap {
  max-width: 100%;
  overflow-x: auto;
}

.mcp-key-name {
  max-width: 320px;
  overflow-wrap: anywhere;
}

.mcp-table-wrap time {
  white-space: nowrap;
}
</style>
