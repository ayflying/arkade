import { httpGet, httpPost, httpDelete } from './request';
export interface Identity {
    id: string;
    provider_id: string;
    created_at: number;
}
export const listIdentities = async () => (await httpGet<{
    items: Identity[];
}>('/account/identities')).items;
export const bindIdentity = (providerID: string, password: string) => httpPost<{
    authorization_url: string;
}>(`/account/identities/${encodeURIComponent(providerID)}/bind`, { password, redirect: '/account' });
export const unbindIdentity = (id: string) => httpDelete<{
    deleted: boolean;
}>(`/account/identities/${encodeURIComponent(id)}`);
