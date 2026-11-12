import { httpGet, httpPost, httpPatch, httpPut, httpDelete } from './request';
import type { CreateUser, ManagedUser, Group, GroupMember, GroupGrant, Permission } from '@/types/management';
const userPath = (id: string) => `/users/${encodeURIComponent(id)}`;
const groupPath = (id: string) => `/groups/${encodeURIComponent(id)}`;
export const listUsers = async () => (await httpGet<{
    users: ManagedUser[];
}>('/users')).users;
export const createUser = async (body: CreateUser) => (await httpPost<{
    user: ManagedUser;
}>('/users', body)).user;
export const updateUser = async (id: string, body: Partial<Pick<ManagedUser, 'display_name' | 'role' | 'disabled'>>) => (await httpPatch<{
    user: ManagedUser;
}>(userPath(id), body)).user;
export const resetUserPassword = (id: string, password: string) => httpPut<void>(`${userPath(id)}/password`, { password });
export const deleteUser = (id: string) => httpDelete(userPath(id));
export interface EffectivePermissions {
    user_id: string;
    role: string;
    disabled: boolean;
    agents: {
        agent_id: string;
        agent_name: string;
        permission: Permission;
        sources: {
            group_id: string;
            group_name: string;
            permission: Permission;
        }[];
    }[];
}
export const userPermissions = (id: string) => httpGet<EffectivePermissions>(`${userPath(id)}/permissions`);
export const listGroups = async () => (await httpGet<{
    groups: Group[];
}>('/groups')).groups;
export const createGroup = async (body: {
    name: string;
    description: string;
}) => (await httpPost<{
    group: Group;
}>('/groups', body)).group;
export const updateGroup = async (id: string, body: {
    name: string;
    description: string;
}) => (await httpPatch<{
    group: Group;
}>(groupPath(id), body)).group;
export const deleteGroup = (id: string) => httpDelete(groupPath(id));
export const groupMembers = async (id: string) => (await httpGet<{
    members: GroupMember[];
}>(`${groupPath(id)}/members`)).members;
export const setGroupMembers = (id: string, user_ids: string[]) => httpPut(`${groupPath(id)}/members`, { user_ids });
export const groupGrants = async (id: string) => (await httpGet<{
    grants: GroupGrant[];
}>(`${groupPath(id)}/grants`)).grants;
export const setGroupGrants = (id: string, grants: GroupGrant[]) => httpPut(`${groupPath(id)}/grants`, { grants });
