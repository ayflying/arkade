/**
 * 存储卷文件管理：数据与操作逻辑
 *
 * 从 VolumeFilesView 抽出来的部分——目录加载、文件读写、增删改。
 *
 * ## 抽出来的理由
 *
 * 页面里混着「请求怎么发」和「长什么样」两件事，改一处要读几百行才能确认
 * 有没有碰到另一半。分出来之后：
 * - 这里只关心状态与请求，不含任何模板与样式；
 * - 视图只关心渲染与交互，不含请求细节；
 * - 请求逻辑可单独测，不必挂载组件。
 *
 * ## 两条必须守住的语义
 *
 * 1. **过期响应**：目录加载带请求序号，先发后到的旧响应必须丢弃，
 *    否则连点两个目录时界面会跳回去。
 * 2. **写冲突**：保存必须带 read 时拿到的 sha256。冲突（409）时
 *    **不清空用户草稿**——清掉等于把刚写的也丢了。
 */

import { ref } from 'vue'
import type { Ref } from 'vue'
import {
  deleteVolumeEntry,
  listVolumeFiles,
  mkdirVolume,
  readVolumeFile,
  renameVolumeEntry,
  writeVolumeFile
} from '@/api/volumeFiles'
import type { VolumeEntry, VolumeFileResult } from '@/types/volumeFile'
import { VOLUME_FILE_MAX_BYTES } from '@/types/volumeFile'
import { bytesToBase64, textToBase64 } from '@/utils/base64'
import { formatBytes } from '@/utils/formatSize'

/** 拼出完整相对路径 */
export function joinPath(dir: string, name: string): string {
  return dir === '' ? name : `${dir}/${name}`
}

/** 取路径最后一段（文件名） */
export function baseName(path: string): string {
  const segments = path.split('/').filter((s) => s !== '')
  return segments[segments.length - 1] ?? ''
}

/**
 * 排序：目录在前，再按名称本地化比较。
 *
 * 不依赖后端顺序——readdir 的顺序随文件系统变化，
 * 每次刷新顺序不同会让用户找不到刚看过的文件。
 */
function sortEntries(list: VolumeEntry[]): VolumeEntry[] {
  return list.slice().sort((a, b) => {
    const aDir = a.kind === 'directory' ? 0 : 1
    const bDir = b.kind === 'directory' ? 0 : 1
    if (aDir !== bDir) return aDir - bDir
    return a.name.localeCompare(b.name, 'zh-Hans-CN')
  })
}

export interface UseVolumeFilesOptions {
  /** 当前服务器 id */
  agentId: Ref<string | null>
  /** 当前卷名 */
  volume: Ref<string>
  /** 当前目录相对路径 */
  currentPath: Ref<string>
}

export interface UseVolumeFilesReturn {
  entries: Ref<VolumeEntry[]>
  listLoading: Ref<boolean>
  listError: Ref<string>
  loadEntries: () => Promise<void>

  fileResult: Ref<VolumeFileResult | null>
  fileLoading: Ref<boolean>
  fileSaving: Ref<boolean>
  fileError: Ref<string>
  /** 当前打开的文件相对路径；空串表示没打开 */
  openFilePath: Ref<string>
  /** 读到的文件是否超限（超限时**不提供下载**，因为后端 read 本身已拒绝） */
  fileTooLarge: Ref<boolean>
  readFile: (path: string) => Promise<void>
  /** 重试读取当前文件 */
  retryRead: () => Promise<void>
  closeFile: () => void
  /**
   * 作废进行中的文件请求。
   *
   * 路由变化与组件卸载时必须调用，否则迟到的响应会写进已换上下文的状态。
   */
  invalidateFileRequests: () => void
  /**
   * 保存；返回成功写入的**目标路径**，失败返回 null。
   * 返回路径而非布尔，便于视图校验它是否仍是当前打开的文件。
   */
  saveFile: (content: string, expectedSha256: string) => Promise<string | null>

