<template>
  <div class="table-wrapper">
    <table class="w-full min-w-[900px] divide-y divide-gray-200 dark:divide-dark-700">
      <thead class="bg-gray-50 dark:bg-dark-800">
        <tr>
          <th class="px-5 py-3 text-left text-xs font-medium uppercase tracking-wider text-gray-500 dark:text-dark-400">
            {{ columns.model }}
          </th>
          <th class="px-5 py-3 text-left text-xs font-medium uppercase tracking-wider text-gray-500 dark:text-dark-400">
            {{ columns.platform }}
          </th>
          <th class="px-5 py-3 text-left text-xs font-medium uppercase tracking-wider text-gray-500 dark:text-dark-400">
            {{ columns.channel }}
          </th>
          <th class="px-5 py-3 text-left text-xs font-medium uppercase tracking-wider text-gray-500 dark:text-dark-400">
            {{ columns.groups }}
          </th>
          <th class="px-5 py-3 text-left text-xs font-medium uppercase tracking-wider text-gray-500 dark:text-dark-400">
            {{ columns.pricing }}
          </th>
        </tr>
      </thead>

      <tbody v-if="loading" class="divide-y divide-gray-200 bg-white dark:divide-dark-700 dark:bg-dark-900">
        <tr v-for="i in 5" :key="i">
          <td v-for="column in 5" :key="column" class="px-5 py-4">
            <div class="h-4 w-3/4 animate-pulse rounded bg-gray-200 dark:bg-dark-700"></div>
          </td>
        </tr>
      </tbody>

      <tbody v-else-if="rows.length === 0" class="bg-white dark:bg-dark-900">
        <tr>
          <td colspan="5" class="px-5 py-12 text-center text-gray-500 dark:text-dark-400">
            <Icon name="inbox" size="xl" class="mx-auto mb-3 h-12 w-12 text-gray-400 dark:text-dark-500" />
            <p class="text-sm">{{ emptyLabel }}</p>
          </td>
        </tr>
      </tbody>

      <tbody v-else class="divide-y divide-gray-200 bg-white dark:divide-dark-700 dark:bg-dark-900">
        <tr
          v-for="row in rows"
          :key="row.id"
          class="transition-colors hover:bg-gray-50/60 dark:hover:bg-dark-800/60"
        >
          <td class="px-5 py-4 align-top">
            <SupportedModelChip
              v-if="row.model.name"
              :model="row.model"
              :pricing-key-prefix="pricingKeyPrefix"
              :no-pricing-label="noPricingLabel"
              :platform-hint="row.platform"
              :show-platform="false"
            />
            <span v-else class="text-xs text-gray-400">{{ noModelsLabel }}</span>
          </td>

          <td class="px-5 py-4 align-top">
            <span
              :class="[
                'inline-flex items-center gap-1 rounded-md border px-2 py-0.5 text-[11px] font-medium uppercase',
                platformBadgeClass(row.platform),
              ]"
            >
              <PlatformIcon :platform="row.platform as GroupPlatform" size="xs" />
              {{ row.platform }}
            </span>
          </td>

          <td class="px-5 py-4 align-top">
            <div class="max-w-[240px]">
              <p class="font-medium text-gray-900 dark:text-white">{{ row.channelName }}</p>
              <p v-if="row.channelDescription" class="mt-1 line-clamp-2 text-xs text-gray-500 dark:text-dark-400">
                {{ row.channelDescription }}
              </p>
            </div>
          </td>

          <td class="px-5 py-4 align-top">
            <div class="flex flex-col gap-1.5">
              <div v-if="exclusiveGroups(row).length > 0" class="flex flex-wrap items-center gap-1.5">
                <span
                  class="inline-flex items-center gap-0.5 text-[10px] font-medium uppercase text-purple-600 dark:text-purple-400"
                  :title="t('availableChannels.exclusiveTooltip')"
                >
                  <Icon name="shield" size="xs" class="h-3 w-3" />
                  {{ t('availableChannels.exclusive') }}
                </span>
                <GroupBadge
                  v-for="group in exclusiveGroups(row)"
                  :key="`ex-${row.id}-${group.id}`"
                  :name="group.name"
                  :platform="group.platform as GroupPlatform"
                  :subscription-type="(group.subscription_type || 'standard') as SubscriptionType"
                  :rate-multiplier="group.rate_multiplier"
                  :user-rate-multiplier="userGroupRates[group.id] ?? null"
                  always-show-rate
                />
              </div>

              <div v-if="publicGroups(row).length > 0" class="flex flex-wrap items-center gap-1.5">
                <span
                  class="inline-flex items-center gap-0.5 text-[10px] font-medium uppercase text-gray-500 dark:text-gray-400"
                  :title="t('availableChannels.publicTooltip')"
                >
                  <Icon name="globe" size="xs" class="h-3 w-3" />
                  {{ t('availableChannels.public') }}
                </span>
                <GroupBadge
                  v-for="group in publicGroups(row)"
                  :key="`pub-${row.id}-${group.id}`"
                  :name="group.name"
                  :platform="group.platform as GroupPlatform"
                  :subscription-type="(group.subscription_type || 'standard') as SubscriptionType"
                  :rate-multiplier="group.rate_multiplier"
                  :user-rate-multiplier="userGroupRates[group.id] ?? null"
                  always-show-rate
                />
              </div>

              <span v-if="row.groups.length === 0" class="text-xs text-gray-400">-</span>
            </div>
          </td>

          <td class="px-5 py-4 align-top text-xs text-gray-600 dark:text-dark-300">
            <template v-if="row.model.pricing">
              <span class="inline-flex items-center rounded-md bg-gray-100 px-2 py-1 font-medium text-gray-700 dark:bg-dark-700 dark:text-dark-200">
                {{ pricingModeLabel(row.model.pricing.billing_mode) }}
              </span>
            </template>
            <span v-else class="text-gray-400">{{ noPricingLabel }}</span>
          </td>
        </tr>
      </tbody>
    </table>
  </div>
