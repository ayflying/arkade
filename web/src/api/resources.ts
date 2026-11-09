/**
 * 服务器资源接口：本地镜像 / 网络 / 存储卷
 *
 * ⚠️ 契约状态：按 task-6 描述的预期接口实现，待后端确认后校准。
 *
 * 三者模式一致：GET 列表 + DELETE 单项，因此集中在一个文件里。
 * 删除类接口返回 204 无响应体，故返回 Promise<void>。
 */

import { httpDelete, httpGet } from './request'
import type {
  ImageListResponse,
  NetworkListResponse,
  VolumeListResponse
} from '@/types/resource'

/** 拼出某台服务器的资源基础路径 */
function resourceBase(agentId: string, resource: string): string {
  return `/agents/${encodeURIComponent(agentId)}/${resource}`
}

/** 列出本地镜像 */
export async function listImages(agentId: string): Promise<ImageListResponse> {
  return httpGet<ImageListResponse>(resourceBase(agentId, 'images'))
}

/**
 * 删除镜像。
 *
 * @param force 强制删除（即使有容器引用）。有引用时不加 force 会被后端拒绝，
 *              因此前端在「引用容器数 > 0」时要让用户显式勾选强制。
 */
export async function deleteImage(agentId: string, imageId: string, force = false): Promise<void> {
  const query = force ? '?force=true' : ''
  return httpDelete(`${resourceBase(agentId, 'images')}/${encodeURIComponent(imageId)}${query}`)
}

/** 列出网络 */
export async function listNetworks(agentId: string): Promise<NetworkListResponse> {
  return httpGet<NetworkListResponse>(resourceBase(agentId, 'networks'))
}

/** 删除网络 */
export async function deleteNetwork(agentId: string, networkId: string): Promise<void> {
  return httpDelete(`${resourceBase(agentId, 'networks')}/${encodeURIComponent(networkId)}`)
}

/** 列出存储卷 */
export async function listVolumes(agentId: string): Promise<VolumeListResponse> {
  return httpGet<VolumeListResponse>(resourceBase(agentId, 'volumes'))
}

/** 删除存储卷（数据永久丢失，调用方必须二次确认） */
export async function deleteVolume(agentId: string, name: string): Promise<void> {
  return httpDelete(`${resourceBase(agentId, 'volumes')}/${encodeURIComponent(name)}`)
}
