import { httpGet, httpPost, httpPut } from './request';

export interface Config {
  enabled: boolean;
  interval_seconds: number;
  duration_seconds: number;
  repeat_seconds: number;
  offline_seconds: number;
  cpu_percent: number;
  memory_percent: number;
  recovery_notify: boolean;
  excluded_agents: string[];
  excluded_containers: string[];
  dingtalk_enabled: boolean;
  email_enabled: boolean;
}
export interface EmailView {
  host: string;
  port: number;
  tls_mode: 'starttls' | 'tls';
  username: string;
  from: string;
  to: string[];
  has_password: boolean;
}
export interface ConfigView {
  version: number;
  config: Config;
  channels: {
    dingtalk: { webhook: string; has_webhook: boolean; has_secret: boolean };
    email: EmailView;
  };
}
export interface ConfigInput {
  version: number;
  config: Config;
  channels: {
    dingtalk: { webhook?: string; secret?: string; clear_webhook?: boolean; clear_secret?: boolean };
    email: Omit<EmailView, 'has_password'> & { password?: string; clear_password?: boolean };
  };
}
export interface Metric {
  agent_id: string;
  agent_name: string;
  container_id: string;
  name: string;
  state: string;
  health: string;
  cpu_percent: number;
  memory_bytes: number;
  memory_limit_bytes: number;
  memory_percent: number;
  network_rx_per_second: number;
  network_tx_per_second: number;
  block_read_per_second: number;
  block_write_per_second: number;
  stats_available: boolean;
  rates_available: boolean;
  snapshot_complete: boolean;
  at: number;
  error: string;
}
export interface Incident {
  id: string;
  agent_id: string;
  container_id: string;
  kind: string;
  status: string;
  first_seen: number;
  last_seen: number;
  last_notified: number;
  resolved_at: number;
  detail_json: string;
}
export interface Delivery {
  id: string;
  incident_id: string;
  channel: string;
  event: string;
  status: string;
  error: string;
  created_at: number;
}
export interface MonitorStatus { metrics: Metric[]; incidents: Incident[]; deliveries: Delivery[] }
export type Channel = 'dingtalk' | 'email';
export const getMonitorConfig = () => httpGet<ConfigView>('/monitor/config');
export const saveMonitorConfig = (body: ConfigInput) => httpPut<ConfigView>('/monitor/config', body);
export const testMonitorChannel = (channel: Channel) => httpPost<unknown>('/monitor/test', { channel });
export const getMonitorStatus = () => httpGet<MonitorStatus>('/monitor/status');