</template>

<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import Icon from '@/components/icons/Icon.vue'
import PlatformIcon from '@/components/common/PlatformIcon.vue'
import GroupBadge from '@/components/common/GroupBadge.vue'
import SupportedModelChip from './SupportedModelChip.vue'
import type { UserAvailableGroup } from '@/api/channels'
import type { GroupPlatform, SubscriptionType } from '@/types'
import { platformBadgeClass } from '@/utils/platformColors'
import {
  BILLING_MODE_IMAGE,
  BILLING_MODE_PER_REQUEST,
  BILLING_MODE_TOKEN,
  type BillingMode,
} from '@/constants/channel'
import type { AvailableModelRow } from '@/views/user/availableModels'

const props = defineProps<{
  columns: {
    model: string
    platform: string
    channel: string
    groups: string
    pricing: string
  }
  rows: AvailableModelRow[]
  loading: boolean
  pricingKeyPrefix: string
  noPricingLabel: string
  noModelsLabel: string
  emptyLabel: string
  userGroupRates: Record<number, number>
}>()

void props.userGroupRates

const { t } = useI18n()

function exclusiveGroups(row: AvailableModelRow): UserAvailableGroup[] {
  return row.groups.filter((group) => group.is_exclusive)
}

function publicGroups(row: AvailableModelRow): UserAvailableGroup[] {
  return row.groups.filter((group) => !group.is_exclusive)
}

function pricingModeLabel(mode: BillingMode): string {
  switch (mode) {
    case BILLING_MODE_TOKEN:
      return t(`${props.pricingKeyPrefix}.billingModeToken`)
    case BILLING_MODE_PER_REQUEST:
      return t(`${props.pricingKeyPrefix}.billingModePerRequest`)
    case BILLING_MODE_IMAGE:
      return t(`${props.pricingKeyPrefix}.billingModeImage`)
    default:
      return '-'
  }
}
</script>
