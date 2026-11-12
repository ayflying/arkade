<script setup lang="ts">
import { computed } from 'vue';
import { NDataTable, type DataTableColumns } from 'naive-ui';
import type { Metric } from '@/api/monitor';
import { bytesText, healthText, metricExpired, percentText, rateText, timeText } from './monitorFormat';
const props = defineProps<{ metrics: Metric[]; intervalSeconds: number; now: number }>();
const stale = (row: Metric) => metricExpired(row.at, props.intervalSeconds, props.now);
const stat = (row: Metric, value: number) => stale(row) ? '已过期' : row.stats_available ? percentText(value) : '无统计数据';
const columns = computed<DataTableColumns<Metric>>(() => [
  { title: '服务器', key: 'agent_name', render: row => row.agent_name || row.agent_id },
  { title: '容器 / ID', key: 'name', render: row => `${row.name || '—'} / ${row.container_id}` },
  { title: '运行状态', key: 'state', render: row => stale(row) ? '已过期' : row.state },
  { title: '健康检查', key: 'health', render: row => stale(row) ? '已过期' : healthText(row.health) },
  { title: 'CPU', key: 'cpu_percent', render: row => stat(row, row.cpu_percent) },
  { title: '内存', key: 'memory_percent', render: row => stale(row) ? '已过期' : row.stats_available ? `${percentText(row.memory_percent)} · ${bytesText(row.memory_bytes)} / ${bytesText(row.memory_limit_bytes)}` : '无统计数据' },
  { title: '网络接收 / 发送', key: 'network_rx_per_second', render: row => rateText(row, row.network_rx_per_second, row.network_tx_per_second, props.intervalSeconds, props.now) },
  { title: '磁盘读取 / 写入', key: 'block_read_per_second', render: row => rateText(row, row.block_read_per_second, row.block_write_per_second, props.intervalSeconds, props.now) },
  { title: '快照', key: 'snapshot_complete', render: row => stale(row) ? '已过期' : row.snapshot_complete ? '完整' : '不完整（部分采集失败）' },
  { title: '采集时间', key: 'at', render: row => timeText(row.at) },
  { title: '采集错误', key: 'error', render: row => row.error || '—' },
]);
</script>
<template>
  <section>
    <h3>容器实时指标</h3>
    <n-data-table :columns="columns" :data="metrics" :row-key="row => `${row.agent_id}/${row.container_id}`" :scroll-x="1900" :pagination="{ pageSize: 10 }" />
    <p>超过采集间隔的 3 倍（至少 90 秒）未更新的指标标记为已过期，不当作当前状态。</p>
    <p v-if="!metrics.length">暂无指标。请启用监控并等待采集；排除的服务器或容器不会参与监控。</p>
  </section>
</template>
