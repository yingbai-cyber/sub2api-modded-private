/**
 * User Models API endpoints (non-admin)
 * 用户侧「可用模型」查询：按当前用户可访问分组返回可支配模型。
 */

import { apiClient } from './client'

export interface UserAvailableModelGroup {
  id: number
  name: string
  platform: string
  subscription_type: string
  rate_multiplier: number
  is_exclusive: boolean
}

export interface UserAvailableModel {
  name: string
  platform: string
  display_name: string
}

export interface UserAvailableModelSection {
  group: UserAvailableModelGroup
  models: UserAvailableModel[]
}

export async function getAvailable(options?: { signal?: AbortSignal }): Promise<UserAvailableModelSection[]> {
  const { data } = await apiClient.get<UserAvailableModelSection[]>('/models/available', {
    signal: options?.signal
  })
  return data
}

export const userModelsAPI = { getAvailable }

export default userModelsAPI
