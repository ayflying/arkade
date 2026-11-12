import { httpGet, httpPatch, httpPost, httpPut } from './request';
import type { UserEnvelope } from '@/types/auth';
import type { AuthOptions } from '@/types/settings';
export const getAuthOptions = () => httpGet<AuthOptions>('/auth/options');
export const register = (body: {
    username: string;
    display_name: string;
    password: string;
}) => httpPost<UserEnvelope>('/auth/register', body);
export const getAccount = async () => (await httpGet<UserEnvelope>('/account')).user;
export const updateAccount = async (display_name: string) => (await httpPatch<UserEnvelope>('/account', { display_name })).user;
export const changePassword = (old_password: string, new_password: string) => httpPut<void>('/account/password', { old_password, new_password });
