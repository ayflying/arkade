<script setup lang="ts">
/**
 * 容器详情页的「控制台」面板
 *
 * ============================ 这是"一次性执行"，不是远程终端 ============================
 *
 * 后端接口是 `POST .../exec`：一次请求 = 一个进程 = 一份输出快照。
 * 没有 stdin 长连接，也没有 PTY。因此：
 *
 * - `cd`、`export`、`cd && make` 这类**跨命令的状态不会保留**；
 * - `vi`、`top`、`less` 这类需要交互的程序**无法使用**（它们会立刻退出或报错）；
 * - 每条命令结束就断开，所以不会有"连接掉线"的问题。
 *
 * 之所以这么设计：控制通道是信令通道（单条消息 8 MiB、出站队列 32 条），
 * 逐键搬运会稳定撞上队列上限。一次性执行与此并存才是诚实的做法，
 * 而不是做一个"看起来像终端、实际会卡死"的半成品。
 *
 * 需要多条语句时把它们写成一条：`a && b`、`for i in ...; do ...; done`。
 *
 * ============================ 两种模式 ============================
 *
 * 1. 默认（shell 控制台）：选定 shell（/bin/sh、/bin/bash、/bin/ash、/bin/dash），
 *    每行命令用 `<shell> -c "<这一行>"` 执行；
 * 2. 自定义命令：关闭 shell 前缀，直接把你写的整行按 POSIX 常见规则拆成
 *    命令向量执行（支持引号与转义，不支持管道/重定向——要它们请包一层 `sh -c`）。
 *
 * ============================ 权限 ============================
 *
 * 执行命令需要 **manage**。403 时给出明确说明而不是空白或"加载失败"。
 */
import { computed, nextTick, ref } from 'vue'
import { NAlert, NButton, NInput, NSelect, NSwitch, NTag } from 'naive-ui'
import { execContainer } from '@/api/containers'
import { splitCommandLine } from '@/utils/commandLine'
import { isLogNearBottom, scrollLogToBottom } from '@/utils/logScroll'
import { ApiError } from '@/types/api'

interface Props {
  agentId: string
  containerId: string
  /** 面板标题里显示的名字，不影响请求 */
  name: string
  /** Agent 离线时不能执行（请求会 503） */
  online: boolean
}

const props = defineProps<Props>()

/** 预设 shell：Docker 官方镜像基本都有 /bin/sh；bash 需要额外安装（alpine 默认没有）。 */
const SHELL_OPTIONS = [
  { label: '/bin/sh（最常见）', value: '/bin/sh' },
  { label: '/bin/bash', value: '/bin/bash' },
  { label: '/bin/ash（BusyBox）', value: '/bin/ash' },
  { label: '/bin/dash（Debian）', value: '/bin/dash' }
]

type EntryKind = 'cmd' | 'out' | 'err' | 'note'

interface Entry {
  kind: EntryKind
  text: string
}

const shell = ref('/bin/sh')
const customMode = ref(false)
const customCommand = ref('')
const user = ref('root')
/** 空表示沿用镜像的 WorkingDir，不硬编码 "/" */
const workdir = ref('')

const connected = ref(false)
const running = ref(false)
const forbidden = ref(false)
const errorText = ref('')
const entries = ref<Entry[]>([])

const line = ref('')
const scrollRef = ref<HTMLElement | null>(null)
/** 命令历史（↑/↓ 调用），只在当前页面生命周期内有效 */
const history = ref<string[]>([])
const historyIndex = ref(-1)

const disabled = computed(() => !props.online || props.agentId === '' || props.containerId === '')
const canSubmit = computed(() => connected.value && !running.value && !disabled.value)

/** 追加一条记录并按需贴底 */
async function append(kind: EntryKind, text: string): Promise<void> {
  if (text === '') return
  const stick = isLogNearBottom(scrollRef.value)
  entries.value.push({ kind, text })
  if (stick) {
    await nextTick()
    scrollLogToBottom(scrollRef.value)
  }
}

/** 进入终端 / 启用输入行 */
function connect(): void {
  if (disabled.value) return
  errorText.value = ''
  forbidden.value = false
  connected.value = true
  void append('note', `已就绪：${shell.value}，用户 ${user.value === '' ? '（镜像默认）' : user.value}。`)
  void append('note', '每条命令都是独立进程：cd、export 不会保留；交互式程序（vi/top）无法使用。')
}

function disconnect(): void {
  connected.value = false
  void append('note', '已结束输入。重新点击「开始」可继续执行。')
}

function clearTranscript(): void {
  entries.value = []
}

