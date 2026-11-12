<script setup lang="ts">
/**
 * 登录页
 *
 * 行为：
 * - 提交后调用 authStore.login，成功则跳转：优先回跳守卫记录的 redirect，否则去 /agents；
 * - 失败用 n-alert 内联展示中文错误（表单类错误用内联比 toast 更易察觉且不会消失）；
 * - 登录中禁用表单并显示 loading，防止重复提交。
 *
 * 说明：表单校验在前端只做「非空」这一层，账号密码正确与否完全交后端判定，
 * 前端不做任何账号合法性猜测。
 */
import { computed, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { NAlert, NButton, NForm, NFormItem, NInput } from 'naive-ui'
import { ApiError } from '@/types/api'
import { useAuthStore } from '@/stores/auth'
import { useAuthOptions } from '@/composables/useAuthOptions'
import { safeLocalPath, safeProviderStart } from '@/utils/accessPolicy'

const auth = useAuthStore()
const router = useRouter()
const route = useRoute()
const { options, error: optionsError, load } = useAuthOptions()
const providers = computed(() => (options.value?.providers ?? []).filter((provider) => safeProviderStart(provider.start_url) !== null))

const username = ref('')
const password = ref('')
/** 内联错误文案；空串表示无错误 */
const errorText = ref('')

const submitting = computed(() => auth.loggingIn)

/**
 * 计算登录成功后的目标地址。
 *
 * 安全考虑：redirect 参数只接受站内绝对路径（以 / 开头且非 //），
 * 防止被构造成 //evil.com 造成开放重定向。
 */
const redirectTarget = computed<string>(() => {
  const raw = route.query.redirect
  const value = Array.isArray(raw) ? raw[0] : raw
  if (typeof value === 'string' && value.startsWith('/') && !value.startsWith('//')) {
    return safeLocalPath(value)
  }
  return '/overview'
})

async function handleSubmit(): Promise<void> {
  if (submitting.value) return
  errorText.value = ''

  // 基础非空校验：避免把空请求发给后端
  if (username.value.trim() === '' || password.value === '') {
    errorText.value = '请输入用户名和密码'
    return
  }

  try {
    await auth.login({ username: username.value.trim(), password: password.value })
    // 登录成功后清空本地密码，避免残留在内存里
    password.value = ''
    await router.replace(auth.user?.must_change_password ? '/account' : redirectTarget.value)
  } catch (err) {
    errorText.value = err instanceof ApiError ? err.message : '登录失败，请稍后重试'
  }
}
</script>

<template>
  <div class="login-page">
    <div class="login-card">
      <div class="login-brand">
        <img src="/favicon.svg" alt="Arkade 图标" class="login-brand__logo" />
        <div>
          <h1 class="login-brand__title">{{ options?.site_name || 'Arkade 容器方舟' }}</h1>
          <p class="login-brand__desc">多服务器容器管理平台</p>
        </div>
      </div>

      <n-alert v-if="errorText !== ''" type="error" :bordered="false" class="login-error">
        {{ errorText }}
      </n-alert>

      <n-alert v-if="options?.login_notice" type="info">{{ options.login_notice }}</n-alert>
      <n-alert v-if="route.query.oidc_error" type="error">第三方登录未完成，请重试或使用本地账户登录。</n-alert>
      <n-alert v-if="optionsError" type="warning">{{ optionsError }} <n-button text @click="load">重新读取登录选项</n-button></n-alert>
      <n-alert v-if="route.query.registered" type="success">注册成功，请使用新账户登录。</n-alert>
      <n-alert v-if="route.query.password_changed" type="success">密码已修改，所有会话已失效，请重新登录。</n-alert>
      <n-form label-placement="top" @submit.prevent="handleSubmit">
        <n-form-item label="用户名">
          <n-input
            v-model:value="username"
            placeholder="请输入用户名"
            :disabled="submitting"
            :input-props="{ autocomplete: 'username', name: 'username' }"
            autofocus
            @keyup.enter="handleSubmit"
          />
        </n-form-item>
        <n-form-item label="密码">
          <n-input
            v-model:value="password"
            type="password"
            show-password-on="click"
            placeholder="请输入密码"
            :disabled="submitting"
            :input-props="{ autocomplete: 'current-password', name: 'password' }"
            @keyup.enter="handleSubmit"
          />
        </n-form-item>
        <n-button
          type="primary"
          block
          attr-type="submit"
          :loading="submitting"
          :disabled="submitting"
        >
          {{ submitting ? '登录中…' : '登录' }}
        </n-button>
      </n-form>

      <p v-for="provider in providers" :key="provider.id"><n-button tag="a" :href="provider.start_url" block>使用 {{ provider.display_name }} 登录</n-button></p>
      <p v-if="options?.registration_enabled"><n-button text @click="router.push('/register')">注册新账户</n-button></p>
      <p class="login-footnote">登录状态通过 Cookie 维持，关闭浏览器后需重新登录。</p>
    </div>
  </div>
</template>

<style scoped>
.login-page {
  display: flex;
  align-items: center;
  justify-content: center;
  min-height: 100vh;
  padding: 24px;
  background: linear-gradient(135deg, #eef4ff 0%, #f7f9fc 45%, #eef7f3 100%);
}

.login-card {
  width: 100%;
  max-width: 380px;
  padding: 32px 28px 24px;
  background: #fff;
  border-radius: 12px;
  box-shadow: 0 8px 32px rgba(31, 34, 37, 0.08);
}

.login-brand {
  display: flex;
  align-items: center;
  gap: 12px;
  margin-bottom: 24px;
}

.login-brand__logo {
  width: 40px;
  height: 40px;
}

.login-brand__title {
  margin: 0;
  font-size: 18px;
  font-weight: 600;
  color: #1f2225;
}

.login-brand__desc {
  margin: 2px 0 0;
  font-size: 12px;
  color: #8b9099;
}

.login-error {
  margin-bottom: 16px;
}

.login-footnote {
  margin: 16px 0 0;
  font-size: 12px;
  color: #b5b9c0;
  text-align: center;
}
</style>
