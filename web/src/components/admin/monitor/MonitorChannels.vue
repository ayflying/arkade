<script setup lang="ts">
import { NButton, NCheckbox, NDynamicTags, NFormItem, NInput, NInputNumber, NSelect, NSpace } from 'naive-ui';
import type { Channel, Config } from '@/api/monitor';
import type { ChannelDraft } from './monitorForm';
const config = defineModel<Config>('config', { required: true });
const draft = defineModel<ChannelDraft>('draft', { required: true });
defineProps<{ disabled: boolean; dirty: boolean }>();
defineEmits<{ test: [channel: Channel] }>();
const tlsOptions = [{ label: 'STARTTLS（通常为 587）', value: 'starttls' }, { label: 'TLS（通常为 465）', value: 'tls' }];
</script>
<template>
  <section>
    <h3>通知通道</h3>
    <p>通知密钥加密保存，不回显。未更换通知目标时，地址、签名密钥与密码留空保持原值；也可勾选清除。</p>
    <p>更换机器人地址时须重新填写签名密钥，否则会清除旧签名密钥。更换 SMTP 主机、端口、加密方式或用户名时，必须重填密码或明确清除，不能复用旧密码。</p>
    <n-space vertical size="large">
      <section>
        <n-checkbox v-model:checked="config.dingtalk_enabled" :disabled="disabled">启用钉钉通知</n-checkbox>
        <n-form-item :label="`机器人地址（${draft.has_webhook ? '已配置，留空保持' : '未配置'}）`">
          <n-input v-model:value="draft.webhook" type="password" show-password-on="click" autocomplete="new-password" placeholder="输入完整机器人 Webhook 地址（含 access_token）" :disabled="disabled || draft.clear_webhook" />
        </n-form-item>
        <n-checkbox v-model:checked="draft.clear_webhook" :disabled="disabled">清除已保存的机器人地址</n-checkbox>
        <n-form-item :label="`签名密钥（${draft.has_secret ? '已配置，留空保持' : '未配置'}）`">
          <n-input v-model:value="draft.secret" type="password" show-password-on="click" autocomplete="new-password" placeholder="留空保持原值" :disabled="disabled || draft.clear_secret" />
        </n-form-item>
        <n-space align="center">
          <n-checkbox v-model:checked="draft.clear_secret" :disabled="disabled">清除签名密钥</n-checkbox>
          <n-button :disabled="disabled || dirty" @click="$emit('test', 'dingtalk')">测试钉钉（请先保存）</n-button>
        </n-space>
      </section>
      <section>
        <n-checkbox v-model:checked="config.email_enabled" :disabled="disabled">启用邮件通知</n-checkbox>
        <div class="email-grid">
          <n-form-item label="SMTP 服务器"><n-input v-model:value="draft.email.host" :disabled="disabled" /></n-form-item>
          <n-form-item label="SMTP 端口"><n-input-number v-model:value="draft.email.port" :min="1" :max="65535" :precision="0" :disabled="disabled" /></n-form-item>
          <n-form-item label="传输加密"><n-select v-model:value="draft.email.tls_mode" :options="tlsOptions" :disabled="disabled" /></n-form-item>
          <n-form-item label="SMTP 用户名"><n-input v-model:value="draft.email.username" :disabled="disabled" autocomplete="off" /></n-form-item>
          <n-form-item label="发件人地址"><n-input v-model:value="draft.email.from" :disabled="disabled" /></n-form-item>
          <n-form-item :label="`SMTP 密码（${draft.email.has_password ? '已配置，留空保持' : '未配置'}）`">
            <n-input v-model:value="draft.password" type="password" show-password-on="click" autocomplete="new-password" placeholder="留空保持原值" :disabled="disabled || draft.clear_password" />
          </n-form-item>
        </div>
        <n-form-item label="收件人邮箱（输入后按回车）"><n-dynamic-tags v-model:value="draft.email.to" :disabled="disabled" /></n-form-item>
        <n-space align="center">
          <n-checkbox v-model:checked="draft.clear_password" :disabled="disabled">清除 SMTP 密码</n-checkbox>
          <n-button :disabled="disabled || dirty" @click="$emit('test', 'email')">测试邮件（请先保存）</n-button>
        </n-space>
      </section>
    </n-space>
    <p>测试仅使用已保存的配置，不会发送当前未保存的输入。</p>
  </section>
</template>
<style scoped>
.email-grid { display: grid; grid-template-columns: repeat(auto-fit, minmax(210px, 1fr)); gap: 0 20px; }
p { color: var(--text-color-secondary); }
</style>
