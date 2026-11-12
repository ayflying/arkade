export interface AuthProvider {
    id: string;
    display_name: string;
    start_url: string;
}
export interface AuthOptions {
    site_name: string;
    login_notice: string;
    registration_enabled: boolean;
    password_min_length: number;
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
    password_min_length: number;
    login_max_attempts: number;
    login_window_seconds: number;
    audit_retention_days: number;
    version: number;
}
