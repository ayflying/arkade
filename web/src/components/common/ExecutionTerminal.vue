<script setup lang="ts">
import { computed, nextTick, onMounted, shallowRef, watch } from 'vue'
import { stripAnsi, type OutputSegment } from '@/utils/stackOperationOutput'
import { highlightTerminalCommand } from '@/utils/terminalCommand'

const props = defineProps<{
  segments: readonly OutputSegment[]
  currentCommand?: string
  emptyHint: string
  label: string
  active: boolean
  /**
   * 执行结束横幅（在日志窗口底部）。
   *
   * 传 null 表示"还没有结论"（排队中/执行中/状态未知），此时**什么都不显示**：
   * 提前显示"完成"是最容易误导人的一种"进度提示"。
   *
   * 由调用方根据真实终态计算，本组件不从输出文本里猜。
   */
  completion?: { text: string; ok: boolean } | null
}>()

const viewport = shallowRef<HTMLElement | null>(null)

const steps = computed(() => {
  let step = 0
  return props.segments.map((segment) => ({
    number: segment.command === null ? null : ++step,
    command: segment.command === null ? null : highlightTerminalCommand(stripAnsi(segment.command)),
    chunks: segment.chunks.map((chunk) => {
      const text = stripAnsi(chunk.text)
      return { seq: chunk.seq, text, tone: outputTone(chunk.stream, text) }
    })
  }))
})

// 任务快照可能先于命令事件到达；补显示，但不把未收到的步骤编号编出来。
const pendingCommand = computed(() => {
  const command = stripAnsi(props.currentCommand ?? '')
  const lastCommand = props.segments.at(-1)?.command
  return command && command !== stripAnsi(lastCommand ?? '') ? highlightTerminalCommand(command) : null
})

function followLatest(): void {
  const el = viewport.value
  if (el !== null) el.scrollTop = el.scrollHeight
}

// 更新前判定是否贴底，更新后再滚动。监听整个输出，行数触顶后仍能跟随。
watch([() => props.segments, pendingCommand], async () => {
  const el = viewport.value
  if (!props.active || el === null || el.scrollHeight - el.scrollTop - el.clientHeight > 24) return
  await nextTick()
  if (props.active) followLatest()
})
watch(() => props.active, (active) => {
  if (active) followLatest()
}, { flush: 'post' })
onMounted(followLatest)

function outputTone(stream: string, text: string): string {
  // Compose 也会把普通进度写到 stderr，因此不用 stderr 判定失败。
  if (stream === 'stage') return 'stage'
  if (/^\s*(?:error\b|fatal\b|failed\b)|\b(?:level=error|Error response from daemon)\b/i.test(text)) return 'error'
  if (/^\s*(?:warn(?:ing)?\b)|\blevel=warn(?:ing)?\b/i.test(text)) return 'warning'
  return stream === 'stderr' ? 'diagnostic' : 'normal'
}
</script>

<template>
  <div class="terminal">
    <div class="terminal__bar" aria-hidden="true">
      <span class="terminal__glyph">&gt;_</span> {{ label }}
    </div>
    <div
      ref="viewport"
      class="terminal__viewport"
      role="log"
      aria-live="polite"
      :aria-label="label"
      tabindex="0"
    >
      <p v-if="steps.length === 0 && emptyHint" class="terminal__empty">{{ emptyHint }}</p>
      <div v-for="(step, index) in steps" :key="index" class="terminal__step">
        <div v-if="step.command !== null" class="terminal__heading">
          <span class="terminal__step-no">第 {{ step.number }} 步</span>
          <code class="terminal__command"><span class="terminal__prompt" aria-hidden="true">$ </span><span v-for="(token, i) in step.command" :key="i" :class="`terminal__token--${token.kind}`">{{ token.text }}</span></code>
        </div>
        <!-- 按事件原序展示，避免阶段说明被提前到真正发生的输出前面。 -->
        <pre v-for="line in step.chunks" :key="line.seq" class="terminal__line" :class="`terminal__line--${line.tone}`">{{ line.text }}</pre>
      </div>
      <div v-if="pendingCommand" class="terminal__heading terminal__heading--pending">
        <span class="terminal__step-no">当前命令</span>
        <code class="terminal__command"><span v-for="(token, i) in pendingCommand" :key="i" :class="`terminal__token--${token.kind}`">{{ token.text }}</span></code>
      </div>
    </div>
    <!--
      结束横幅：告诉用户"命令都跑完了，可以关了"。
      放在日志窗口底部而不是弹窗里，是因为用户的目光就停在日志末尾——
      目标参照的产品也是这么做的。role=status 让读屏软件也能读出结论。
    -->
    <p
      v-if="completion"
      class="terminal__completion"
      :class="completion.ok ? 'terminal__completion--ok' : 'terminal__completion--failed'"
      role="status"
    >
      {{ completion.text }}
    </p>
  </div>
