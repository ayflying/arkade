<script setup lang="ts">
/**
 * 容器详情页的「检查」面板
 *
 * ============================ 为什么单独一页 ============================
 *
 * 概览页把字段拆成给人看的标签（"内存上限：512 MiB"），
 * 这对日常判断够用，但排障时经常需要**整块复制**给同事或贴进工单。
 * 所以这里给一份完整、可复制的结构化清单，不再做二次解释。
 *
 * ============================ 显示的是白名单，不是 daemon 原文 ============================
 *
 * 后端刻意不下发 Env、全部 Labels、Secrets、Configs 与 daemon 原始 JSON
 * （环境变量里几乎必然有口令与连接串）。这里显示的字段与协议一致，
 * **不会**从 command 或镜像名反推环境变量——反推出来的内容既不可靠，
 * 也会让"看不到"变成"看起来能看到"，从而误导排障方向。
 */
import { computed } from 'vue'
import { NButton, NTooltip } from 'naive-ui'
import type { ContainerDetail } from '@/types/container'
import { copyToClipboard } from '@/utils/clipboard'
import { toastError, toastSuccess } from '@/utils/feedback'

interface Props {
  detail: ContainerDetail | null
  forbidden: boolean
}

const props = defineProps<Props>()

/**
 * 完整清单：直接序列化页面已拿到的详情对象。
 *
 * 排序保持协议字段顺序（不按字母重排）：字段顺序本身就是阅读顺序，
 * 重排会让"退出码在哪"这类定位每次都变。
 */
const prettyJson = computed<string>(() => {
  const d = props.detail
  if (d === null) return ''
  try {
    return JSON.stringify(d, null, 2)
  } catch {
    // 详情里全是原始类型，理论上不会抛；真抛了也不能让整页白屏。
    return ''
  }
})

/** 概览性的字段计数，让人一眼判断"这份清单是不是完整的" */
const fieldCount = computed<number>(() => {
  const d = props.detail
  if (d === null) return 0
  return Object.keys(d).length
})

async function handleCopy(): Promise<void> {
  if (prettyJson.value === '') return
  const ok = await copyToClipboard(prettyJson.value)
  if (ok) toastSuccess('已复制容器详情 JSON')
  else toastError('复制失败，请手动选择文本复制')
}
</script>

<template>
  <div class="inspect">
    <!-- 权限不足时说明为什么看不到，而不是留一片空白让人以为出错了 -->
    <p v-if="forbidden" class="inspect__hint">
      查看容器详情需要该服务器的管理权限。可切换到「日志」页查看输出。
    </p>

    <template v-else-if="detail === null">
      <p class="inspect__hint">暂无详情数据。请先用管理权限账号刷新本页。</p>
    </template>

    <template v-else>
      <div class="inspect__head">
        <span class="inspect__meta">{{ fieldCount }} 个字段 · 只读</span>
        <div class="inspect__actions">
          <n-tooltip trigger="hover">
            <template #trigger>
              <n-button size="small" quaternary @click="handleCopy">复制 JSON</n-button>
            </template>
            复制完整的容器详情清单，便于贴进工单
          </n-tooltip>
        </div>
      </div>

      <!-- 用 pre 保留缩进：这一页的价值就是"可读的原始结构" -->
      <pre class="inspect__box">{{ prettyJson }}</pre>

      <p class="inspect__note">
        清单里不含环境变量（Env）、全部 Labels、Secrets 与 Configs：这些字段常含
        口令与连接串，后端刻意不下发。需要确认环境变量请用「控制台」在容器内自行查看。
      </p>
    </template>
  </div>
</template>

<style scoped>
.inspect {
  display: flex;
  flex-direction: column;
  gap: 10px;
}

.inspect__head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 10px;
  flex-wrap: wrap;
}

.inspect__meta {
  font-size: 12px;
  color: var(--arkade-text-tertiary);
}

.inspect__actions {
  display: flex;
  align-items: center;
  gap: 8px;
}

.inspect__box {
  margin: 0;
  padding: 12px;
  max-height: 560px;
  overflow: auto;
  font-family: var(--arkade-mono-font);
  font-size: 12px;
  line-height: 1.6;
  white-space: pre;
  background: var(--arkade-page-bg);
  border: 1px solid var(--arkade-border-color);
  border-radius: 4px;
}

.inspect__hint {
  margin: 0;
  font-size: 13px;
  color: var(--arkade-text-tertiary);
}

.inspect__note {
  margin: 0;
  font-size: 12px;
  line-height: 1.6;
  color: var(--arkade-text-tertiary);
}
</style>
