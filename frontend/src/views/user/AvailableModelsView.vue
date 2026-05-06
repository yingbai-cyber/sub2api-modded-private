<template>
  <AppLayout>
    <TablePageLayout>
      <template #actions>
        <div class="grid gap-4 md:grid-cols-2 xl:grid-cols-4">
          <div class="card p-5 md:col-span-2 xl:col-span-1">
            <div class="flex items-start gap-3">
              <div class="flex h-11 w-11 flex-shrink-0 items-center justify-center rounded-xl bg-primary-100 dark:bg-primary-900/30">
                <Icon name="sparkles" size="lg" class="text-primary-600 dark:text-primary-400" />
              </div>
              <div class="min-w-0">
                <h1 class="text-lg font-semibold text-gray-900 dark:text-white">{{ t('availableModels.title') }}</h1>
                <p class="mt-1 text-sm text-gray-500 dark:text-dark-400">
                  {{ t('availableModels.description') }}
                </p>
              </div>
            </div>
          </div>

          <div class="card p-5">
            <p class="text-sm font-medium text-gray-500 dark:text-dark-400">{{ t('availableModels.stats.models') }}</p>
            <p class="mt-2 text-2xl font-semibold text-gray-900 dark:text-white">{{ summary.modelCount }}</p>
          </div>

          <div class="card p-5">
            <p class="text-sm font-medium text-gray-500 dark:text-dark-400">{{ t('availableModels.stats.channels') }}</p>
            <p class="mt-2 text-2xl font-semibold text-gray-900 dark:text-white">{{ summary.channelCount }}</p>
          </div>

          <div class="card p-5">
            <p class="text-sm font-medium text-gray-500 dark:text-dark-400">{{ t('availableModels.stats.platforms') }}</p>
            <p class="mt-2 text-2xl font-semibold text-gray-900 dark:text-white">{{ summary.platformCount }}</p>
          </div>
        </div>
      </template>

      <template #filters>
        <div class="flex flex-col justify-between gap-4 lg:flex-row lg:items-start">
          <div class="flex flex-1 flex-wrap items-center gap-3">
            <div class="relative w-full sm:w-80">
              <Icon
                name="search"
                size="md"
                class="absolute left-3 top-1/2 -translate-y-1/2 text-gray-400 dark:text-gray-500"
              />
              <input
                v-model="searchQuery"
                type="text"
                :placeholder="t('availableModels.searchPlaceholder')"
                class="input pl-10"
              />
            </div>
            <span class="text-xs text-gray-500 dark:text-dark-400">
              {{ t('availableModels.filteredCount', { count: filteredRows.length, total: allRows.length }) }}
            </span>
          </div>

          <div class="flex w-full flex-shrink-0 flex-wrap items-center justify-end gap-3 lg:w-auto">
            <button
              @click="loadChannels"
              :disabled="loading"
              class="btn btn-secondary"
              :title="t('common.refresh', 'Refresh')"
            >
              <Icon name="refresh" size="md" :class="loading ? 'animate-spin' : ''" />
            </button>
          </div>
        </div>
      </template>

      <template #table>
        <AvailableModelsTable
          :columns="columnLabels"
          :rows="filteredRows"
          :loading="loading"
          :user-group-rates="userGroupRates"
          pricing-key-prefix="availableChannels.pricing"
          :no-pricing-label="t('availableChannels.noPricing')"
          :no-models-label="t('availableChannels.noModels')"
          :empty-label="t('availableModels.empty')"
        />
      </template>
    </TablePageLayout>
  </AppLayout>
</template>

<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import AppLayout from '@/components/layout/AppLayout.vue'
import TablePageLayout from '@/components/layout/TablePageLayout.vue'
import Icon from '@/components/icons/Icon.vue'
import AvailableModelsTable from '@/components/channels/AvailableModelsTable.vue'
import userChannelsAPI, { type UserAvailableChannel } from '@/api/channels'
import userGroupsAPI from '@/api/groups'
import { useAppStore } from '@/stores/app'
import { extractApiErrorMessage } from '@/utils/apiError'
import {
  filterAvailableModelRows,
  flattenAvailableModelRows,
  summarizeAvailableModels,
} from './availableModels'

const { t } = useI18n()
const appStore = useAppStore()

const channels = ref<UserAvailableChannel[]>([])
const userGroupRates = ref<Record<number, number>>({})
const loading = ref(false)
const searchQuery = ref('')

const columnLabels = computed(() => ({
  model: t('availableModels.columns.model'),
  platform: t('availableModels.columns.platform'),
  channel: t('availableModels.columns.channel'),
  groups: t('availableModels.columns.groups'),
  pricing: t('availableModels.columns.pricing'),
}))

const summary = computed(() => summarizeAvailableModels(channels.value))
const allRows = computed(() => flattenAvailableModelRows(channels.value))
const filteredRows = computed(() => filterAvailableModelRows(allRows.value, searchQuery.value))

async function loadChannels() {
  loading.value = true
  try {
    const [list, rates] = await Promise.all([
      userChannelsAPI.getAvailable(),
      userGroupsAPI.getUserGroupRates().catch((err: unknown) => {
        console.error('Failed to load user group rates:', err)
        return {} as Record<number, number>
      }),
    ])
    channels.value = list
    userGroupRates.value = rates
  } catch (err: unknown) {
    appStore.showError(extractApiErrorMessage(err, t('common.error')))
  } finally {
    loading.value = false
  }
}

onMounted(loadChannels)
</script>