/** 把一次执行结果写进转写区 */
async function report(res: Awaited<ReturnType<typeof execContainer>>): Promise<void> {
  if (res.stdout !== '') await append('out', res.stdout)
  if (res.stderr !== '') await append('err', res.stderr)

  // 退出码 -1 = 无法确定（超时或查询失败），绝不显示成 0。
  const parts: string[] = []
  if (res.timed_out) {
    parts.push('等待超时，已停止接收输出；容器内进程可能仍在运行')
  } else if (res.exit_code < 0) {
    parts.push('退出码未知')
  } else {
    parts.push(`退出码 ${res.exit_code}`)
  }
  if (res.duration_ms > 0) parts.push(`耗时 ${res.duration_ms} ms`)
  if (res.truncated) parts.push('输出超过上限，只显示前一部分')
  await append('note', `[${parts.join(' · ')}]`)
}

/** 统一执行入口：把命令向量交给后端，并按结果写转写区 */
async function run(command: string[], label: string): Promise<void> {
  if (command.length === 0) return
  await append('cmd', `$ ${label}`)
  running.value = true
  errorText.value = ''
  try {
    const res = await execContainer(props.agentId, props.containerId, {
      command,
      user: user.value.trim(),
      workdir: workdir.value.trim(),
      // 30 秒与后端默认一致；超时后如实显示"已停止等待"
      timeout_sec: 30
    })
    forbidden.value = false
    await report(res)
  } catch (err) {
    if (err instanceof ApiError && err.status === 403) {
      forbidden.value = true
      errorText.value = '没有执行权限：需要该服务器的管理权限（manage）。日志仍然可查看。'
    } else {
      errorText.value = err instanceof Error ? err.message : '命令执行失败'
    }
    await append('err', errorText.value)
  } finally {
    running.value = false
  }
}

/** 提交输入行：默认模式包一层 shell，自定义模式直接拆成向量 */
async function submitLine(): Promise<void> {
  if (!canSubmit.value) return
  const text = line.value.trim()
  if (text === '') return
  history.value.push(text)
  historyIndex.value = -1
  line.value = ''
  // 默认模式必须经过 shell：否则 cd/管道/重定向都不生效，用户会以为命令没跑。
  await run([shell.value, '-c', text], text)
}

/** 自定义命令：一次执行整行，不进入循环输入 */
async function runCustom(): Promise<void> {
  if (running.value || disabled.value) return
  const text = customCommand.value.trim()
  if (text === '') {
    errorText.value = '请先填写要执行的命令'
    return
  }
  const { args, error } = splitCommandLine(text)
  if (error !== '') {
    errorText.value = `命令解析失败：${error}`
    return
  }
  if (args.length === 0) {
    errorText.value = '请先填写要执行的命令'
    return
  }
  errorText.value = ''
  forbidden.value = false
  await run(args, text)
}

/** ↑/↓ 翻命令历史，保留当前未提交的输入 */
function handleHistory(direction: 1 | -1): void {
  if (history.value.length === 0) return
  if (historyIndex.value === -1) {
    if (direction === -1) return
    historyIndex.value = history.value.length - 1
  } else {
    const next = historyIndex.value + direction
    if (next < 0) {
      historyIndex.value = 0
      return
    }
    if (next >= history.value.length) {
      historyIndex.value = -1
      line.value = ''
      return
    }
    historyIndex.value = next
  }
  line.value = history.value[historyIndex.value] ?? ''
}

/** 输入框回车：执行；上下键翻历史 */
function handleKeydown(event: KeyboardEvent): void {
  if (event.key === 'Enter' && !event.shiftKey) {
    event.preventDefault()
    void submitLine()
    return
  }
  if (event.key === 'ArrowUp') {
    event.preventDefault()
    handleHistory(-1)
    return
  }
  if (event.key === 'ArrowDown') {
    event.preventDefault()
    handleHistory(1)
  }
}
</script>

