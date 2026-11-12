import type { User } from './auth';
export interface ManagedUser extends User {
    disabled: boolean;
    created_at: number;
    updated_at: number;
    group_ids: string[];
}
export interface Group {
    id: string;
    name: string;
    description: string;
    member_count: number;
    grant_count: number;
    created_at: number;
}
export interface GroupMember {
    id: string;
    username: string;
    display_name: string;
    role: string;
    disabled: boolean;
}
export type Permission = 'none' | 'view' | 'operate' | 'manage';
export interface GroupGrant {
    agent_id: string;
    permission: Exclude<Permission, 'none'>;
}
export interface CreateUser {
    username: string;
    display_name: string;
    password: string;
    role: 'admin' | 'user';
}
export const ROLE_OPTIONS = [{ label: '普通用户', value: 'user' }, { label: '管理员', value: 'admin' }];
export const PERMISSION_OPTIONS = [
    { label: '无权限', value: 'none' }, { label: '查看', value: 'view' },
    { label: '操作', value: 'operate' }, { label: '管理', value: 'manage' }
];
