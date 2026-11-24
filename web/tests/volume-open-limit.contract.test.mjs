/**
 * 存储卷文件「在线查看上限」契约测试
 *
 * ## 这条契约在守什么
 *
 * 超过 2 MiB 的文件**不在浏览器里打开**，只允许下载：
 * - 打开它要占住 Agent 侧一个并发槽位（同时最多 2 个）、等一次完整的
 *   helper 读取和几 MB 传输，而这类文件（日志、导出数据、数据库）本来就该
 *   下载到本地用合适的工具看；
 * - 但它的内容仍必须能下载——下载本身就是一次 read，上限是单文件上限 4 MiB。
 *
 * 所以两个阈值不能合并：
 * - 2 MiB：只影响「给不给编辑器」；
 * - 4 MiB：read 的硬上限，超过它连下载也不可用。
 *
 * ## 为什么既测行为又测源码
 *
 * 判定函数可以直接跑（真行为）；而"点开文件时不发起读取""面板把下载按钮
 * 留给仅下载态"属于接线，没有组件运行时可用，用源码契约守住。
 */

import test from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { fileURLToPath, pathToFileURL } from 'node:url'
import { dirname, join } from 'node:path'
import { registerHooks } from 'node:module'

const here = dirname(fileURLToPath(import.meta.url))
const srcDir = join(here, '..', 'src')

registerHooks({
  resolve(specifier, context, nextResolve) {
    if (specifier.startsWith('@/')) {
      return { url: pathToFileURL(join(srcDir, specifier.slice(2) + '.ts')).href, shortCircuit: true }
    }
    if (specifier.startsWith('.') && context.parentURL !== undefined && !/\.[a-z]+$/.test(specifier)) {
      const candidate = new URL(specifier + '.ts', context.parentURL)
      if (candidate.href.startsWith(pathToFileURL(srcDir).href)) {
        return { url: candidate.href, shortCircuit: true }
      }
    }
    return nextResolve(specifier, context)
  }
})

const failures = []

function check(label, condition, detail) {
  if (condition) return
  failures.push(detail === undefined ? label : `${label}：${detail}`)
}

const types = await import('../src/types/volumeFile.ts')
const { VOLUME_FILE_MAX_BYTES, VOLUME_FILE_OPEN_MAX_BYTES, volumeFileViewLevel } = types

const MiB = 1024 * 1024

/* ───────── 1. 常量与边界 ───────── */

check('在线查看上限应为 2 MiB', VOLUME_FILE_OPEN_MAX_BYTES === 2 * MiB, String(VOLUME_FILE_OPEN_MAX_BYTES))
check('单文件上限应为 4 MiB', VOLUME_FILE_MAX_BYTES === 4 * MiB, String(VOLUME_FILE_MAX_BYTES))
check('查看上限必须小于单文件上限', VOLUME_FILE_OPEN_MAX_BYTES < VOLUME_FILE_MAX_BYTES)

check('0 字节可在线打开', volumeFileViewLevel(0) === 'full')
check('恰好 2 MiB 仍可在线打开（边界含等号）', volumeFileViewLevel(2 * MiB) === 'full')
check('2 MiB + 1 字节只允许下载', volumeFileViewLevel(2 * MiB + 1) === 'download')
check('3 MiB 只允许下载', volumeFileViewLevel(3 * MiB) === 'download')
check('恰好 4 MiB 仍可下载（read 上限含等号）', volumeFileViewLevel(4 * MiB) === 'download')
check('4 MiB + 1 字节拒绝打开与下载', volumeFileViewLevel(4 * MiB + 1) === 'blocked')
check('大小未知时不擅自拒绝', volumeFileViewLevel(undefined) === 'full')

/* ───────── 2. store：不信列表、按实际大小再判 ───────── */

const storeSrc = readFileSync(join(srcDir, 'composables', 'useVolumeFiles.ts'), 'utf8')

check(
  'store: 打开文件走统一判定函数',
  /knownSize !== undefined && volumeFileViewLevel\(knownSize\) !== 'full'/.test(storeSrc)
)
check(
  'store: 超限时不得发起读取（先判定再 readFile）',
  (() => {
    const openIdx = storeSrc.indexOf('async function openFile')
    if (openIdx < 0) return false
    const body = storeSrc.slice(openIdx, openIdx + 900)
    const guardIdx = body.indexOf("volumeFileViewLevel(knownSize) !== 'full'")
    const readIdx = body.indexOf('await readFile(path)')
    return guardIdx >= 0 && readIdx > guardIdx
  })(),
  'openFile 里必须先判定、后读取'
)
check(
  'store: 读取结果要按实际字节数再收敛一次',
  /const limited =\s*result\.size !== undefined && setMetaOnly\(/.test(storeSrc)
)
check(
  'store: 收敛时只保留元信息、丢弃内容',
  /fileResult\.value = \{ path, sha256, size \}/.test(storeSrc)
)
check('store: 关闭面板要清掉仅下载标记', /fileDownloadOnly\.value = false/.test(storeSrc))

/* ───────── 3. 视图接线 ───────── */

const viewSrc = readFileSync(join(srcDir, 'views', 'VolumeFilesView.vue'), 'utf8')

check('视图: 打开文件时把列表已知大小传下去', /store\.openFile\(path, entry\.size\)/.test(viewSrc))
check('视图: 仅下载标记要传给面板', /:download-only="store\.fileDownloadOnly\.value"/.test(viewSrc))
check(
  '视图: 仅下载态点击下载要按需读一次',
  /if \(store\.fileDownloadOnly\.value\) \{\s*void downloadPath\(path\)/.test(viewSrc)
)
check('视图: 超限文件不给下载入口', /store\.fileTooLarge\.value\) return/.test(viewSrc))

/* ───────── 4. 面板 ───────── */

const panelSrc = readFileSync(join(srcDir, 'components', 'volume', 'VolumeFilePanel.vue'), 'utf8')

check('面板: 声明 downloadOnly 属性', /downloadOnly\?: boolean/.test(panelSrc))
check(
  '面板: 仅下载判定复用同一函数（不另写阈值）',
  /props\.downloadOnly \|\| viewLevel\.value === 'download'/.test(panelSrc)
)
check(
  '面板: 仅下载态必须禁用编辑器',
  /!isDownloadOnly\.value/.test(panelSrc)
)
check(
  '面板: 超限文案不再重复写两遍限制值',
  !/超过在线查看上限[\s\S]{0,120}（当前限制/.test(panelSrc),
  '文案里出现了重复的上限说明'
)
check(
  '面板: 仅下载提示要说明可下载',
  /超过在线查看上限[\s\S]{0,200}下载/.test(panelSrc),
  '用户需要知道下一步怎么拿到文件'
)
check('面板: 下载按钮仍由 canDownload 控制', /v-if="canDownload"/.test(panelSrc))

/* ───────── 5. 容器透传 ───────── */

const boxSrc = readFileSync(join(srcDir, 'components', 'volume', 'VolumeFileBox.vue'), 'utf8')

check('容器: 声明并透传 downloadOnly', /downloadOnly: boolean/.test(boxSrc) && /:download-only="downloadOnly"/.test(boxSrc))

test('存储卷在线查看上限契约', () => {
  assert.deepEqual(failures, [], `\n${failures.join('\n')}`)
})