<template>
  <div class="console">
    <!--
      工具条：命令（shell 或自定义）、用户、工作目录。
      与 Portainer 的 Execute 面板同样的字段顺序，用户换工具时不用重新找。
    -->
    <div class="console__head">
      <div class="console__field">
        <span class="console__label">命令</span>
        <n-select
          v-if="!customMode"
          v-model:value="shell"
          :options="SHELL_OPTIONS"
          size="small"
          class="console__shell"
          :disabled="connected || running"
        />
        <n-input
          v-else
          v-model:value="customCommand"
          size="small"
          class="console__custom"
          placeholder="完整命令，例如 ls -la /data"
          :disabled="running"
          @keydown.enter.prevent="runCustom"
        />
      </div>

      <label class="console__switch">
        <n-switch v-model:value="customMode" size="small" :disabled="running" />
        <span>自定义命令</span>
      </label>

      <div class="console__field">
        <span class="console__label">用户</span>
        <n-input
          v-model:value="user"
          size="small"
          class="console__user"
          placeholder="留空 = 镜像默认"
          :disabled="running"
        />
      </div>

      <div class="console__field">
        <span class="console__label">工作目录</span>
        <n-input
          v-model:value="workdir"
          size="small"
          class="console__workdir"
          placeholder="留空 = 镜像默认"
          :disabled="running"
        />
      </div>
    </div>

    <div class="console__actions">
      <template v-if="!customMode">
        <n-button v-if="!connected" size="small" :disabled="disabled" @click="connect">开始</n-button>
        <n-button v-else size="small" quaternary :disabled="running" @click="disconnect">结束输入</n-button>
      </template>
      <n-button
        v-else
        size="small"
        type="primary"
        :disabled="disabled"
        :loading="running"
        @click="runCustom"
      >
        执行一次
      </n-button>
      <n-button size="small" quaternary :disabled="entries.length === 0" @click="clearTranscript">
        清屏
      </n-button>
      <n-tag v-if="forbidden" size="small" type="warning" :bordered="false">无执行权限</n-tag>
      <span v-if="!online" class="console__hint">服务器离线，无法执行命令。</span>
    </div>

    <n-alert v-if="errorText !== ''" type="error" :bordered="false" class="console__error">
      {{ errorText }}
    </n-alert>

    <!-- 转写区：不解释 ANSI 颜色，原样显示（宁可看到控制字符也不改内容） -->
    <div ref="scrollRef" class="console__screen">
      <p v-if="entries.length === 0" class="console__empty">
        点击「开始」后即可在下方输入命令。执行结果会显示在这里。
      </p>
      <template v-else>
        <div
          v-for="(entry, index) in entries"
          :key="index"
          :class="['console__line', `console__line--${entry.kind}`]"
        >{{ entry.text }}</div>
      </template>
    </div>

    <div v-if="!customMode" class="console__prompt">
      <span class="console__promptText">$</span>
      <n-input
        v-model:value="line"
        size="small"
        class="console__input"
        :placeholder="connected ? '输入命令，回车执行（↑/↓ 翻历史）' : '先点击「开始」'"
        :disabled="!canSubmit"
        @keydown="handleKeydown"
      />
      <n-button size="small" type="primary" :disabled="!canSubmit" :loading="running" @click="submitLine">
        执行
      </n-button>
    </div>

    <p class="console__note">
      每条命令都是独立进程：cd、export 等状态不保留，需要多条语句时写成一条（例如
      <code>cd /data &amp;&amp; ls</code>）。交互式程序（vi、top）不可用。
    </p>
  </div>
</template>

<style scoped>
.console {
  display: flex;
  flex-direction: column;
  gap: 10px;
}

.console__head {
  display: flex;
  align-items: center;
  gap: 12px;
  flex-wrap: wrap;
}

.console__field {
  display: flex;
  align-items: center;
  gap: 6px;
}

.console__label {
  font-size: 12px;
  color: var(--arkade-text-secondary);
  white-space: nowrap;
}

.console__shell {
  width: 190px;
}

.console__custom {
  width: 380px;
}

.console__user {
  width: 120px;
}

.console__workdir {
  width: 180px;
}

.console__switch {
  display: flex;
  align-items: center;
  gap: 6px;
  font-size: 12px;
  color: var(--arkade-text-secondary);
  cursor: pointer;
  user-select: none;
}

.console__actions {
  display: flex;
  align-items: center;
  gap: 8px;
  flex-wrap: wrap;
}

.console__hint {
  font-size: 12px;
  color: var(--arkade-text-tertiary);
}

.console__error {
  margin: 0;
}

.console__screen {
  margin: 0;
  padding: 10px 12px;
  min-height: 220px;
  max-height: 480px;
  overflow: auto;
  font-family: var(--arkade-mono-font);
  font-size: 12px;
  line-height: 1.6;
  background: var(--arkade-page-bg);
  border: 1px solid var(--arkade-border-color);
  border-radius: 4px;
}

.console__empty {
  margin: 0;
  color: var(--arkade-text-tertiary);
}

/* 每段输出保持原样换行；长行折行而不横向滚动，避免输出串丢失 */
.console__line {
  white-space: pre-wrap;
  word-break: break-all;
}

/* 输入的命令用次要色 + 前缀区分，避免与输出混在一起 */
.console__line--cmd {
  color: var(--arkade-text-secondary);
}

.console__line--err {
  color: var(--arkade-error-color, var(--arkade-danger));
}

/* 状态行（退出码/耗时/截断）用弱化样式，不抢输出内容的注意力 */
.console__line--note {
  color: var(--arkade-text-tertiary);
}

.console__prompt {
  display: flex;
  align-items: center;
  gap: 8px;
}

.console__promptText {
  font-family: var(--arkade-mono-font);
  font-size: 13px;
  color: var(--arkade-text-tertiary);
}

.console__input {
  flex: 1;
}

.console__note {
  margin: 0;
  font-size: 12px;
  line-height: 1.6;
  color: var(--arkade-text-tertiary);
}

.console__note code {
  font-family: var(--arkade-mono-font);
  padding: 1px 4px;
  border-radius: 3px;
  background: var(--arkade-surface-bg);
}
</style>
