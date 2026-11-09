<script setup lang="ts">
/**
 * 「添加服务器」说明弹窗
 *
 * ## 为什么这里是说明而不是表单
 *
 * Arkade 的 Agent 是**主动向外连接** Hub 的：Agent 部署在目标服务器上，
 * 启动时带着 Hub 地址和令牌去连 Hub，连上后自己注册。
 * 因此 Hub **没有任何办法**主动去连一台服务器（那台机器可能在内网、
 * 没有公网 IP、也没有开放端口）。
 *
 * 这一点如果不说清，用户会一直找「添加服务器」的按钮，
 * 甚至以为功能没做完。所以入口做成「说明」而不是「新增表单」，
 * 并在文案里把「为什么不能直接加」讲明白。
 *
 * 说明分三步给出：在那台机器上装 Agent → 配 Hub 地址与令牌 → 启动后自动出现。
 */
import { NAlert, NButton, NModal, NStep, NSteps } from 'naive-ui'

interface Props {
  show: boolean
}

defineProps<Props>()

const emit = defineEmits<{
  (e: 'update:show', value: boolean): void
}>()

/**
 * 供用户复制的示例启动命令。
 *
 * 这里刻意用占位符而不是真实地址：真实 Hub 地址因部署而异，
 * 写死一个会让用户直接复制到错误的值。
 */
const EXAMPLE_COMMAND =
  'arkade-agent \\\n  --hub-url https://你的-Hub-地址 \\\n  --token <在 Hub 生成的接入令牌>'
</script>

<template>
  <n-modal
    :show="show"
    preset="card"
    title="如何添加一台服务器"
    :style="{ maxWidth: '680px', width: '92vw' }"
    :bordered="false"
    @update:show="(v: boolean) => emit('update:show', v)"
  >
    <!--
      第一段就把最关键的概念点破：Agent 主动连出来，Hub 不能主动连进去。
      放在最前面，是因为用户来这里往往正是想找「新增」按钮。
    -->
    <n-alert type="info" :bordered="false" class="add-agent__lead">
      Arkade <strong>没有「直接添加服务器」的功能</strong>，这是设计如此，不是缺少功能。
      <br />
      服务器上的 Agent 会<strong>主动向外连接</strong> Hub，连接成功后自动完成注册。
      Hub 无法反向连接内网里的服务器，因此只能由服务器这一侧发起。
    </n-alert>

    <n-steps vertical :current="4" size="small" class="add-agent__steps">
      <n-step
        title="在目标服务器上部署 Agent"
        description="把 Agent 程序放到那台服务器上（支持 Docker / 二进制两种方式），它需要能访问到 Hub 的地址。"
      />
      <n-step
        title="配置 Hub 地址与接入令牌"
        description="在 Agent 的配置文件或启动参数中填入本 Hub 的地址，以及一个有效的接入令牌。"
      >
        <pre class="add-agent__code">{{ EXAMPLE_COMMAND }}</pre>
      </n-step>
      <n-step
        title="启动 Agent"
        description="Agent 启动后会立即连接 Hub 并上报自身信息（名称、引擎、版本、容器列表）。"
      />
      <n-step
        title="回到这里查看"
        description="服务器会自动出现在「服务器」列表中，无需手动添加。状态变为「在线」后即可开始管理容器。"
      />
    </n-steps>

    <n-alert type="warning" :bordered="false" class="add-agent__tip">
      如果 Agent 启动后没有出现在列表里，请检查：Agent 所在机器能否访问 Hub 地址、
      令牌是否有效（可在 Hub 的令牌设置中确认）、以及两边时间是否同步。
    </n-alert>

    <template #footer>
      <div class="add-agent__footer">
        <n-button size="small" type="primary" @click="emit('update:show', false)">知道了</n-button>
      </div>
    </template>
  </n-modal>
</template>

<style scoped>
.add-agent__lead {
  margin-bottom: 16px;
  line-height: 1.7;
}

.add-agent__steps {
  /* 长描述在窄屏下必须能换行，否则会把弹窗撑破 */
  min-width: 0;
}

.add-agent__code {
  margin: 8px 0 0;
  padding: 10px 12px;
  background: #1e2430;
  color: #d8dee9;
  border-radius: 6px;
  font-family: var(--arkade-mono-font);
  font-size: 12px;
  line-height: 1.6;
  /* 命令行可能超出宽度：允许横向滚动而不是撑破弹窗 */
  overflow-x: auto;
  white-space: pre;
}

.add-agent__tip {
  margin-top: 8px;
  line-height: 1.7;
}

.add-agent__footer {
  display: flex;
  justify-content: flex-end;
}
</style>
