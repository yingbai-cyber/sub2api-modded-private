<template>
  <AppLayout>
    <div class="space-y-4 pb-8">
      <div class="space-y-4">
          <div class="rounded-xl border border-amber-200 bg-amber-50 px-4 py-3 text-sm text-amber-800 dark:border-amber-700/40 dark:bg-amber-900/20 dark:text-amber-200">
            <div class="font-semibold">{{ t('admin.accountCleanup.warningTitle') }}</div>
            <div class="mt-1">{{ t('admin.accountCleanup.warningDescription') }}</div>
          </div>

          <div class="rounded-xl border border-gray-200 bg-white p-4 shadow-sm dark:border-dark-700 dark:bg-dark-900">
            <div class="grid gap-4 lg:grid-cols-2 xl:grid-cols-3">
              <div class="space-y-1.5">
                <label class="form-label">{{ t('admin.accountCleanup.sourceGroup') }}</label>
                <Select
                  v-model="form.sourceGroupId"
                  :options="sourceGroupOptions"
                  searchable
                  :placeholder="t('admin.accountCleanup.selectSourceGroup')"
                />
              </div>

              <div class="space-y-1.5">
                <label class="form-label">{{ t('admin.accountCleanup.action') }}</label>
                <Select v-model="form.action" :options="actionOptions" />
              </div>

              <div v-if="form.action === 'move'" class="space-y-1.5">
                <label class="form-label">{{ t('admin.accountCleanup.targetGroup') }}</label>
                <Select
                  v-model="form.targetGroupId"
                  :options="targetGroupOptions"
                  searchable
                  :placeholder="t('admin.accountCleanup.selectTargetGroup')"
                />
              </div>

              <div class="space-y-1.5">
                <label class="form-label">{{ t('admin.accountCleanup.platform') }}</label>
                <Select v-model="form.platform" :options="platformOptions" />
              </div>

              <div class="space-y-1.5">
                <label class="form-label">{{ t('admin.accountCleanup.type') }}</label>
                <Select v-model="form.type" :options="typeOptions" />
              </div>

              <div class="space-y-1.5">
                <label class="form-label">{{ t('admin.accountCleanup.search') }}</label>
                <SearchInput
                  v-model="form.search"
                  :placeholder="t('admin.accountCleanup.searchPlaceholder')"
                />
              </div>
            </div>

            <div class="mt-4 space-y-2">
              <label class="form-label">{{ t('admin.accountCleanup.statuses') }}</label>
              <div class="flex flex-wrap gap-2">
                <label
                  v-for="option in statusOptions"
                  :key="option.value"
                  class="inline-flex cursor-pointer items-center gap-2 rounded-lg border px-3 py-2 text-sm transition-colors"
                  :class="isStatusSelected(option.value) ? 'border-primary-300 bg-primary-50 text-primary-700 dark:border-primary-600/60 dark:bg-primary-900/20 dark:text-primary-200' : 'border-gray-200 bg-white text-gray-700 hover:bg-gray-50 dark:border-dark-700 dark:bg-dark-800 dark:text-gray-200 dark:hover:bg-dark-700'"
                >
                  <input
                    type="checkbox"
                    class="rounded border-gray-300 text-primary-600 focus:ring-primary-500"
                    :checked="isStatusSelected(option.value)"
                    @change="toggleStatus(option.value)"
                  />
                  <span>{{ option.label }}</span>
                </label>
              </div>
            </div>

            <div v-if="form.action === 'move'" class="mt-4 rounded-lg border border-blue-200 bg-blue-50 px-3 py-2 text-sm text-blue-700 dark:border-blue-700/40 dark:bg-blue-900/20 dark:text-blue-200">
              {{ t('admin.accountCleanup.moveOverwriteHint') }}
            </div>

            <div class="mt-4 flex flex-wrap items-center justify-between gap-3">
              <div class="text-sm text-gray-500 dark:text-gray-400">
                {{ t('admin.accountCleanup.limitHint', { limit: form.limit }) }}
              </div>
              <div class="flex gap-2">
                <button type="button" class="btn btn-secondary" @click="resetForm">
                  {{ t('common.reset') }}
                </button>
                <button type="button" class="btn btn-primary" :disabled="previewLoading" @click="() => handlePreview()">
                  {{ previewLoading ? t('admin.accountCleanup.previewing') : t('admin.accountCleanup.preview') }}
                </button>
                <button
                  v-if="previewResult"
                  type="button"
                  class="btn"
                  :class="form.action === 'delete' ? 'btn-danger' : 'btn-primary'"
                  :disabled="!canExecute || executing"
                  @click="openConfirm"
                >
                  {{ executing ? t('admin.accountCleanup.executing') : executeButtonText }}
                </button>
              </div>
            </div>
          </div>
        </div>

      <div class="space-y-4">
          <div v-if="previewResult" class="grid gap-3 md:grid-cols-4">
            <div class="rounded-xl border border-gray-200 bg-white p-4 dark:border-dark-700 dark:bg-dark-900">
              <div class="text-sm text-gray-500 dark:text-gray-400">{{ t('admin.accountCleanup.matched') }}</div>
              <div class="mt-1 text-2xl font-bold text-gray-900 dark:text-white">{{ previewResult.matched }}</div>
              <div v-if="previewResult.capped" class="mt-1 text-xs text-amber-600 dark:text-amber-300">
                {{ t('admin.accountCleanup.cappedHint', { limit: previewResult.limit }) }}
              </div>
            </div>
            <div class="rounded-xl border border-gray-200 bg-white p-4 dark:border-dark-700 dark:bg-dark-900 md:col-span-3">
              <div class="grid gap-3 md:grid-cols-2">
                <div>
                  <div class="text-sm font-medium text-gray-700 dark:text-gray-200">{{ t('admin.accountCleanup.byStatus') }}</div>
                  <div class="mt-2 flex flex-wrap gap-2">
                    <span v-for="(count, status) in previewResult.summary.by_status" :key="status" class="rounded-full bg-gray-100 px-2 py-1 text-xs text-gray-700 dark:bg-dark-700 dark:text-gray-200">
                      {{ statusLabel(status) }}: {{ count }}
                    </span>
                  </div>
                </div>
                <div>
                  <div class="text-sm font-medium text-gray-700 dark:text-gray-200">{{ t('admin.accountCleanup.byPlatform') }}</div>
                  <div class="mt-2 flex flex-wrap gap-2">
                    <span v-for="(count, platform) in previewResult.summary.by_platform" :key="platform" class="rounded-full bg-gray-100 px-2 py-1 text-xs text-gray-700 dark:bg-dark-700 dark:text-gray-200">
                      {{ platformLabel(platform) }}: {{ count }}
                    </span>
                  </div>
                </div>
              </div>
            </div>
          </div>

          <div v-if="executeResult" class="rounded-xl border border-gray-200 bg-white p-4 dark:border-dark-700 dark:bg-dark-900">
            <div class="flex flex-wrap items-center gap-3 text-sm">
              <span class="font-semibold text-gray-900 dark:text-white">{{ t('admin.accountCleanup.executeResult') }}</span>
              <span class="text-emerald-600 dark:text-emerald-300">{{ t('admin.accountCleanup.successCount', { count: executeResult.success }) }}</span>
              <span class="text-rose-600 dark:text-rose-300">{{ t('admin.accountCleanup.failedCount', { count: executeResult.failed }) }}</span>
              <span class="text-amber-600 dark:text-amber-300">{{ t('admin.accountCleanup.skippedCount', { count: executeResult.skipped }) }}</span>
            </div>
            <div v-if="executeResult.failed_items.length" class="mt-3 max-h-40 overflow-auto rounded-lg bg-rose-50 p-3 text-xs text-rose-700 dark:bg-rose-900/20 dark:text-rose-200">
              <div v-for="item in executeResult.failed_items" :key="item.account_id">
                #{{ item.account_id }} {{ item.name || '-' }} — {{ item.error }}
              </div>
            </div>
            <div v-if="executeResult.skipped_items.length" class="mt-3 max-h-40 overflow-auto rounded-lg bg-amber-50 p-3 text-xs text-amber-700 dark:bg-amber-900/20 dark:text-amber-200">
              <div v-for="item in executeResult.skipped_items" :key="item.account_id">
                #{{ item.account_id }} {{ item.name || '-' }} — {{ item.reason }}
              </div>
            </div>
          </div>

          <div class="account-cleanup-table overflow-hidden rounded-xl border border-gray-200 bg-white dark:border-dark-700 dark:bg-dark-900">
            <DataTable :columns="columns" :data="previewItems" :loading="previewLoading" row-key="id">
              <template #empty>
                <div class="flex flex-col items-center py-8 text-gray-500 dark:text-gray-400">
                  <Icon name="inbox" size="xl" class="mb-3" />
                  <p>{{ previewResult ? t('admin.accountCleanup.noMatchedAccounts') : t('admin.accountCleanup.previewEmpty') }}</p>
                </div>
              </template>
              <template #cell-id="{ value }">
                <span class="font-mono text-xs text-gray-500">#{{ value }}</span>
              </template>
              <template #cell-name="{ row }">
                <div class="max-w-xs">
                  <div class="font-medium text-gray-900 dark:text-white">{{ row.name }}</div>
                  <div v-if="row.error_message" class="truncate text-xs text-rose-500" :title="row.error_message">{{ row.error_message }}</div>
                </div>
              </template>
              <template #cell-platform="{ row }">
                <div class="text-sm">
                  <div>{{ platformLabel(row.platform) }}</div>
                  <div class="text-xs text-gray-500">{{ typeLabel(row.type) }}</div>
                </div>
              </template>
              <template #cell-status="{ row }">
                <span class="rounded-full px-2 py-1 text-xs font-medium" :class="statusClass(row.status)">
                  {{ statusLabel(row.status) }}
                </span>
              </template>
              <template #cell-groups="{ row }">
                <div class="flex max-w-xs flex-wrap gap-1">
                  <span v-for="group in row.groups || []" :key="group.id" class="rounded-full bg-gray-100 px-2 py-0.5 text-xs text-gray-600 dark:bg-dark-700 dark:text-gray-300">
                    {{ group.name }}
                  </span>
                  <span v-if="!row.groups?.length" class="text-xs text-gray-400">-</span>
                </div>
              </template>
              <template #cell-reason="{ row }">
                <span class="text-xs text-gray-600 dark:text-gray-300">{{ reasonLabel(row.reason) }}</span>
              </template>
              <template #cell-last_used_at="{ value }">
                <span class="text-xs text-gray-500">{{ formatDateTime(value) }}</span>
              </template>
            </DataTable>
          </div>

          <div v-if="previewResult && previewResult.pages > 1" class="flex justify-end">
            <Pagination
              :page="form.page"
              :page-size="form.pageSize"
              :total="previewResult.matched"
              :page-size-options="[20, 50, 100, 200]"
              @update:page="handlePageChange"
              @update:pageSize="handlePageSizeChange"
            />
          </div>

      </div>
    </div>
  </AppLayout>

  <BaseDialog :show="confirmVisible" :title="confirmTitle" width="normal" @close="confirmVisible = false">
    <div class="space-y-4">
      <p class="text-sm text-gray-600 dark:text-gray-300">{{ confirmMessage }}</p>
      <div v-if="form.action === 'delete'" class="space-y-2">
        <label class="form-label">{{ t('admin.accountCleanup.deleteConfirmInputLabel') }}</label>
        <input v-model="deleteConfirmText" class="input" :placeholder="t('admin.accountCleanup.deleteConfirmPlaceholder')" />
      </div>
    </div>
    <template #footer>
      <div class="flex justify-end gap-3">
        <button type="button" class="btn btn-secondary" @click="confirmVisible = false">{{ t('common.cancel') }}</button>
        <button type="button" class="btn" :class="form.action === 'delete' ? 'btn-danger' : 'btn-primary'" :disabled="confirmDisabled" @click="handleExecute">
          {{ t('common.confirm') }}
        </button>
      </div>
    </template>
  </BaseDialog>
