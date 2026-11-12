// Frontend navigation is a usability guard, never a substitute for API authorization.
export function accessRedirect(input: {
    loggedIn: boolean;
    mustChange: boolean;
    admin: boolean;
    public: boolean;
    adminOnly: boolean;
    account: boolean;
}): string | null {
    if (!input.loggedIn)
        return input.public ? null : '/login';
    if (input.mustChange && !input.account)
        return '/account';
    if (input.public)
        return '/overview';
    if (input.adminOnly && !input.admin)
        return '/forbidden';
    return null;
}
export function safeLocalPath(raw: unknown, fallback = '/overview'): string {
    return typeof raw === 'string' && raw.startsWith('/') && !raw.startsWith('//') && !raw.includes('\\') && !/[\r\n]/.test(raw) ? raw : fallback;
}
export function safeProviderStart(raw: string): string | null {
    return raw.startsWith('/api/auth/') && !raw.includes('\\') && !raw.startsWith('//') ? raw : null;
}
