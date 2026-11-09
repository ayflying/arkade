/**
 * 二进制内容判定
 *
 * ## 为什么必须有这一步
 *
 * 卷里大部分是二进制：SQLite 数据库、LevelDB 的 .ldb/SST、PostgreSQL 的段文件、
 * 图片、压缩包。如果在浏览器里当文本打开它们：
 *
 * 1. UTF-8 解码会把非法字节换成 U+FFFD（替换字符），**这一步就已经丢数据**；
 * 2. 用户随手动一下存回去，写回的就是被替换字符污染的内容——
 *    数据库直接损坏，而且**不可恢复**。
 *
 * 所以必须在**解码之前**、只看原始字节就判断出「这不是文本」，
 * 然后拒绝编辑，只允许下载原件。
 *
 * ## 判定规则
 *
 * 按顺序检查前若干字节：
 * 1. 含 NUL 字节 → 二进制（几乎所有文本格式都不含 NUL，这是最强的信号；
 *    也是 `file`、git、grep 等工具判定二进制的传统依据）；
 * 2. 出现了「不该出现在文本里」的控制字符 → 二进制；
 * 3. 尝试严格 UTF-8 解码，失败 → 二进制。
 *
 * 反过来，**只要通过这三关就认为是文本**——宁可把某个真二进制误判成文本
 * （用户会看到乱码，但至少能看出来不对劲），也不要把文本误判成二进制
 * （用户就完全没法编辑了）。
 */

/** 允许出现在文本里的控制字符：制表、换行、回车、换页 */
function isTextControl(byte: number): boolean {
  return byte === 0x09 || byte === 0x0a || byte === 0x0d || byte === 0x0c
}

/**
 * 判定是否像二进制。
 *
 * @param bytes 文件原始字节（可以是完整内容，也可以是前几 KB 的样本）
 */
export function looksBinary(bytes: Uint8Array): boolean {
  if (bytes.length === 0) return false

  // 只看前 8 KiB 足够：二进制文件几乎必然在头部就出现 NUL 或非法控制字符
  const sample = bytes.subarray(0, 8192)

  for (const byte of sample) {
    // NUL 是最强信号，立刻判定
    if (byte === 0x00) return true
    // 0x01-0x08、0x0b、0x0e-0x1f、0x7f 都是文本里不该出现的控制字符
    if (byte < 0x20 && !isTextControl(byte)) return true
    if (byte === 0x7f) return true
  }

  /*
   * 最后做严格 UTF-8 校验：用 fatal 模式解码，失败即说明不是合法 UTF-8 文本。
   * 有些二进制（如纯随机数据）可能不含 NUL 也不含控制字符，
   * 靠这一步兜住。
   */
  try {
    new TextDecoder('utf-8', { fatal: true }).decode(sample)
  } catch {
    return true
  }

  return false
}

/**
 * 判定是否像二进制（从 base64 直接判定）。
 *
 * 供「后端只给了 base64」的场景用：解码后判定，避免先转成字符串再判断
 * （那一步已经会污染数据了）。
 */
export function looksBinaryBase64(base64: string): boolean {
  // 只为判定，取前若干字符解出样本即可；完整解码没必要
  const clean = base64.replace(/[\s]/g, '')
  // 8192 字节对应约 10924 个 base64 字符（4/3 倍），多截一点无妨
  const sampleBase64 = clean.slice(0, 12000)
  const padded = padBase64(sampleBase64)
  try {
    const binary = atobSafe(padded)
    const bytes = new Uint8Array(binary.length)
    for (let i = 0; i < binary.length; i += 1) bytes[i] = binary.charCodeAt(i) & 0xff
    return looksBinary(bytes)
  } catch {
    // 解不开就当二进制处理：宁可不给编辑，也不能给一个会毁数据的编辑器
    return true
  }
}

/** 补齐 base64 的 `=`，截断后长度不合法时 atob 会抛错 */
function padBase64(s: string): string {
  const remain = s.length % 4
  if (remain === 0) return s
  return s + '='.repeat(4 - remain)
}

/** atob 的安全封装：以 0 开头的二进制字符串里 atob 行为在各浏览器间一致，但需要防御非法输入 */
function atobSafe(s: string): string {
  return atob(s)
}

/**
 * 检测文本里是否有换行符风格，用于「保存时保留原换行」。
 *
 * Windows/Linux 混用时，把 CRLF 存成 LF 会让 git diff 出现整个文件改动，
 * 用户会以为出了大事。这里只用于提示，不做自动转换。
 */
export function detectLineEnding(text: string): 'crlf' | 'lf' {
  return text.includes('\r\n') ? 'crlf' : 'lf'
}
