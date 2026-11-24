export interface AuthProvider {
    id: string;
    display_name: string;
    start_url: string;
}
export interface AuthOptions {
    site_name: string;
    login_notice: string;
    registration_enabled: boolean;
    local_login_enabled: boolean;
    providers: AuthProvider[];
}
export interface SystemSettings {
    local_login_enabled: true;
    site_name: string;
    login_notice: string;
    registration_enabled: boolean;
    registration_group_id: string;
    session_ttl_seconds: number;
    login_max_attempts: number;
    login_window_seconds: number;
    /**
     * 审计日志保留天数，取值 1–30。
     *
     * 30 是**硬上限**（后端拒绝更大值，不会静默截断），因为审计只用于近期
     * 复盘，不承担长期归档；保存更久需要自行导出。后台每小时按这个天数
     * 删除更早的记录，删除不可恢复。
     */
    audit_retention_days: number;
    version: number;
}
