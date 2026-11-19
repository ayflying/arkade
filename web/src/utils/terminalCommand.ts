export interface TerminalCommandToken {
  text: string
  kind: 'plain' | 'executable' | 'option' | 'value'
}

/** 仅为展示分色，不做 Shell 解析。保留引号、转义和空白，复制时不改命令原文。 */
export function highlightTerminalCommand(command: string): TerminalCommandToken[] {
  const words = command.match(/\s+|(?:[^\s'"\\]+|'[^']*'|"(?:\\[\s\S]|[^"\\])*"|\\[\s\S]?|['"])+/g) ?? []
  let firstWord = true
  return words.map((text) => {
    if (/^\s/.test(text)) return { text, kind: 'plain' }
    if (firstWord) {
      firstWord = false
      return { text, kind: 'executable' }
    }
    if (/^--?[^-\s]/.test(text)) return { text, kind: 'option' }
    if (/^(?:['"]|\.{0,2}\/|[a-z]:\\)/i.test(text)) return { text, kind: 'value' }
    return { text, kind: 'plain' }
  })
}