  createFile: (name: string) => Promise<void>
  createDir: (name: string) => Promise<void>
  /**
   * 上传本地文件到当前目录。
   *
   * 一律 `create_only`：同名已存在则报错，不覆盖。
   * 超过 4 MiB 或为空文件时**在读取内容之前**就抛错。
   */
  uploadCreate: (file: File) => Promise<void>
  rename: (fromPath: string, toPath: string) => Promise<void>
  remove: (path: string) => Promise<void>

  /** 正在操作的条目名（行内 loading） */
  busyName: Ref<string>
}

/**
 * 创建卷文件管理状态。
 *
 * 所有方法在失败时**抛出**（由调用方决定怎么展示），
 * 只有 saveFile 返回布尔——因为它要区分「成功」与「冲突但草稿保留」。
 */
export function useVolumeFiles(options: UseVolumeFilesOptions): UseVolumeFilesReturn {
  const { agentId, volume, currentPath } = options

  const entries = ref<VolumeEntry[]>([])
  const listLoading = ref(false)
  const listError = ref('')

  const fileResult = ref<VolumeFileResult | null>(null)
  const fileLoading = ref(false)
  const fileSaving = ref(false)
  const fileError = ref('')
  const openFilePath = ref('')
  /**
   * 超限标记。
   *
   * 单独一个 ref 而不是从 result 推导：超限时我们**不保留内容**
   * （避免几 MB base64 挂在内存里），此时 result 可能为空，
   * 视图仍需要知道「这是超限，不是读取失败」才能给出正确提示。
   */
  const fileTooLarge = ref(false)

  const busyName = ref('')

  /** 请求序号：丢弃过期响应 */
  let listSeq = 0
  /**
   * 文件读取序号。
   *
   * 与 listSeq 分开：目录和文件是两条独立的异步流，
   * 共用计数器会导致「刷新目录」意外作废正在进行的文件读取。
   */
  let readSeq = 0

  /**
   * 作废所有进行中的文件请求。
   *
   * 三个时机都要调用，否则迟到的响应会写进已经换了上下文的状态：
   * - 关闭面板
   * - 路由变化（切目录/切卷）
   * - 组件卸载
   *
   * 只自增计数器而不 abort：请求本身很小，让它自然结束即可；
   * 关键是它的**结果不再被采用**。
   */
  function invalidateFileRequests(): void {
    readSeq += 1
    fileLoading.value = false
  }

  /**
   * 载入目录。
   *
   * 注意即使抛错也不把错误抛给调用方：目录加载失败是页面级状态，
   * 由 listError 展示 + 重试按钮处理，不需要每个调用点 try/catch。
   */
  async function loadEntries(): Promise<void> {
    if (agentId.value === null || volume.value === '') return
    const seq = ++listSeq
    listLoading.value = true
    listError.value = ''
    try {
      const result = await listVolumeFiles(agentId.value, volume.value, currentPath.value)
      // 过期响应直接丢弃
      if (seq !== listSeq) return
      entries.value = sortEntries(result.entries ?? [])
    } catch (err) {
      if (seq !== listSeq) return
      entries.value = []
      listError.value = err instanceof Error ? err.message : '加载目录失败'
    } finally {
      if (seq === listSeq) listLoading.value = false
    }
  }

  /**
   * 读取文件内容。
   *
   * ## 过期响应必须丢弃
   *
   * 用户点 A 再点 B，A 的响应如果后到，会把面板刷成 A 的内容而路径显示 B——
   * 此时用户看着 B 的路径编辑 A 的内容，一保存就**写错文件**。
   * 所以每次读取自增 `readSeq`，只有最新一次的响应允许落进状态。
   * 与 `listSeq` 同一套模式，但文件读错的后果比目录显示错更严重。
   *
   * 超过 4 MiB 时**丢掉内容**：后端 read 对超限文件直接拒绝，
   * 没有单独流式下载端点，留着内容只会让视图给出必然失败的下载按钮。
   */
  async function readFile(path: string): Promise<void> {
    if (agentId.value === null) return
    const seq = ++readSeq
    openFilePath.value = path
    fileResult.value = null
    fileError.value = ''
    fileTooLarge.value = false
    fileLoading.value = true
    try {
      const result = await readVolumeFile(agentId.value, volume.value, path)
      // 过期响应：不写任何状态，连 loading 都不动（最新那次会负责收尾）
      if (seq !== readSeq) return
      if (result.size !== undefined && result.size > VOLUME_FILE_MAX_BYTES) {
        // 只保留元信息，不留内容
        fileTooLarge.value = true
        fileResult.value = { path: result.path, sha256: result.sha256, size: result.size }
      } else {
        fileResult.value = result
      }
    } catch (err) {
      if (seq !== readSeq) return
      fileError.value = err instanceof Error ? err.message : '读取文件失败'
    } finally {
      if (seq === readSeq) fileLoading.value = false
    }
  }

  /** 重试读取当前文件 */
  async function retryRead(): Promise<void> {
    if (openFilePath.value === '') return
    const path = openFilePath.value
    await readFile(path)
  }

  /** 关闭文件面板（不处理未保存确认，那由视图负责） */
  function closeFile(): void {
    // 先作废：关掉之后迟到的 read 响应不能再把面板填回来
    invalidateFileRequests()
    fileResult.value = null
    fileError.value = ''
    openFilePath.value = ''
    fileTooLarge.value = false
  }

  /**
   * 保存。
   *
   * ## 为什么要把 path 和令牌一起固定住
   *
   * 保存是异步的，等待期间用户可能已经切到别的文件。
   * 如果用「await 之后读一次 openFilePath.value」来构造结果，
   * 就会把 A 的保存结果标到 B 的路径上——界面显示 B 已保存，
   * 实际 B 根本没动过。所以：
   *
   * 1. 进入时**立刻快照**目标路径与读取序号；
   * 2. 写入用的就是这个快照路径，不受后续导航影响；
   * 3. 回来后只有「路径没变 且 读取序号没变」才更新面板状态，
   *    否则这次保存对当前界面已无意义（用户看着的是别的文件）。
   *
   * @returns 保存成功的**目标路径**；失败返回 null（错误已写入 fileError）。
   *          返回路径而不是布尔，是为了让视图校验它是否仍指向当前文件。
   */
  async function saveFile(content: string, expectedSha256: string): Promise<string | null> {
    if (agentId.value === null || openFilePath.value === '') return null
    // 快照：后续所有判断都基于这两个值，不重读响应式状态
    const targetPath = openFilePath.value
    const savedSeq = readSeq
    fileSaving.value = true
    fileError.value = ''
    try {
      const result = await writeVolumeFile(agentId.value, volume.value, {
        path: targetPath,
        dataBase64: textToBase64(content),
        // 乐观锁：后端比对不符会 409，避免静默覆盖他人修改
        expectedSha256
      })
      // 只在「仍是同一个文件、且期间没重新读取」时才更新面板基线
      if (readSeq === savedSeq && openFilePath.value === targetPath) {
        fileResult.value = {
          ...(fileResult.value ?? { path: targetPath }),
          path: targetPath,
          sha256: result.sha256,
          size: result.size
        }
      }
      // 大小/修改时间变了，刷新目录
      void loadEntries()
      return targetPath
    } catch (err) {
      // 失败时同样只在同一文件上回填错误，避免把 A 的错误挂到 B 上
      if (readSeq === savedSeq && openFilePath.value === targetPath) {
        fileError.value = err instanceof Error ? err.message : '保存失败'
      }
      return null
    } finally {
      fileSaving.value = false
    }
  }

  /** 新建空文件（create_only，绝不覆盖同名） */
  async function createFile(name: string): Promise<void> {
    if (agentId.value === null) return
    await writeVolumeFile(agentId.value, volume.value, {
      path: joinPath(currentPath.value, name),
      dataBase64: textToBase64(''),
      createOnly: true
    })
    void loadEntries()
  }

  /** 新建目录 */
  async function createDir(name: string): Promise<void> {
    if (agentId.value === null) return
    await mkdirVolume(agentId.value, volume.value, joinPath(currentPath.value, name))
    void loadEntries()
  }

  /**
   * 上传文件：读取本地文件内容并写入卷。
   *
   * ## 为什么必须 `create_only: true`
   *
   * 上传没有「我读到过的旧内容」可以比对，拿不出 expected_sha256。
   * 如果允许覆盖，就等于用本地文件**无条件冲掉**服务器上的现有文件，
   * 而那份文件可能是别人刚改过的、或者是正在被容器写入的。
   * 所以这里一律 create_only：同名已存在就报错，由用户自己先删除再传。
   *
   * ## 大小检查必须在读文件之前
   *
   * 4 MiB 是契约硬限制。先 `file.arrayBuffer()` 再判断大小的话，
   * 用户选了一个 2 GB 的文件会先把内存打满才被拒绝。所以先看 `file.size`。
   *
   * ## 二进制不做任何转码
   *
   * 图片、压缩包、SQLite 都必须原样写入。这里只做 base64 编码，
   * 不经过字符串解码，避免把非法字节变成 U+FFFD 损坏内容。
   */
  async function uploadCreate(file: File): Promise<void> {
    if (agentId.value === null) return

    /*
     * 0) 立刻快照目标上下文。
     *
     * `file.arrayBuffer()` 是异步的，读大文件期间用户完全可能切到另一个卷
     * 或另一个目录。若等 await 回来再读 agentId/volume/currentPath，
     * 写的就是**另一个卷**——文件出现在用户没预期的位置，且因为 create_only
     * 还会在那卷里凭空多出一个文件。所以三个目标值必须在进函数时定死。
     */
    const targetAgentId = agentId.value
    const targetVolume = volume.value
    const targetPath = joinPath(currentPath.value, file.name)
    const targetDir = currentPath.value

    /*
     * 1) 先按元数据拦截，不读内容。
     *    只拦超上限：0 字节是合法文件（后端支持，用户也能下载空的），
     *    不该拒；0 字节的 arrayBuffer 也正常返回空。
     */
    if (file.size > VOLUME_FILE_MAX_BYTES) {
      throw new Error(
        `文件大小 ${formatBytes(file.size)}，超过上传上限 ${formatBytes(VOLUME_FILE_MAX_BYTES)}`
      )
    }

    // 2) 读字节 → base64（原样，不解码成文本）
    const buffer = await file.arrayBuffer()
    const base64 = bytesToBase64(new Uint8Array(buffer))

    // 3) create_only：绝不覆盖同名文件；用快照路径，不受期间导航影响
    await writeVolumeFile(targetAgentId, targetVolume, {
      path: targetPath,
      dataBase64: base64,
      createOnly: true
    })

    /*
     * 4) 只在用户仍停在同一上下文时才刷新列表。
     *    已经切走了就别去刷他现在看的那个目录——那是另一份数据，
     *    刷了反而让人以为刚上传的文件出现在那里。
     */
    if (agentId.value === targetAgentId && volume.value === targetVolume && currentPath.value === targetDir) {
      void loadEntries()
    }
  }

  /** 重命名 / 移动（后端拒绝覆盖已存在目标） */
  async function rename(fromPath: string, toPath: string): Promise<void> {
    if (agentId.value === null) return
    busyName.value = baseName(fromPath)
    try {
      await renameVolumeEntry(agentId.value, volume.value, fromPath, toPath)
      void loadEntries()
    } finally {
      busyName.value = ''
    }
  }

  /** 删除单文件或空目录 */
  async function remove(path: string): Promise<void> {
    if (agentId.value === null) return
    busyName.value = baseName(path)
    try {
      await deleteVolumeEntry(agentId.value, volume.value, path)
      void loadEntries()
    } finally {
      busyName.value = ''
    }
  }

  return {
    entries,
    listLoading,
    listError,
    loadEntries,
    fileResult,
    fileLoading,
    fileSaving,
    fileError,
    openFilePath,
    fileTooLarge,
    readFile,
    retryRead,
    closeFile,
    invalidateFileRequests,
    saveFile,
    createFile,
    createDir,
    uploadCreate,
    rename,
    remove,
    busyName
  }
}
