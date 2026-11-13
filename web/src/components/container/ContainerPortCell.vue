<script setup lang="ts">
/**
 * 容器列表的端口单元格
 *
 * ## 为什么不再直接显示后端给的 `ports` 文本
 *
 * 那段文本是给人看的，不是给浏览器用的：`0.0.0.0:8080->80/tcp` 里的
 * `0.0.0.0` 是通配绑定，真拼成链接点过去必然失败。
 *
 * ## 规则（与 utils/portLinks 一致）
 *
 * - 只显示**宿主端口**（用户关心的是「我该访问哪个端口」）
 * - TCP + 服务器配了直连地址 + 绑定不是回环 → 做成链接，新窗口打开
 * - UDP 照常显示但**不给链接**（浏览器没有对应协议，链接一定是坏的）
 * - 没有结构化 `published_ports`（无发布端口）→ 显示「无端口映射」，
 *   **绝不解析 ports 文本猜链接**
 */
import { computed } from 'vue'
import { NTag } from 'naive-ui'
import type { Container } from '@/types/container'
import type { PortDisplayItem } from '@/utils/portLinks'
import { isLoopbackBind, toDisplayItems } from '@/utils/portLinks'

interface Props {
  container: Container
  /** 服务器直连地址；空表示未配置，此时任何端口都不给链接 */
  directAddress: string
}

const props = defineProps<Props>()

/** 只要 host_port 有效就展示；双栈等重复项由 toDisplayItems 去重 */
const items = computed<PortDisplayItem[]>(() =>
  toDisplayItems(props.container.published_ports, props.directAddress)
)

/*
 * 协议里 published_ports 带 omitempty，无发布端口时可为 undefined；
 * 空值文案为「无已上报的端口映射」，不从显示文本反推网络事实。
 */

/**
 * 单项的悬浮说明。
 *
 * 不给链接时要讲清原因，否则用户会以为是页面坏了。
 *
 * ⚠️ 这里**不能**断言「一定是因为回环」：
 * `url === null` 有好几种成因（没配直连地址、地址是脏数据、绑了回环、
 * 端口非法），而我们手上只有"有没有链接"这一个结果。
 * 唯一能确定的是回环——那种情况下面单独判断，其余一律给合并说法。
 */
function itemTitle(item: PortDisplayItem): string {
  // 合并掉的其他容器端口必须说出来，否则用户以为只有一条映射
  const extra =
    item.otherContainerPorts.length > 0
      ? `（同一宿主端口还映射了容器端口 ${item.otherContainerPorts.join(', ')}）`
      : ''
  const map = `容器端口 ${String(item.port.container_port)} / ${item.port.protocol}${extra}`
  if (item.url !== null) return `${map}，点击在新窗口打开`
  if (item.nonTcp) {
    const proto = item.port.protocol.trim().toUpperCase()
    return `${map}，${proto} 协议无法在浏览器中直接打开`
  }
  // 只有这里能确定是回环绑定
  if (isLoopbackBind(item.port.host_ip)) {
    return `${map}，该端口只绑定在本机回环地址，外部无法访问`
  }
  // 其余成因前端无法区分，给合并说明而不是猜一个
  return `${map}，未配置有效直连地址或绑定回环，不生成链接`
}

/**
 * 把 http 链接换成 https。
 *
 * 有些服务只挂在了 TLS 上（用户自己的反代），但端口映射本身看不出这点，
 * 所以不猜、不固定 443，只是给一个"换协议再试一次"的轻量入口。
 */
function httpsUrl(url: string): string {
  return url.replace(/^http:\/\//, 'https://')
}
</script>

<template>
  <span v-if="items.length === 0" class="port-cell__muted">
    无已上报的端口映射
  </span>

  <span v-else class="port-cell">
    <template v-for="item in items" :key="item.key">
      <a
        v-if="item.url !== null"
        :href="item.url"
        target="_blank"
        rel="noopener noreferrer"
        class="port-cell__link arkade-cell-mono"
        :title="itemTitle(item)"
      >{{ item.port.host_port }}</a>
      <!--
        轻量 HTTPS 入口：只在能访问的前提下出现。
        不推断 443、不改端口，只是把协议换掉再试一次——
        有些服务只挂在 TLS 上，而端口映射本身看不出这一点。
      -->
      <a
        v-if="item.url !== null"
        :href="httpsUrl(item.url)"
        target="_blank"
        rel="noopener noreferrer"
        class="port-cell__https"
        :title="`用 HTTPS 打开（端口 ${String(item.port.host_port)} 不变）`"
      >HTTPS</a>
      <!-- 非 TCP（udp/sctp）用标签区分：仍然是有效信息，只是不能点 -->
      <n-tag
        v-else-if="item.nonTcp"
        size="small"
        :bordered="false"
        class="port-cell__udp arkade-cell-mono"
        :title="itemTitle(item)"
      >{{ item.port.host_port }} {{ item.port.protocol.toLowerCase() }}</n-tag>
      <span
        v-else
        class="port-cell__plain arkade-cell-mono"
        :title="itemTitle(item)"
      >{{ item.port.host_port }}</span>
    </template>
    <!-- 绑在通配地址时提示实际访问地址来自直连地址，避免用户以为看到的就是宿主 IP -->
    <span
      v-if="directAddress !== ''"
      class="port-cell__hint"
      :title="`访问地址使用服务器直连地址 ${directAddress}`"
    >@{{ directAddress }}</span>
  </span>
</template>

<style scoped>
.port-cell {
  display: inline-flex;
  align-items: center;
  flex-wrap: wrap;
  gap: 6px;
}

.port-cell__link {
  color: var(--arkade-link-color, #2080f0);
  text-decoration: none;
}

.port-cell__link:hover {
  text-decoration: underline;
}

/*
 * HTTPS 入口做得很轻：一个小标签，不抢注意力。
 * 它的作用是"换个协议再试一次"，不是主要操作。
 */
.port-cell__https {
  display: inline-flex;
  align-items: center;
  height: 16px;
  padding: 0 4px;
  font-size: 9px;
  line-height: 1;
  font-weight: 600;
  color: var(--arkade-text-tertiary);
  text-decoration: none;
  border: 1px solid var(--arkade-border-color);
  border-radius: 3px;
  opacity: 0.65;
}

.port-cell__https:hover {
  color: var(--arkade-link-color, #2080f0);
  border-color: var(--arkade-link-color, #2080f0);
  opacity: 1;
}

.port-cell__plain {
  color: var(--arkade-text-primary);
}

.port-cell__udp {
  font-family: var(--arkade-mono-font);
}

.port-cell__hint {
  font-size: 11px;
  color: var(--arkade-text-tertiary);
}

.port-cell__muted {
  color: var(--arkade-text-tertiary);
}
</style>
