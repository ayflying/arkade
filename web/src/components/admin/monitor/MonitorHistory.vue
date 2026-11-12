<script setup lang="ts">
import { NDataTable, type DataTableColumns } from 'naive-ui';
import type { Delivery, Incident } from '@/api/monitor';
import { labelText, timeText } from './monitorFormat';
defineProps<{ incidents: Incident[]; deliveries: Delivery[] }>();
// 不展示原始 detail_json：告警详情由服务端管理，避免未知字段泄露通知凭据。
const incidentColumns: DataTableColumns<Incident> = [
  { title: '告警 ID', key: 'id' },
  { title: '服务器 ID', key: 'agent_id' },
  { title: '容器 ID', key: 'container_id', render: row => row.container_id || '—' },
  { title: '类型', key: 'kind', render: row => labelText(row.kind) },
  { title: '状态', key: 'status', render: row => labelText(row.status) },
  { title: '首次发现', key: 'first_seen', render: row => timeText(row.first_seen) },
  { title: '最近发现', key: 'last_seen', render: row => timeText(row.last_seen) },
  { title: '最近通知', key: 'last_notified', render: row => timeText(row.last_notified) },
  { title: '恢复时间', key: 'resolved_at', render: row => timeText(row.resolved_at) },
];
const deliveryColumns: DataTableColumns<Delivery> = [
  { title: '记录 ID', key: 'id' },
  { title: '告警 ID', key: 'incident_id', render: row => row.incident_id || '测试通知' },
  { title: '通道', key: 'channel', render: row => labelText(row.channel) },
  { title: '事件', key: 'event', render: row => labelText(row.event) },
  { title: '状态', key: 'status', render: row => labelText(row.status) },
  { title: '错误', key: 'error', render: row => row.error || '—' },
  { title: '时间', key: 'created_at', render: row => timeText(row.created_at) },
];
</script>
<template>
  <section>
    <h3>告警事件</h3>
    <n-data-table :columns="incidentColumns" :data="incidents" :row-key="row => row.id" :scroll-x="1400" :pagination="{ pageSize: 10 }" />
    <p v-if="!incidents.length">暂无告警事件。无事件不等于所有容器都健康，请同时查看采集时间和健康检查。</p>
    <h3>通知投递记录</h3>
    <n-data-table :columns="deliveryColumns" :data="deliveries" :row-key="row => row.id" :scroll-x="1000" :pagination="{ pageSize: 10 }" />
    <p v-if="!deliveries.length">暂无投递记录。保存通知通道后可发送测试通知。</p>
  </section>
</template>