</template>

<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import AppLayout from '@/components/layout/AppLayout.vue'
import Select from '@/components/common/Select.vue'
import SearchInput from '@/components/common/SearchInput.vue'
import DataTable from '@/components/common/DataTable.vue'
import Pagination from '@/components/common/Pagination.vue'
import BaseDialog from '@/components/common/BaseDialog.vue'
import Icon from '@/components/icons/Icon.vue'
import { adminAPI } from '@/api/admin'
import type { AdminGroup, AccountPlatform, AccountType } from '@/types'
import type {
  AccountCleanupAction,
  AccountCleanupExecuteResponse,
  AccountCleanupPreviewItem,
  AccountCleanupPreviewResponse,
  AccountCleanupStatus
} from '@/api/admin/accountCleanup'
import { useAppStore } from '@/stores/app'
import { formatDate } from '@/utils/format'

const { t } = useI18n()
const appStore = useAppStore()

interface FormState {
  sourceGroupId: number | null
  targetGroupId: number | null
  statuses: AccountCleanupStatus[]
  action: AccountCleanupAction
  platform: AccountPlatform | ''
  type: AccountType | ''
  search: string
  page: number
  pageSize: number
  limit: number
}

const form = reactive<FormState>({
  sourceGroupId: null,
  targetGroupId: null,
  statuses: ['error', 'inactive'],
  action: 'move',
  platform: '',
  type: '',
  search: '',
  page: 1,
  pageSize: 50,
  limit: 1000
})

