/**
 * 前端只做 bcrypt 的 72 字节上限提示；最短长度限制已移除，
 * 与后端 user.ValidatePassword 保持一致（只拒绝全空白和超长）。
 */
export function passwordIssue(password: string): string {
    if (new TextEncoder().encode(password).length > 72)
        return '密码的 UTF-8 总长度不能超过 72 字节（中文等字符占多个字节）';
    return '';
}
