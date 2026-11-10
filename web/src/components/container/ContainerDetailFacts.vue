<script setup lang="ts">
/**
 * 容器概览：基本信息 / 运行状态 / 网络与端口 / 资源限制
 *
 * 拆成组件而不是塞进页面：这一块字段很多，混在页面里会让详情页变成第二个长文件。
 *
 * ## 显示原则
 *
 * 不确定就不显示，绝不编造。契约里每个字段都可能是 null（未限制/未启动/未知），
 * 把 null 显示成 0 会误导用户以为「限制为 0」或「退出码 0」。
 */
import { computed } from 'vue'
import { NTag } from 'naive-ui'
import ContainerStateTag from '@/components/container/ContainerStateTag.vue'
import type { ContainerDetail } from '@/types/container'
import { buildPortUrl, isUdp } from '@/utils/portLinks'
import { formatAbsoluteTime, formatRelativeTime } from '@/utils/formatTime'
import { formatBytes } from '@/utils/formatSize'

interface Props {
  detail: ContainerDetail | null
  /** 详情是否因权限被拒（此时不渲染字段，避免显示成一堆「—」） */
  forbidden: boolean
  /** 服务器直连地址，用于端口链接 */
  directAddress: string
}

const props = defineProps<Props>()

/**
 * RFC3339 字符串 → Unix 秒。
 *
 * ⚠️ 详情的 `created`/`started_at`/`finished_at` 是 **RFC3339 字符串**，
 * 而共用的 formatTime 工具收的是 Unix **秒**。中间必须真的解析时间，
 * 不能 `Number(str)`——那样得到的是 NaN，会被当成「没有值」，
 * 把真实时间显示成「—」。
 *
 * 解析失败或零值一律返回 null，由展示层显示「—」。
 */
function rfc3339ToSeconds(text: string): number | null {
  if (text === '') return null
  const ms = Date.parse(text)
  if (Number.isNaN(ms)) return null
  // Date.parse 对 0001-01-01 会给出一个极大的负值，过滤掉
  if (ms <= 0) return null
  return Math.floor(ms / 1000)
}

/** 相对时间：相对时间为主，绝对时间作为 title */
function relative(text: string): string {
  const sec = rfc3339ToSeconds(text)
  return sec === null ? '—' : formatRelativeTime(sec)
}
function absolute(text: string): string {
  const sec = rfc3339ToSeconds(text)
  return sec === null ? '—' : formatAbsoluteTime(sec)
}

/** 退出码：协议里是 int，0 是合法的「正常退出」 */
const exitCodeText = computed(() => {
  const d = props.detail
  if (d === null) return '—'
  return String(d.exit_code)
})

/**
 * 退出码的补充说明。
 *
 * 协议里 exit_code 是 int（不是指针），所以容器还在跑时它的值是 0——
 * 但「运行中」和「正常退出」是两回事，必须靠 state 区分，
 * 不能拿 0 直接说「正常退出」。
 */
const exitCodeHint = computed(() => {
  const d = props.detail
  if (d === null) return ''
  const running = d.state.trim().toLowerCase() === 'running'
  if (running) return '容器仍在运行，此值暂不代表结果'
  if (d.exit_code === 0) return '正常退出'
  return '异常退出'
})

const restartText = computed(() => {
  const d = props.detail
  if (d === null) return '—'
  return `${String(d.restart_count)} 次`
})

/**
 * 内存上限：**0 表示未设置上限**。
 *
 * 协议注释明确这是 daemon 的默认语义，不是"上限为 0"。
 * 显示成 "0 字节" 会让用户以为容器被限制到无法运行。
 */
const memoryText = computed(() => {
  const d = props.detail
  if (d === null) return '—'
  const bytes = d.resource_limits.memory_bytes
  if (bytes <= 0) return '未限制'
  return formatBytes(bytes)
})

/** CPU 上限：nano_cpus 是 10^-9 核；0 同样表示未限制 */
const cpuText = computed(() => {
  const d = props.detail
  if (d === null) return '—'
  const nano = d.resource_limits.nano_cpus
  if (nano <= 0) return '未限制'
  const cores = nano / 1e9
  // 保留两位小数就够，避免显示 1.500000000
  return `${cores.toFixed(2)} 核`
})

/** 进程数上限：协议里是带 omitempty 的指针，null 表示没设 */
const pidsText = computed(() => {
  const d = props.detail
  if (d === null) return '—'
  const n = d.resource_limits.pids_limit
  if (n === null || n <= 0) return '未限制'
  return String(n)
})

const commandText = computed(() => {
  const d = props.detail
  if (d === null) return ''
  if (d.command.length === 0) return '（使用镜像默认入口）'
  return d.command.join(' ')
})

/** 端口行：只在这里展示全部映射，能链接的做成链接 */
const portRows = computed(() => {
  const d = props.detail
  if (d === null) return []
  return d.published_ports.map((p) => ({
    key: `${p.host_ip}:${String(p.host_port)}:${String(p.container_port)}/${p.protocol}`,
    text: `${p.host_ip !== '' ? p.host_ip : '0.0.0.0'}:${String(p.host_port)} → ${String(p.container_port)}/${p.protocol}`,
    url: buildPortUrl(p, props.directAddress),
    udp: isUdp(p)
  }))
})
</script>

