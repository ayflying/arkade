/**
 * 外部（未纳管）compose 项目详情的数据层
 *
 * 拆出来的理由：详情页要同时管「项目+容器」「配置」「草稿」三份状态，
 * 且每份都有独立的 loading / error / 竞态，塞进 view 会把它撑成第二个 811 行。
 *
 * 权限现实：配置类接口要 manage，详情/容器要 view。
 * 所以「项目加载成功但配置 403」是完全正常的状态，本层不把它当失败。
 */
import { computed, onUnmounted, ref, watch } from 'vue'
import { ApiError } from '@/types/api'
import type { Container } from '@/types/container'
import type { ExternalStack } from '@/types/stack'
import type { ExternalStackDraft } from '@/types/externalStack'
import {
  getExternalStack,
  getExternalStackConfig,
  saveExternalDraft
} from '@/api/externalStacks'

interface Options {
  agentId: () => string
  name: () => string
  configOnly?: boolean
}

/** 配置区状态：区分「没权限」和「读不到」和「正常」 */
export type ConfigState = 'idle' | 'loading' | 'ready' | 'forbidden' | 'unreadable' | 'error'

export function useExternalStack(options: Options) {
  /* ── 项目 + 容器（view 权限） ── */
  const project = ref<ExternalStack | null>(null)
  const containers = ref<Container[]>([])
  const detailLoading = ref(false)
  const detailError = ref('')
  let detailSeq = 0

  /* ── 配置（manage 权限） ── */
  const configState = ref<ConfigState>('idle')
  const rawContent = ref('')
  const configHash = ref('')
  const blockers = ref<string[]>([])
  const draft = ref<ExternalStackDraft | null>(null)
  const configReason = ref('')
  const configError = ref('')
  /** 磁盘原文不可读、只能靠草稿支撑编辑时置 true */
  const draftOnly = ref(false)
  let configSeq = 0

  /* ── 草稿保存 ── */
  const savingDraft = ref(false)
  const draftError = ref('')

  async function loadDetail(): Promise<void> {
    const id = options.agentId()
    const nm = options.name()
    if (id === '' || nm === '') return
    const seq = ++detailSeq
    detailLoading.value = true
    detailError.value = ''
    try {
      const res = await getExternalStack(id, nm)
      if (seq !== detailSeq || id !== options.agentId() || nm !== options.name()) return
      project.value = res.project
      containers.value = res.containers
    } catch (err) {
      if (seq !== detailSeq || id !== options.agentId() || nm !== options.name()) return
      detailError.value = err instanceof Error ? err.message : '加载失败'
      project.value = null
      containers.value = []
    } finally {
      if (seq === detailSeq) detailLoading.value = false
    }
  }

  /**
   * 加载配置。
   *
   * 403 单独归类为 `forbidden` 而不是普通错误：
   * 用户看得到详情却读不到配置是**权限设计的正常结果**，
   * 提示要说「没有管理权限」而不是「加载失败」——后者会让人反复重试。
   *
   * ⚠️ 草稿与磁盘原文是**两个独立来源**：即使磁盘现在读不到
   * （文件被删、权限变更、路径不可读），已存在的 Hub 草稿也必须照常展示。
   * 草稿存在数据库里，它的价值正是「源码没了也还在」，
   * 读失败时把它丢掉等于毁掉用户唯一幸存的编辑内容。
   */
  async function loadConfig(): Promise<void> {
    const id = options.agentId()
    const nm = options.name()
    if (id === '' || nm === '') return
    const seq = ++configSeq
    configState.value = 'loading'
    configError.value = ''
    configReason.value = ''
    try {
      const res = await getExternalStackConfig(id, nm)
      if (seq !== configSeq || id !== options.agentId() || nm !== options.name()) return
      rawContent.value = res.content ?? ''
      configHash.value = res.config_hash ?? ''
      blockers.value = res.blockers ?? []
      draft.value = res.draft ?? null
      if (!res.readable) {
        configReason.value = res.reason ?? ''
        // 有草稿时仍然可以编辑草稿；只有连草稿都没有才算完全不可用
        draftOnly.value = res.draft !== undefined
        configState.value = draftOnly.value ? 'ready' : 'unreadable'
        return
      }
      draftOnly.value = false
      // 有 accessible 内容才算 ready；blockers 不影响可读性
      configState.value = 'ready'
    } catch (err) {
      if (seq !== configSeq || id !== options.agentId() || nm !== options.name()) return
      // 403 是权限，不是故障
      const forbidden = err instanceof ApiError && err.status === 403
      configState.value = forbidden ? 'forbidden' : 'error'
      if (forbidden) {
        configReason.value = '读取配置需要该服务器的管理权限。请联系管理员授权后再试。'
      } else {
        configError.value = err instanceof Error ? err.message : '配置加载失败'
      }
    }
  }

  /**
   * 保存草稿到 Hub。
   *
   * 只写 Hub，不碰服务器文件，因此这里**不需要**任何「会不会影响运行」的确认。
   * 返回是否成功，让调用方决定提示文案。
   */
  async function saveDraft(content: string, expectedVersion: number): Promise<boolean> {
    const id = options.agentId()
    const nm = options.name()
    if (id === '' || nm === '' || savingDraft.value) return false
    const seq = configSeq
    savingDraft.value = true
    draftError.value = ''
    try {
      const res = await saveExternalDraft(id, nm, {
        content,
        source_hash: configHash.value,
        expected_version: expectedVersion
      })
      if (seq !== configSeq || id !== options.agentId() || nm !== options.name()) return false
      draft.value = res.draft
      return true
    } catch (err) {
      if (seq !== configSeq || id !== options.agentId() || nm !== options.name()) return false
      // 409 = 别处已经改过草稿，要让人看到并重新加载，不能静默覆盖
      draftError.value = err instanceof Error ? err.message : '草稿保存失败'
      return false
    } finally {
      if (seq === configSeq) savingDraft.value = false
    }
  }

  /** 丢弃本地加载状态，用于路由变化 */
  function reset(): void {
    detailSeq++
    configSeq++
    project.value = null
    containers.value = []
    detailError.value = ''
    configState.value = 'idle'
    rawContent.value = ''
    configHash.value = ''
    blockers.value = []
    draft.value = null
    configReason.value = ''
    configError.value = ''
    draftError.value = ''
    draftOnly.value = false
    savingDraft.value = false
    detailLoading.value = false
  }

  const hasBlockers = computed(() => blockers.value.length > 0)
  /** 配置是否可读（决定编辑器是否可用） */
  const configReadable = computed(() => configState.value === 'ready')

  /**
   * 路由参数变了就重来一遍。
   *
   * Vue 会复用同一个组件实例（只有 params 变），onMounted 不会再跑。
   * 光清空状态不重新拉取的话，页面会停在空白——用户以为项目没数据。
   * 所以 reset 之后立刻按新参数加载，不引入任何轮询。
   */
  watch(
    () => [options.agentId(), options.name()],
    ([nextAgent, nextName], [prevAgent, prevName]) => {
      // 首次不算变化
      if (nextAgent === prevAgent && nextName === prevName) return
      reset()
      if (nextAgent === '' || nextName === '') return
      if (!options.configOnly) void loadDetail()
      void loadConfig()
    }
  )

  onUnmounted(reset)

  return {
    project,
    containers,
    detailLoading,
    detailError,
    loadDetail,
    configState,
    rawContent,
    configHash,
    blockers,
    draft,
    configReason,
    configError,
    draftOnly,
    loadConfig,
    savingDraft,
    draftError,
    saveDraft,
    hasBlockers,
    configReadable,
    reset
  }
}