const groups = ref<AdminGroup[]>([])
const groupsLoading = ref(false)
const previewLoading = ref(false)
const executing = ref(false)
const previewResult = ref<AccountCleanupPreviewResponse | null>(null)
const executeResult = ref<AccountCleanupExecuteResponse | null>(null)
const confirmVisible = ref(false)
const deleteConfirmText = ref('')

const columns = computed(() => [
  { key: 'id', label: t('admin.accountCleanup.columns.id') },
  { key: 'name', label: t('admin.accountCleanup.columns.name') },
  { key: 'platform', label: t('admin.accountCleanup.columns.platform') },
  { key: 'status', label: t('admin.accountCleanup.columns.status') },
  { key: 'groups', label: t('admin.accountCleanup.columns.groups') },
  { key: 'reason', label: t('admin.accountCleanup.columns.reason') },
  { key: 'last_used_at', label: t('admin.accountCleanup.columns.lastUsed') }
])

const sourceGroupOptions = computed(() => [
  { value: null, label: t('admin.accountCleanup.selectSourceGroup') },
  ...groups.value.map(group => ({ value: group.id, label: group.name }))
])

const targetGroupOptions = computed(() => [
  { value: null, label: t('admin.accountCleanup.selectTargetGroup') },
  ...groups.value.map(group => ({
    value: group.id,
    label: group.name,
    disabled: group.id === form.sourceGroupId
  }))
])

