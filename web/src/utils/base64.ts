/**
 * base64 ↔ UTF-8 文本转换
 *
 * ## 为什么不能直接用 btoa / atob
 *
 * `btoa` 只接受「每个字符都在 0-255 范围内」的字符串。
 * 中文、emoji、任何非 Latin-1 字符直接抛 `InvalidCharacterError`。
 * 而这个功能的核心场景恰恰是**编辑 UTF-8 文本配置**（中文注释、中文值都会出现），
 * 所以必须先按 UTF-8 编码成字节，再 base64。
 *
 * 同理 `atob` 返回的是「每字符一字节」的字符串，直接当 UTF-8 用会得到乱码。
 *
 * ## 为什么自己实现而不是引库
 *
 * 需要的只是 TextEncoder/TextDecoder 这层薄封装，两个函数。
 * 引一个 base64 库只为这点事不值得（而且很多库默认用 latin1，正好是错的那个）。
 */

/** 把二进制字符串（每字符 0-255）转成 base64；纯 JS 实现避免 btoa 的大数组问题 */
const BASE64_ALPHABET = 'ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/'

/**
 * 字节数组 → base64 字符串。
 *
 * 分块处理：一次性 `String.fromCharCode(...bytes)` 在几 MB 时会爆调用栈
 * （spread 的参数个数有上限，Chrome 约 65k）。这里逐块转换。
 */
export function bytesToBase64(bytes: Uint8Array): string {
  let out = ''
  const chunkSize = 8192
  for (let i = 0; i < bytes.length; i += chunkSize) {
    const chunk = bytes.subarray(i, Math.min(i + chunkSize, bytes.length))
    for (let j = 0; j < chunk.length; j += 1) {
      // 用非空断言避免 noUncheckedIndexedAccess 干扰；索引必然有效
      const b = chunk[j] as number
      out += String.fromCharCode(b)
    }
  }
  return base64FromBinaryString(out)
}

/** 把「每字符一字节」的字符串编码成 base64 */
function base64FromBinaryString(binary: string): string {
  let out = ''
  let i = 0
  for (; i + 2 < binary.length; i += 3) {
    const n =
      ((binary.charCodeAt(i) & 0xff) << 16) |
      ((binary.charCodeAt(i + 1) & 0xff) << 8) |
      (binary.charCodeAt(i + 2) & 0xff)
    out += BASE64_ALPHABET[(n >> 18) & 63]
    out += BASE64_ALPHABET[(n >> 12) & 63]
    out += BASE64_ALPHABET[(n >> 6) & 63]
    out += BASE64_ALPHABET[n & 63]
  }
  const remain = binary.length - i
  if (remain === 1) {
    const n = (binary.charCodeAt(i) & 0xff) << 16
    out += BASE64_ALPHABET[(n >> 18) & 63]
    out += BASE64_ALPHABET[(n >> 12) & 63]
    out += '=='
  } else if (remain === 2) {
    const n = ((binary.charCodeAt(i) & 0xff) << 16) | ((binary.charCodeAt(i + 1) & 0xff) << 8)
    out += BASE64_ALPHABET[(n >> 18) & 63]
    out += BASE64_ALPHABET[(n >> 12) & 63]
    out += BASE64_ALPHABET[(n >> 6) & 63]
    out += '='
  }
  return out
}

/** base64 → 字节数组 */
export function base64ToBytes(base64: string): Uint8Array {
  const binary = binaryStringFromBase64(base64)
  const bytes = new Uint8Array(binary.length)
  for (let i = 0; i < binary.length; i += 1) {
    bytes[i] = binary.charCodeAt(i) & 0xff
  }
  return bytes
}

/** base64 → 「每字符一字节」的字符串 */
function binaryStringFromBase64(base64: string): string {
  // 去掉换行/空白：有些后端会按行折行输出
  const clean = base64.replace(/[\s]/g, '')
  let out = ''
  let buffer = 0
  let bits = 0
  for (let i = 0; i < clean.length; i += 1) {
    const ch = clean[i] as string
    if (ch === '=') break
    const value = BASE64_ALPHABET.indexOf(ch)
    // 非法字符直接跳过而不是抛错：宁可拿到部分内容也别让整个预览炸掉，
    // 真正的完整性由 sha256 和后端校验保证
    if (value < 0) continue
    buffer = (buffer << 6) | value
    bits += 6
    if (bits >= 8) {
      bits -= 8
      out += String.fromCharCode((buffer >> bits) & 0xff)
    }
  }
  return out
}

/**
 * UTF-8 文本 → base64。
 *
 * 这是 write 接口需要的方向。
 */
export function textToBase64(text: string): string {
  return bytesToBase64(new TextEncoder().encode(text))
}

/**
 * base64 → UTF-8 文本。
 *
 * ⚠️ 只有在**已确认内容是 UTF-8 文本**时才该调用。
 * 二进制文件（数据库、图片）用这个转换会得到一堆替换字符（U+FFFD），
 * 再保存回去就把文件彻底毁了。二进制判定见 utils/binaryDetect.ts。
 */
export function base64ToText(base64: string): string {
  return new TextDecoder('utf-8').decode(base64ToBytes(base64))
}

/** 字节数 → 人类可读（供需要在无网络时估算的场景用） */
export function base64ByteLength(base64: string): number {
  const clean = base64.replace(/[\s]/g, '')
  if (clean === '') return 0
  let padding = 0
  if (clean.endsWith('==')) padding = 2
  else if (clean.endsWith('=')) padding = 1
  return Math.floor((clean.length * 3) / 4) - padding
}
