import type { Config, ConfigInput, ConfigView, EmailView } from '@/api/monitor';

export const defaultConfig = (): Config => ({
  enabled: false, interval_seconds: 30, duration_seconds: 120, repeat_seconds: 1800,
  offline_seconds: 120, cpu_percent: 80, memory_percent: 80, recovery_notify: true,
  excluded_agents: [], excluded_containers: [], dingtalk_enabled: false, email_enabled: false,
});
export interface ChannelDraft {
  webhook: string;
  secret: string;
  password: string;
  clear_webhook: boolean;
  clear_secret: boolean;
  clear_password: boolean;
  has_webhook: boolean;
  has_secret: boolean;
  email: EmailView;
}
export function channelDraft(view: ConfigView): ChannelDraft {
  return {
    // 即使服务端意外返回完整 URL，也不回填含 access_token 的地址。
    webhook: '', secret: '', password: '', clear_webhook: false, clear_secret: false,
    clear_password: false, has_webhook: view.channels.dingtalk.has_webhook,
    has_secret: view.channels.dingtalk.has_secret,
    email: { ...view.channels.email, to: [...(view.channels.email.to ?? [])] },
  };
}
export function monitorPayload(version: number, config: Config, draft: ChannelDraft): ConfigInput {
  const { has_password: _hasPassword, ...email } = draft.email;
  return {
    version, config: { ...config, excluded_agents: [...config.excluded_agents], excluded_containers: [...config.excluded_containers] },
    channels: {
      dingtalk: {
        ...(draft.clear_webhook ? { clear_webhook: true } : draft.webhook.trim() ? { webhook: draft.webhook.trim() } : {}),
        ...(draft.clear_secret ? { clear_secret: true } : draft.secret ? { secret: draft.secret } : {}),
      },
      email: { ...email, to: [...email.to], ...(draft.clear_password ? { clear_password: true } : draft.password ? { password: draft.password } : {}) },
    },
  };
}
export function configIssue(config: Config, draft: ChannelDraft): string {
  const ranges = [
    ['interval_seconds', 10, 3600], ['duration_seconds', 0, 86400],
    ['repeat_seconds', 60, 604800], ['offline_seconds', 30, 86400],
  ] as const;
  for (const [key, min, max] of ranges) {
    if (!Number.isInteger(config[key]) || config[key] < min || config[key] > max) return `时间必须为整数秒：采集 10–3600、持续 0–86400、重复 60–604800、失联 30–86400`;
  }
  if (!Number.isFinite(config.cpu_percent) || config.cpu_percent <= 0 || config.cpu_percent > 12800) return 'CPU 阈值必须大于 0 且不超过 12800%（一个核为 100%）';
  if (!Number.isFinite(config.memory_percent) || config.memory_percent <= 0 || config.memory_percent > 100) return '内存阈值必须大于 0 且不超过 100%';
  if (!Number.isInteger(draft.email.port) || draft.email.port < 1 || draft.email.port > 65535) return 'SMTP 端口必须在 1–65535 之间';
  if (config.dingtalk_enabled && (draft.clear_webhook || (!draft.webhook.trim() && !draft.has_webhook))) return '启用钉钉通知前请配置机器人地址';
  if (config.email_enabled && (!draft.email.host.trim() || !draft.email.from.trim() || !draft.email.to.length)) return '启用邮件通知前请填写服务器、发件人和收件人';
  return '';
}
