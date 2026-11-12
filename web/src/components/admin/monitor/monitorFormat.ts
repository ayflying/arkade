export function timeText(value: number): string {
  return value > 0 ? new Date(value * 1000).toLocaleString('zh-CN') : '—';
}
export function percentText(value: number): string {
  return Number.isFinite(value) ? `${value.toFixed(1)}%` : '—';
}
export function bytesText(value: number): string {
  if (!Number.isFinite(value) || value < 0) return '—';
  const units = ['B', 'KiB', 'MiB', 'GiB', 'TiB'];
  let unit = 0;
  while (value >= 1024 && unit < units.length - 1) { value /= 1024; unit++; }
  return `${value.toFixed(unit === 0 ? 0 : 1)} ${units[unit]}`;
}
export function healthText(value: string): string {
  if (!value || value === 'none') return '无健康检查（不代表健康）';
  return ({ healthy: '健康检查通过', unhealthy: '健康检查失败', starting: '健康检查启动中' } as Record<string, string>)[value] ?? value;
}
export function metricExpired(at: number, intervalSeconds: number, now = Date.now()): boolean {
  return !Number.isFinite(at) || at <= 0 || now - at * 1000 > Math.max(90, intervalSeconds * 3) * 1000;
}
export function rateText(row: { at: number; rates_available: boolean }, rx: number, tx: number, interval: number, now = Date.now()): string {
  if (metricExpired(row.at, interval, now)) return '已过期';
  return row.rates_available ? `${bytesText(rx)}/s / ${bytesText(tx)}/s` : '速率暂不可用（需连续样本）';
}
export function labelText(value: string): string {
  return ({ dingtalk: '钉钉', email: '邮件', active: '告警中', firing: '告警中', resolved: '已恢复', pending: '待处理', sent: '已发送', success: '成功', failed: '失败', recovery: '恢复', alert: '告警', cpu: 'CPU 超限', memory: '内存超限', offline: '服务器失联', unhealthy: '健康检查失败' } as Record<string, string>)[value] ?? value;
}
