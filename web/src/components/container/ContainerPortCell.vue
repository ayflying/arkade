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
 * - 显示成 `宿主端口:容器端口`（如 `38517:8080`）——只给宿主端口的话，
 *   用户看不出这个口子映射到容器里的哪个服务，而这个对应关系正是排查时最需要的
 * - TCP + 有可用访问地址 → 做成链接，新窗口打开
 * - 访问地址来自「编辑服务器」里配置的**完整地址**（带 http/https 前缀）；
 *   内置服务器没配置时自动退回当前访问地址（用户要求）
 * - UDP 照常显示但**不给链接**（浏览器没有对应协议，链接一定是坏的）
 * - 没有结构化 `published_ports`（无发布端口）→ 显示「无端口映射」，
 *   **绝不解析 ports 文本猜链接**
 *
 * ## 2026-11 的两处减法（用户明确要求）
 *
 * 1. 去掉端口后面的「HTTPS」小入口：用户现在把协议直接写进服务器访问地址，
 *    再给一个「换个协议试试」的按钮只会制造歧义；
 * 2. 去掉「@主机（当前访问地址）」这一行说明：它不提供任何操作价值，
 *    还把单元格撑成两行，端口本身应该一眼可读。兜底逻辑本身保留，只是不再显式标注。
 */
import { computed } from 'vue'
import { NTag } from 'naive-ui'
import type { Container } from '@/types/container'
import type { PortDisplayItem } from '@/utils/portLinks'
import { isLoopbackBind, resolvePortAccess, toDisplayItems } from '@/utils/portLinks'

interface Props {
  container: Container
  agentId: string
  /** 服务器访问地址（完整地址，含 http/https 前缀）；空表示未配置，内置服务器会自动兜底 */
  directAddress: string
}

const props = defineProps<Props>()

/**
 * 实际使用的访问地址。
 *
 * 配置了就用配置；内置服务器（agent-local）没配置时自动用当前访问地址兜底。
 * 这里解析成对象后复用，避免每个端口项各解析一遍字符串。
 */
const access = computed(() => resolvePortAccess(props.directAddress, props.agentId))

/**
 * 只要 host_port 有效就展示；双栈等重复项由 toDisplayItems 去重。
 *
 * 第二参数传已解析的访问地址（含协议），由 portLinks 决定端口拼接规则；
 * 这里不再做任何推断。
 */
const items = computed<PortDisplayItem[]>(() =>
  toDisplayItems(props.container.published_ports, access.value)
)

/*
 * 协议里 published_ports 带 omitempty，无发布端口时可为 undefined；
 * 空值文案为「无已上报的端口映射」，不从显示文本反推网络事实。
 */

/**
 * 单项的悬浮说明
 *
 * 不给链接时要讲清原因，否则用户会以为是页面坏了。
 *
 * ⚠️ 这里**不能**断言「一定是因为回环」：
 * `url === null` 有好几种成因（没有配置访问地址、绑了回环、端口非法），
 * 而我们手上只有"有没有链接"这一个结果。
 * 唯一能确定的是回环——那种情况下面单独判断，其余一律给合并说法。
 */
function itemTitle(item: PortDisplayItem): string {
  // 合并掉的其他容器端口必须说出来，否则用户以为只有一条映射
  const extra =
    item.otherContainerPorts.length > 0
      ? `（同一宿主端口还映射了容器端口 ${item.otherContainerPorts.join(', ')}）`
      : ''
  // 用 宿主端口:容器端口 的写法，与界面显示保持一致
  const map = `${String(item.port.host_port)}:${String(item.port.container_port)} / ${item.port.protocol}${extra}`
  if (item.url !== null) return `${map}，打开 ${item.url}`
  if (item.nonTcp) {
    const proto = item.port.protocol.trim().toUpperCase()
    return `${map}，${proto} 协议无法在浏览器中直接打开`
  }
  // 只有这里能确定是回环绑定
  if (isLoopbackBind(item.port.host_ip)) {
    return `${map}，该端口只绑定在本机回环地址，外部无法访问`
  }
  // 其余成因前端无法区分，给合并说明而不是猜一个
  return `${map}，没有可用的访问地址，请到「编辑服务器」填写完整地址，例如 https://example.com`
}
</script>

<template>
  <span v-if="items.length === 0" class="port-cell__muted">
    无已上报的端口映射
  </span>

  <span v-else class="port-cell">
    <template v-for="item in items" :key="item.key">
      <!--
        显示成 宿主端口:容器端口（如 38517:8080）。
        只看宿主端口的话，用户没法判断这个口子通到容器的哪个服务；
        两个端口并排也能区分"同一宿主口映射了多个容器口"这种异常配置。
      -->
      <a
        v-if="item.url !== null"
        :href="item.url"
        target="_blank"
        rel="noopener noreferrer"
        class="port-cell__link arkade-cell-mono"
        :title="itemTitle(item)"
      >{{ item.port.host_port }}:{{ item.port.container_port }}</a>
      <!-- 非 TCP（udp/sctp）用标签区分：仍然是有效信息，只是不能点 -->
      <n-tag
        v-else-if="item.nonTcp"
        size="small"
        :bordered="false"
        class="port-cell__udp arkade-cell-mono"
        :title="itemTitle(item)"
      >{{ item.port.host_port }}:{{ item.port.container_port }} {{ item.port.protocol.toLowerCase() }}</n-tag>
      <span
        v-else
        class="port-cell__plain arkade-cell-mono"
        :title="itemTitle(item)"
      >{{ item.port.host_port }}:{{ item.port.container_port }}</span>
    </template>
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
  color: var(--arkade-link-color, var(--arkade-primary));
  text-decoration: underline;
  text-underline-offset: 2px;
}

.port-cell__link:focus-visible {
  outline: 2px solid var(--arkade-primary);
  outline-offset: 2px;
}

.port-cell__plain {
  color: var(--arkade-text-primary);
}

.port-cell__udp {
  font-family: var(--arkade-mono-font);
}

.port-cell__muted {
  color: var(--arkade-text-tertiary);
}
</style>