</template>

<style scoped>
.terminal {
  --terminal-fg: #dce4e9;
  --terminal-muted: #9baab5;
  --terminal-frame: #2b363e;
  --terminal-mint: #8bd9b6;
  min-width: 0;
  color-scheme: dark;
  color: var(--terminal-fg);
  background: #080b0e;
  border: 1px solid var(--terminal-frame);
  border-radius: 8px;
  overflow: hidden;
  font: 12px/1.6 var(--arkade-mono-font, monospace);
}

.terminal__bar {
  padding: 6px 12px;
  background: #131a20;
  border-bottom: 1px solid var(--terminal-frame);
  color: var(--terminal-muted);
  overflow-wrap: anywhere;
}

.terminal__glyph {
  margin-inline-end: 6px;
  color: var(--terminal-mint);
  font-weight: 700;
}

.terminal__viewport {
  height: min(288px, 34vh);
  box-sizing: border-box;
  padding: 10px 12px;
  overflow: auto;
}

.terminal__viewport:focus-visible {
  outline: 2px solid var(--terminal-mint);
  outline-offset: -2px;
}

.terminal__step + .terminal__step,
.terminal__step + .terminal__heading--pending {
  margin-top: 8px;
  padding-top: 8px;
  border-top: 1px solid var(--terminal-frame);
}

.terminal__heading {
  display: flex;
  flex-wrap: wrap;
  align-items: baseline;
  gap: 0 10px;
  padding: 4px 7px;
  margin-bottom: 5px;
  background: #131a20;
  border-inline-start: 2px solid var(--terminal-mint);
}

.terminal__step-no {
  flex: none;
  color: var(--terminal-muted);
  font-size: 11px;
}

.terminal__command,
.terminal__line {
  min-width: 0;
  margin: 0;
  font: inherit;
  white-space: pre-wrap;
  overflow-wrap: anywhere;
}

.terminal__command {
  flex: 1 1 16ch;
}

.terminal__prompt,
.terminal__token--executable {
  color: var(--terminal-mint);
  font-weight: 700;
}

.terminal__token--option { color: #9ecbff; }
.terminal__token--value,
.terminal__line--warning { color: #f0cb8e; }
.terminal__line--error { color: #ffaaa8; }
.terminal__line--diagnostic { color: #bac7d3; }

.terminal__line { min-height: 1lh; }

.terminal__line--stage {
  color: var(--terminal-muted);
  font-style: italic;
}

.terminal__empty {
  margin: 0 0 5px;
  color: var(--terminal-muted);
}

/* 结束横幅：与目标参照产品一致的"绿条成功 / 红条失败"观感。
   不用 n-alert：它属于浅色主题，放在深色终端里会像两个不同产品拼在一起。 */
.terminal__completion {
  margin: 0;
  padding: 8px 12px;
  border-top: 1px solid var(--terminal-frame);
  font-size: 12px;
  line-height: 1.6;
  font-weight: 600;
}

.terminal__completion--ok {
  color: #0a2a1d;
  background: var(--terminal-mint);
}

.terminal__completion--failed {
  color: #2c0b0b;
  background: #ffaaa8;
}
</style>