<template>
  <div v-if="detail === null" class="facts__empty">
    {{ forbidden ? '没有权限查看这些信息。' : '暂无数据。' }}
  </div>

  <div v-else class="facts">
    <!-- 运行状态 -->
    <section class="facts__section">
      <h4 class="facts__title">运行状态</h4>
      <dl class="facts__grid">
        <dt>状态</dt>
        <dd><container-state-tag :state="detail.state" /><span v-if="detail.status !== ''" class="facts__muted"> {{ detail.status }}</span></dd>

        <dt>退出码</dt>
        <dd>
          {{ exitCodeText }}
          <span v-if="exitCodeHint !== ''" class="facts__muted">{{ exitCodeHint }}</span>
        </dd>

        <dt>重启次数</dt>
        <dd>{{ restartText }}</dd>

        <dt>创建时间</dt>
        <dd :title="absolute(detail.created)">{{ relative(detail.created) }}</dd>

        <dt>启动时间</dt>
        <dd :title="absolute(detail.started_at)">
          {{ detail.started_at === '' ? '尚未启动' : relative(detail.started_at) }}
        </dd>

        <dt>结束时间</dt>
        <dd :title="absolute(detail.finished_at)">
          {{ detail.finished_at === '' ? '尚未结束' : relative(detail.finished_at) }}
        </dd>
      </dl>
    </section>

    <!-- 启动命令 -->
    <section class="facts__section">
      <h4 class="facts__title">启动命令</h4>
      <pre class="facts__command">{{ commandText }}</pre>
      <p class="facts__note">环境变量不在展示范围内。</p>
    </section>

    <!-- 网络 -->
    <section class="facts__section">
      <h4 class="facts__title">网络</h4>
      <dl class="facts__grid">
        <dt>模式</dt>
        <dd>{{ detail.network_mode === '' ? '—' : detail.network_mode }}</dd>

        <dt>网络</dt>
        <dd>
          <span v-if="detail.networks.length === 0" class="facts__muted">—</span>
          <span v-for="n in detail.networks" :key="n.name" class="facts__net">
            {{ n.name }}<span v-if="n.ip_address !== ''" class="facts__muted"> · {{ n.ip_address }}</span>
          </span>
        </dd>
      </dl>
    </section>

    <!-- 端口 -->
    <section class="facts__section">
      <h4 class="facts__title">端口</h4>
      <div v-if="portRows.length === 0" class="facts__muted">无端口映射</div>
      <ul v-else class="facts__ports">
        <li v-for="row in portRows" :key="row.key" class="facts__port">
          <a
            v-if="row.url !== null"
            :href="row.url"
            target="_blank"
            rel="noopener noreferrer"
            class="facts__link"
          >{{ row.text }}</a>
          <span v-else>{{ row.text }}</span>
          <n-tag v-if="row.udp" size="small" :bordered="false">UDP 不可直接打开</n-tag>
          <span v-else-if="row.url === null" class="facts__muted">（无法生成访问链接）</span>
        </li>
      </ul>
      <div v-if="detail.exposed_ports.length > 0" class="facts__note">
        仅声明未发布：{{ detail.exposed_ports.join(', ') }}
      </div>
    </section>

    <!-- 资源限制 -->
    <section class="facts__section">
      <h4 class="facts__title">资源限制</h4>
      <dl class="facts__grid">
        <dt>内存</dt>
        <dd>{{ memoryText }}</dd>
        <dt>CPU</dt>
        <dd>{{ cpuText }}</dd>
        <dt>进程数</dt>
        <dd>{{ pidsText }}</dd>
      </dl>
    </section>
  </div>
</template>

<style scoped>
.facts {
  display: flex;
  flex-direction: column;
  gap: 20px;
}

.facts__empty {
  padding: 20px 0;
  color: var(--arkade-text-tertiary);
  font-size: 13px;
}

.facts__title {
  margin: 0 0 8px;
  font-size: 13px;
  font-weight: 600;
  color: var(--arkade-text-primary);
}

.facts__grid {
  display: grid;
  grid-template-columns: 88px 1fr;
  gap: 6px 12px;
  margin: 0;
  font-size: 13px;
}

.facts__grid dt {
  color: var(--arkade-text-secondary);
}

.facts__grid dd {
  margin: 0;
  color: var(--arkade-text-primary);
  word-break: break-all;
}

.facts__command {
  margin: 0;
  padding: 8px 10px;
  font-family: var(--arkade-mono-font);
  font-size: 12px;
  white-space: pre-wrap;
  word-break: break-all;
  background: var(--arkade-page-bg);
  border: 1px solid var(--arkade-border-color);
  border-radius: 4px;
}

.facts__net {
  display: block;
}

.facts__muted {
  color: var(--arkade-text-tertiary);
}

.facts__note {
  margin: 6px 0 0;
  font-size: 12px;
  color: var(--arkade-text-tertiary);
}

.facts__ports {
  margin: 0;
  padding: 0;
  list-style: none;
  display: flex;
  flex-direction: column;
  gap: 6px;
  font-size: 13px;
}

.facts__port {
  display: flex;
  align-items: center;
  gap: 8px;
  flex-wrap: wrap;
  font-family: var(--arkade-mono-font);
}

.facts__link {
  color: var(--arkade-link-color, #2080f0);
  text-decoration: none;
}

.facts__link:hover {
  text-decoration: underline;
}
</style>
