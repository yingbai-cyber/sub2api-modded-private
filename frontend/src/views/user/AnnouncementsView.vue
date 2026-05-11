<template>
  <AppLayout>
    <div class="mx-auto max-w-5xl space-y-6">
      <div class="flex flex-col gap-4 sm:flex-row sm:items-center sm:justify-between">
        <div>
          <h2 class="text-xl font-semibold text-gray-900 dark:text-white">{{ t('announcements.title') }}</h2>
          <p class="mt-1 text-sm text-gray-500 dark:text-gray-400">{{ t('announcements.description') }}</p>
        </div>
        <div class="flex items-center gap-2">
          <button
            v-if="unreadCount > 0"
            class="btn btn-secondary"
            :disabled="loading"
            @click="markAllAsRead"
          >
            {{ t('announcements.markAllRead') }}
          </button>
          <button class="btn btn-primary" :disabled="loading" @click="refreshAnnouncements">
            {{ t('common.refresh') }}
          </button>
        </div>
      </div>

      <div class="grid gap-4 sm:grid-cols-3">
        <div class="card p-4">
          <p class="text-sm text-gray-500 dark:text-gray-400">{{ t('announcements.total') }}</p>
          <p class="mt-2 text-2xl font-bold text-gray-900 dark:text-white">{{ announcements.length }}</p>
        </div>
        <div class="card p-4">
          <p class="text-sm text-gray-500 dark:text-gray-400">{{ t('announcements.unread') }}</p>
          <p class="mt-2 text-2xl font-bold text-blue-600 dark:text-blue-400">{{ unreadCount }}</p>
        </div>
        <div class="card p-4">
          <p class="text-sm text-gray-500 dark:text-gray-400">{{ t('announcements.read') }}</p>
          <p class="mt-2 text-2xl font-bold text-emerald-600 dark:text-emerald-400">{{ readCount }}</p>
        </div>
      </div>

      <div v-if="loading" class="flex items-center justify-center py-20">
        <div class="h-8 w-8 animate-spin rounded-full border-4 border-primary-500 border-t-transparent"></div>
      </div>

      <div v-else-if="announcements.length === 0" class="card p-12 text-center">
        <div class="mx-auto mb-4 flex h-16 w-16 items-center justify-center rounded-full bg-gray-100 dark:bg-dark-700">
          <Icon name="inbox" size="xl" class="text-gray-400" />
        </div>
        <h3 class="mb-2 text-lg font-semibold text-gray-900 dark:text-white">{{ t('announcements.empty') }}</h3>
        <p class="text-sm text-gray-500 dark:text-gray-400">{{ t('announcements.emptyDescription') }}</p>
      </div>

      <div v-else class="space-y-4">
        <article
          v-for="item in announcements"
          :key="item.id"
          class="card overflow-hidden transition-all hover:shadow-lg"
          :class="{ 'ring-1 ring-blue-200 dark:ring-blue-900/60': !item.read_at }"
        >
          <button
            type="button"
            class="flex w-full items-start gap-4 p-5 text-left"
            @click="toggleExpanded(item.id)"
          >
            <div
              class="mt-1 flex h-10 w-10 shrink-0 items-center justify-center rounded-xl"
              :class="item.read_at ? 'bg-gray-100 text-gray-400 dark:bg-dark-700 dark:text-gray-500' : 'bg-blue-100 text-blue-600 dark:bg-blue-900/40 dark:text-blue-300'"
            >
              <Icon :name="item.read_at ? 'check' : 'bell'" size="md" />
            </div>
            <div class="min-w-0 flex-1">
              <div class="flex flex-wrap items-center gap-2">
                <h3 class="text-base font-semibold text-gray-900 dark:text-white">{{ item.title }}</h3>
                <span
                  class="rounded-full px-2 py-0.5 text-xs font-medium"
                  :class="item.read_at ? 'bg-gray-100 text-gray-600 dark:bg-dark-700 dark:text-gray-300' : 'bg-blue-100 text-blue-700 dark:bg-blue-900/40 dark:text-blue-300'"
                >
                  {{ item.read_at ? t('announcements.read') : t('announcements.unread') }}
                </span>
              </div>
              <div class="mt-2 flex flex-wrap gap-x-4 gap-y-1 text-xs text-gray-500 dark:text-gray-400">
                <span>{{ formatRelativeWithDateTime(item.created_at) }}</span>
                <span v-if="item.ends_at">{{ t('announcements.endsAt') }}: {{ formatDateTime(item.ends_at) }}</span>
                <span v-if="item.read_at">{{ t('announcements.readAt') }}: {{ formatDateTime(item.read_at) }}</span>
              </div>
            </div>
            <ChevronDownIcon
              class="mt-2 h-5 w-5 shrink-0 text-gray-400 transition-transform"
              :class="expandedIds.has(item.id) ? 'rotate-180' : ''"
            />
          </button>

          <div v-if="expandedIds.has(item.id)" class="border-t border-gray-100 px-5 py-5 dark:border-dark-700">
            <div class="markdown-body prose prose-sm max-w-none dark:prose-invert" v-html="renderMarkdown(item.content)"></div>
            <div class="mt-5 flex justify-end">
              <button
                v-if="!item.read_at"
                class="btn btn-primary"
                @click="markAsRead(item.id)"
              >
                {{ t('announcements.markRead') }}
              </button>
            </div>
          </div>
        </article>
      </div>
    </div>
  </AppLayout>
</template>

<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { storeToRefs } from 'pinia'
import { marked } from 'marked'
import DOMPurify from 'dompurify'
import AppLayout from '@/components/layout/AppLayout.vue'
import Icon from '@/components/icons/Icon.vue'
import { useAppStore } from '@/stores/app'
import { useAnnouncementStore } from '@/stores/announcements'
import { formatDateTime, formatRelativeWithDateTime } from '@/utils/format'

const { t } = useI18n()
const appStore = useAppStore()
const announcementStore = useAnnouncementStore()
const { announcements, loading } = storeToRefs(announcementStore)
const expandedIds = ref(new Set<number>())

const unreadCount = computed(() => announcementStore.unreadCount)
const readCount = computed(() => announcements.value.length - unreadCount.value)

marked.setOptions({
  breaks: true,
  gfm: true,
})

function renderMarkdown(content: string): string {
  if (!content) return ''
  const html = marked.parse(content) as string
  return DOMPurify.sanitize(html)
}

function toggleExpanded(id: number) {
  if (expandedIds.value.has(id)) {
    expandedIds.value.delete(id)
  } else {
    expandedIds.value.add(id)
  }
}

async function refreshAnnouncements() {
  await announcementStore.fetchAnnouncements(true)
}

async function markAsRead(id: number) {
  try {
    await announcementStore.markAsRead(id)
    appStore.showSuccess(t('announcements.markedAsRead'))
  } catch (err: any) {
    appStore.showError(err?.message || t('common.unknownError'))
  }
}

async function markAllAsRead() {
  try {
    await announcementStore.markAllAsRead()
    appStore.showSuccess(t('announcements.allMarkedAsRead'))
  } catch (err: any) {
    appStore.showError(err?.message || t('common.unknownError'))
  }
}

onMounted(() => {
  refreshAnnouncements()
})
</script>
