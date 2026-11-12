import { httpGet, httpPost, httpPut, httpPatch, httpDelete } from './request';
import type { SystemSettings } from '@/types/settings';
export const getSettings = async () => (await httpGet<{
    settings: SystemSettings;
}>('/settings')).settings;
export const saveSettings = async (settings: SystemSettings) => (await httpPut<{
    settings: SystemSettings;
}>('/settings', settings)).settings;
export interface OIDCProvider {
    id: string;
    display_name: string;
    issuer: string;
    client_id: string;
    scopes: string;
    default_group_id: string;
    enabled: boolean;
    auto_register: boolean;
    has_client_secret: boolean;
    callback_url: string;
    version: number;
    created_at: number;
    updated_at: number;
}
export interface ProviderInput {
    display_name: string;
    issuer: string;
    client_id: string;
    scopes: string;
    default_group_id: string;
    enabled: boolean;
    auto_register: boolean;
    version?: number;
    client_secret?: string;
}
export interface DiscoveryResult {
    issuer: string;
    authorization_endpoint: string;
    token_endpoint: string;
    jwks_uri: string;
    reachable: boolean;
}
const path = (id: string) => `/settings/oidc-providers/${encodeURIComponent(id)}`;
export const listProviders = async () => (await httpGet<{
    providers: OIDCProvider[];
}>('/settings/oidc-providers')).providers;
export const createProvider = async (body: ProviderInput) => (await httpPost<{
    provider: OIDCProvider;
}>('/settings/oidc-providers', body)).provider;
export const updateProvider = async (id: string, body: ProviderInput) => (await httpPatch<{
    provider: OIDCProvider;
}>(path(id), body)).provider;
export const deleteProvider = (id: string) => httpDelete(path(id));
export const testProvider = (id: string) => httpPost<DiscoveryResult>(`${path(id)}/test`);
export function providerPayload(input: ProviderInput): ProviderInput {
    const { client_secret, ...rest } = input;
    return client_secret ? { ...rest, client_secret } : rest;
}