const actionOptions = computed(() => [
  { value: 'move', label: t('admin.accountCleanup.actionMove') },
  { value: 'delete', label: t('admin.accountCleanup.actionDelete') }
])

const platformOptions = computed(() => [
  { value: '', label: t('admin.accounts.allPlatforms') },
  { value: 'anthropic', label: 'Anthropic' },
  { value: 'openai', label: 'OpenAI' },
  { value: 'gemini', label: 'Gemini' },
  { value: 'antigravity', label: 'Antigravity' }
])

const typeOptions = computed(() => [
  { value: '', label: t('admin.accounts.allTypes') },
  { value: 'oauth', label: t('admin.accounts.oauthType') },
  { value: 'setup-token', label: t('admin.accounts.setupToken') },
  { value: 'apikey', label: t('admin.accounts.apiKey') },
  { value: 'upstream', label: t('admin.accounts.types.upstream') },
  { value: 'bedrock', label: 'AWS Bedrock' },
  { value: 'service_account', label: 'Service Account' },
  { value: 'kiro', label: 'Kiro' }
])

const statusOptions = computed<Array<{ value: AccountCleanupStatus; label: string }>>(() => [
  { value: 'error', label: t('admin.accounts.status.error') },
  { value: 'inactive', label: t('admin.accounts.status.inactive') },
  { value: 'disabled', label: t('common.disabled') },
  { value: 'unschedulable', label: t('admin.accounts.status.unschedulable') },
  { value: 'rate_limited', label: t('admin.accounts.status.rateLimited') },
  { value: 'temp_unschedulable', label: t('admin.accounts.status.tempUnschedulable') },
  { value: 'active', label: t('admin.accounts.status.active') }
])

