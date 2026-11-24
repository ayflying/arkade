/**
 * 把一行用户输入的命令拆成命令向量（argv）。
 *
 * ============================ 为什么需要它 ============================
 *
 * 容器内执行命令的协议要求 **命令向量**（`["/bin/sh","-c","ps aux"]`），
 * 而不是一整行字符串。自定义命令输入框里用户写的是"一整行"，
 * 所以必须有一层拆分。
 *
 * ============================ 为什么不交给后端 ============================
 *
 * 后端（Agent）刻意不做字符串拆分：它只接收向量，命令边界由调用方决定。
 * 如果让后端去 split(" ")，引号、转义、含空格参数全都会错，
 * 而且"校验过的内容"与"真正执行的内容"就变成了两个不同的东西。
 *
 * ============================ 规则（逐条对齐 POSIX shell 的常见子集） ============================
 *
 * - 空白分隔参数（空格 / 制表符 / 换行）；
 * - `'…'` 单引号内全部原样，**不支持转义**（与 shell 一致）；
 * - `"…"` 双引号内支持 `\"` 与 `\\` 两个转义，其余原样；
 * - 引号外 `\x` 表示字面 x；
 * - 引号未闭合、结尾悬空反斜杠 → 返回错误，**不做猜测性修复**。
 *
 * 刻意不支持：变量展开、命令替换、通配符、管道、重定向。
 * 这些属于 shell 的职责——需要它们时用户应显式写 `/bin/sh -c '…'`，
 * 让容器里的 shell 去解释，而不是由前端偷偷实现半套 shell。
 */
export interface SplitCommandLineResult {
  /** 拆分成功时的命令向量（可能为空数组） */
  args: string[]
  /** 拆分失败时的中文原因；成功时为空串 */
  error: string
}

/** 拆分一行命令。空输入返回空 args，由调用方决定是否报错。 */
export function splitCommandLine(input: string): SplitCommandLineResult {
  const args: string[] = []
  let current = ''
  /** 当前是否已经"开始"一个参数（用于区分空串参数 "" 与"还没有参数"） */
  let started = false
  /** 0 = 无引号，1 = 单引号，2 = 双引号 */
  let quote: 0 | 1 | 2 = 0

  const isSpace = (ch: string): boolean => ch === ' ' || ch === '\t' || ch === '\n' || ch === '\r'

  for (let i = 0; i < input.length; i += 1) {
    const ch = input[i]

    if (quote === 0) {
      if (isSpace(ch)) {
        if (started) {
          args.push(current)
          current = ''
          started = false
        }
        continue
      }
      if (ch === "'") {
        quote = 1
        started = true
        continue
      }
      if (ch === '"') {
        quote = 2
        started = true
        continue
      }
      if (ch === '\\') {
        // 悬空反斜杠：不猜，直接报错
        if (i + 1 >= input.length) {
          return { args: [], error: '命令以反斜杠结尾，缺少被转义的字符' }
        }
        i += 1
        current += input[i]
        started = true
        continue
      }
      current += ch
      started = true
      continue
    }

    if (quote === 1) {
      if (ch === "'") {
        quote = 0
        continue
      }
      current += ch
      continue
    }

    // 双引号内：只认 \" 与 \\，其它反斜杠原样保留（让容器里的程序自己看）
    if (ch === '\\') {
      if (i + 1 >= input.length) {
        return { args: [], error: '命令以反斜杠结尾，缺少被转义的字符' }
      }
      const next = input[i + 1]
      if (next === '"' || next === '\\') {
        current += next
        i += 1
        continue
      }
      current += ch
      continue
    }
    if (ch === '"') {
      quote = 0
      continue
    }
    current += ch
  }

  if (quote === 1) return { args: [], error: '单引号未闭合' }
  if (quote === 2) return { args: [], error: '双引号未闭合' }
  if (started) args.push(current)
  return { args, error: '' }
}
