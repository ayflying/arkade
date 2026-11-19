import { ref } from 'vue'
import { retryExternalAdopt } from '@/api/externalStacks'
import { toastError, toastSuccess } from '@/utils/feedback'

/**
 * 「重试自动接管」按钮的状态与动作。
 *
 * 为什么需要这个按钮：后台自动发现对两类结论**不再自动重扫**——
 * 确定性阻塞（多配置文件、工作目录不合规、符号链接…）和重试次数用尽的环境故障。
 * 不重扫是为了不每次失败都创建一个临时 helper 容器；
 * 但用户把问题修好之后，必须有个地方能立刻再来一次，
 * 否则就只能删了重建，或者等 Hub 重启。
 *
 * 触发后只是清掉结论，真正的扫描仍由后台下一轮（约 30 秒内）执行：
 * 这样不会因为连点按钮而产生并发读取。
 *
 * 参数用取值函数而不是字符串：agentId 是异步加载的，
 * 在 setup 时直接取会拿到空串，按钮就会永远点不动。
 */
export function useAutoAdoptRetry(
  target: { agentId: () => string; stackName: () => string },
  done: () => void
) {
  const retrying = ref(false)

  async function retry(): Promise<void> {
    const agentId = target.agentId()
    const name = target.stackName()
    if (agentId === '' || name === '' || retrying.value) return
    retrying.value = true
    try {
      const res = await retryExternalAdopt(agentId, name)
      toastSuccess(res.message === '' ? '已安排重新扫描该项目' : res.message)
      done()
    } catch (err) {
      // 权限不足（403）会原样透出后端的说明，不在这里另编一句含糊的提示。
      toastError(err instanceof Error && err.message !== '' ? err.message : '重试失败')
    } finally {
      retrying.value = false
    }
  }

  return { retrying, retry }
}