const previewItems = computed<AccountCleanupPreviewItem[]>(() => previewResult.value?.items || [])
const canExecute = computed(() => !!previewResult.value && previewResult.value.matched > 0 && form.statuses.length > 0)
const executeButtonText = computed(() => form.action === 'delete' ? t('admin.accountCleanup.executeDelete') : t('admin.accountCleanup.executeMove'))
const confirmTitle = computed(() => form.action === 'delete' ? t('admin.accountCleanup.deleteConfirmTitle') : t('admin.accountCleanup.moveConfirmTitle'))
const confirmMessage = computed(() => {
  const count = previewResult.value?.matched || 0
  if (form.action === 'delete') {
    return t('admin.accountCleanup.deleteConfirmMessage', { count })
  }
  const targetName = groups.value.find(group => group.id === form.targetGroupId)?.name || '-'
  return t('admin.accountCleanup.moveConfirmMessage', { count, group: targetName })
})
const confirmDisabled = computed(() => form.action === 'delete' && deleteConfirmText.value.trim() !== 'DELETE')

const buildPayload = (includePage = true) => ({
  source_group_id: Number(form.sourceGroupId),
  statuses: [...form.statuses],
  action: form.action,
  target_group_id: form.action === 'move' ? Number(form.targetGroupId) : undefined,
  platform: form.platform || undefined,
  type: form.type || undefined,
  search: form.search.trim() || undefined,
  page: includePage ? form.page : undefined,
  page_size: includePage ? form.pageSize : undefined,
  limit: form.limit
})

const loadGroups = async () => {
  groupsLoading.value = true
  try {
    groups.value = await adminAPI.groups.getAll()
  } catch (error: any) {
    appStore.showError(error?.message || t('admin.accountCleanup.loadGroupsFailed'))
  } finally {
    groupsLoading.value = false
  }
}

const validateForm = () => {
  if (!form.sourceGroupId) {
    appStore.showWarning(t('admin.accountCleanup.sourceGroupRequired'))
    return false
  }
  if (form.statuses.length === 0) {
    appStore.showWarning(t('admin.accountCleanup.statusRequired'))
    return false
  }
  if (form.action === 'move') {
    if (!form.targetGroupId) {
      appStore.showWarning(t('admin.accountCleanup.targetGroupRequired'))
      return false
    }
    if (form.targetGroupId === form.sourceGroupId) {
      appStore.showWarning(t('admin.accountCleanup.sameGroupWarning'))
      return false
    }
  }
  return true
}

const handlePreview = async (clearExecuteResult = true) => {
  if (!validateForm()) return
  previewLoading.value = true
  if (clearExecuteResult) {
    executeResult.value = null
  }
  try {
    previewResult.value = await adminAPI.accountCleanup.preview(buildPayload(true))
  } catch (error: any) {
    appStore.showError(error?.message || t('admin.accountCleanup.previewFailed'))
  } finally {
    previewLoading.value = false
  }
}

const openConfirm = () => {
  if (!validateForm() || !previewResult.value) return
  deleteConfirmText.value = ''
  confirmVisible.value = true
}

