import { apiClient } from '../client'
import type { AccountPlatform, AccountType } from '@/types'

export type AccountCleanupAction = 'delete' | 'move'
export type AccountCleanupStatus =
  | 'active'
  | 'inactive'
  | 'disabled'
  | 'error'
  | 'rate_limited'
  | 'temp_unschedulable'
  | 'unschedulable'

export interface AccountCleanupRequest {
  source_group_id: number
  statuses: AccountCleanupStatus[]
  action: AccountCleanupAction
  target_group_id?: number | null
  platform?: AccountPlatform | ''
  type?: AccountType | ''
  search?: string
  page?: number
  page_size?: number
  limit?: number
  account_ids?: number[]
  confirm_text?: string
}

export interface AccountCleanupPreviewGroup {
  id: number
  name: string
}

export interface AccountCleanupPreviewItem {
  id: number
  name: string
  platform: string
  type: string
  status: string
  schedulable: boolean
  error_message?: string
  group_ids?: number[]
  groups?: AccountCleanupPreviewGroup[]
  last_used_at?: string | null
  reason: string
  target_group_id?: number | null
}

export interface AccountCleanupSummary {
  by_status: Record<string, number>
  by_platform: Record<string, number>
}

export interface AccountCleanupPreviewResponse {
  action: AccountCleanupAction
  source_group_id: number
  target_group_id?: number | null
  matched: number
  page: number
  page_size: number
  pages: number
  limit: number
  capped: boolean
  items: AccountCleanupPreviewItem[]
  summary: AccountCleanupSummary
}

export interface AccountCleanupFailedItem {
  account_id: number
  name?: string
  error: string
}

export interface AccountCleanupSkippedItem {
  account_id: number
  name?: string
  reason: string
}

export interface AccountCleanupExecuteResponse {
  total: number
  success: number
  failed: number
  skipped: number
  success_ids: number[]
  failed_items: AccountCleanupFailedItem[]
  skipped_items: AccountCleanupSkippedItem[]
}

export async function preview(payload: AccountCleanupRequest): Promise<AccountCleanupPreviewResponse> {
  const { data } = await apiClient.post<AccountCleanupPreviewResponse>('/admin/accounts/cleanup/preview', payload)
  return data
}

export async function execute(payload: AccountCleanupRequest): Promise<AccountCleanupExecuteResponse> {
  const { data } = await apiClient.post<AccountCleanupExecuteResponse>('/admin/accounts/cleanup', payload)
  return data
}

export const accountCleanupAPI = {
  preview,
  execute
}

export default accountCleanupAPI
