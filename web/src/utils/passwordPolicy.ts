export function passwordIssue(password: string, minimum = 8): string {
    if (Array.from(password).length < minimum)
        return `密码至少需要 ${minimum} 位`;
    if (new TextEncoder().encode(password).length > 72)
        return '密码的 UTF-8 总长度不能超过 72 字节（中文等字符占多个字节）';
    return '';
}