const handleExecute = async () => {
  if (!validateForm()) return
  executing.value = true
  try {
    executeResult.value = await adminAPI.accountCleanup.execute({
      ...buildPayload(false),
      confirm_text: form.action === 'delete' ? deleteConfirmText.value.trim() : undefined
    })
    confirmVisible.value = false
    const result = executeResult.value
    if (result.failed > 0 || result.skipped > 0) {
      appStore.showWarning(t('admin.accountCleanup.executeCompletedWithIssues', { success: result.success, failed: result.failed, skipped: result.skipped }))
    } else {
      appStore.showSuccess(t('admin.accountCleanup.executeCompleted', { success: result.success }))
    }
    // Avoid immediate re-preview after execution: it can race with dialog/layout
    // teardown and make the virtualized table read a zero height. Clear the stale
    // preview and keep the execution result visible; admins can preview again.
    previewResult.value = null
  } catch (error: any) {
    appStore.showError(error?.message || t('admin.accountCleanup.executeFailed'))
  } finally {
    executing.value = false
  }
}

const handlePageChange = (page: number) => {
  form.page = page
  handlePreview()
}

const handlePageSizeChange = (pageSize: number) => {
  form.pageSize = pageSize
  form.page = 1
  handlePreview()
}

const resetForm = () => {
  form.sourceGroupId = null
  form.targetGroupId = null
  form.statuses = ['error', 'inactive']
  form.action = 'move'
  form.platform = ''
  form.type = ''
  form.search = ''
  form.page = 1
  form.pageSize = 50
  form.limit = 1000
  previewResult.value = null
  executeResult.value = null
}

const isStatusSelected = (status: AccountCleanupStatus) => form.statuses.includes(status)
const toggleStatus = (status: AccountCleanupStatus) => {
  if (isStatusSelected(status)) {
    form.statuses = form.statuses.filter(item => item !== status)
  } else {
    form.statuses = [...form.statuses, status]
  }
}

const statusLabel = (status: string) => {
  const key = status === 'disabled' ? 'inactive' : status
  const labels: Record<string, string> = {
    active: t('admin.accounts.status.active'),
    inactive: t('admin.accounts.status.inactive'),
    error: t('admin.accounts.status.error'),
    rate_limited: t('admin.accounts.status.rateLimited'),
    temp_unschedulable: t('admin.accounts.status.tempUnschedulable'),
    unschedulable: t('admin.accounts.status.unschedulable')
  }
  return labels[key] || status
}

const statusClass = (status: string) => {
  if (status === 'error') return 'bg-rose-100 text-rose-700 dark:bg-rose-500/20 dark:text-rose-200'
  if (status === 'inactive' || status === 'disabled') return 'bg-gray-200 text-gray-700 dark:bg-dark-700 dark:text-gray-200'
  return 'bg-amber-100 text-amber-700 dark:bg-amber-500/20 dark:text-amber-200'
}

const platformLabel = (platform: string) => platformOptions.value.find(option => option.value === platform)?.label || platform
const typeLabel = (type: string) => typeOptions.value.find(option => option.value === type)?.label || type
const reasonLabel = (reason: string) => {
  if (reason.startsWith('status:')) return t('admin.accountCleanup.reasonStatus', { status: statusLabel(reason.slice('status:'.length)) })
  const labels: Record<string, string> = {
    unschedulable: t('admin.accounts.status.unschedulable'),
    rate_limited: t('admin.accounts.status.rateLimited'),
    temp_unschedulable: t('admin.accounts.status.tempUnschedulable')
  }
  return labels[reason] || reason
}

const formatDateTime = (value?: string | null) => value ? formatDate(value) : '-'

onMounted(() => {
  loadGroups()
})
</script>

<style scoped>
.account-cleanup-table :deep(.table-wrapper) {
  height: min(62vh, 640px);
  min-height: 420px;
}

@media (max-width: 1023px) {
  .account-cleanup-table :deep(.table-wrapper) {
    height: auto;
    min-height: 0;
  }
}
</style>
