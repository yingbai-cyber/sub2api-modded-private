<template>
  <AppLayout>
    <div class="flex min-h-[calc(100vh-8rem)] flex-col gap-4">
      <section class="card p-4">
        <div class="flex flex-col gap-4 xl:flex-row xl:items-center xl:justify-between">
          <div class="flex items-start gap-3">
            <div class="flex h-12 w-12 flex-shrink-0 items-center justify-center rounded-2xl bg-gradient-to-br from-primary-500 to-purple-600 text-white shadow-lg shadow-primary-500/25">
              <Icon name="sparkles" size="lg" :stroke-width="1.8" />
            </div>
            <div class="min-w-0">
              <div class="mb-1 flex flex-wrap items-center gap-2">
                <span class="rounded-full bg-primary-50 px-2.5 py-1 text-xs font-semibold text-primary-700 dark:bg-primary-900/30 dark:text-primary-300">
                  {{ t('imageGeneration.badge') }}
                </span>
                <span class="text-xs text-gray-500 dark:text-dark-400">{{ t('imageGeneration.sessionOnly') }}</span>
              </div>
              <h2 class="text-2xl font-bold text-gray-900 dark:text-white">{{ t('imageGeneration.title') }}</h2>
              <p class="mt-1 max-w-3xl text-sm text-gray-600 dark:text-dark-300">{{ t('imageGeneration.description') }}</p>
            </div>
          </div>

          <div class="grid w-full gap-3 sm:grid-cols-[minmax(220px,1fr)_auto_auto] xl:w-auto xl:min-w-[620px]">
            <label class="block">
              <span class="mb-1.5 block text-xs font-medium text-gray-600 dark:text-dark-300">{{ t('imageGeneration.apiKey') }}</span>
              <select
                v-model="selectedKeyId"
                class="input h-10"
                :disabled="keysLoading || apiKeys.length === 0"
                @change="refreshCapability"
              >
                <option :value="null">{{ t('imageGeneration.selectApiKey') }}</option>
                <option v-for="key in apiKeys" :key="key.id" :value="key.id">
                  {{ key.name }} · {{ key.group?.name || 'OpenAI' }}
                </option>
              </select>
            </label>

            <div class="flex items-end">
              <button
                type="button"
                class="btn btn-secondary h-10 min-w-24"
                :disabled="keysLoading"
                @click="loadApiKeys"
              >
                <Icon name="refresh" size="sm" :class="keysLoading ? 'animate-spin' : ''" />
                {{ t('common.refresh') }}
              </button>
            </div>

            <div class="flex items-end">
              <div class="flex h-10 min-w-[150px] items-center justify-center gap-2 rounded-xl border px-3 text-sm font-medium" :class="capabilityStatusClass">
                <Icon :name="capabilityStatusIcon" size="sm" />
                <span>{{ capabilityStatusText }}</span>
              </div>
            </div>
          </div>
        </div>

        <div v-if="apiKeys.length === 0 && !keysLoading" class="mt-4 rounded-2xl border border-amber-200 bg-amber-50 p-3 text-sm text-amber-800 dark:border-amber-900/60 dark:bg-amber-900/20 dark:text-amber-200">
          <div class="font-medium">{{ t('imageGeneration.noOpenAIKeys') }}</div>
          <div class="mt-1 text-xs opacity-90">{{ t('imageGeneration.noOpenAIKeysHint') }}</div>
        </div>

        <div v-if="capability?.warnings?.length" class="mt-4 rounded-2xl border border-amber-200 bg-amber-50 p-3 text-xs text-amber-800 dark:border-amber-900/60 dark:bg-amber-900/20 dark:text-amber-200">
          <div v-for="warning in capability.warnings" :key="warning">{{ warning }}</div>
        </div>
      </section>

      <section class="grid min-h-0 flex-1 grid-cols-1 gap-4 lg:grid-cols-[230px_1fr]">
        <aside class="card flex min-h-[320px] flex-col p-3">
          <div class="flex items-center justify-between gap-3">
            <div>
              <h3 class="text-sm font-semibold text-gray-900 dark:text-white">{{ t('imageGeneration.results') }}</h3>
              <p class="text-xs text-gray-500 dark:text-dark-400">{{ results.length }} {{ t('common.total') }}</p>
            </div>
            <button
              v-if="results.length"
              type="button"
              class="btn btn-secondary h-9 min-w-[86px] px-3 text-xs"
              @click="clearResults"
            >
              {{ t('imageGeneration.clearResults') }}
            </button>
          </div>

          <div v-if="results.length" class="mt-3 grid gap-3 overflow-y-auto pr-1 lg:max-h-[calc(100vh-27rem)]">
            <button
              v-for="image in results"
              :key="image.id"
              type="button"
              class="group rounded-2xl border p-2 text-left transition-all hover:-translate-y-0.5 hover:shadow-card-hover"
              :class="selectedImageId === image.id ? 'border-primary-400 bg-primary-50/70 dark:border-primary-500 dark:bg-primary-900/20' : 'border-gray-100 bg-white dark:border-dark-700 dark:bg-dark-800/70'"
              @click="selectedImageId = image.id"
            >
              <img :src="image.src" :alt="image.prompt" class="aspect-square w-full rounded-xl object-cover" />
              <div class="mt-2 line-clamp-2 text-xs text-gray-600 dark:text-dark-300">{{ image.prompt }}</div>
              <div class="mt-1 text-[11px] text-gray-400 dark:text-dark-500">{{ image.endpointLabel }}</div>
            </button>
          </div>

          <div v-else class="flex flex-1 flex-col items-center justify-center rounded-2xl border border-dashed border-gray-200 p-4 text-center dark:border-dark-700">
            <Icon name="inbox" size="xl" class="text-gray-300 dark:text-dark-600" />
            <p class="mt-3 text-sm font-medium text-gray-700 dark:text-dark-200">{{ t('imageGeneration.noResultsTitle') }}</p>
            <p class="mt-1 text-xs text-gray-500 dark:text-dark-400">{{ t('imageGeneration.noResultsHint') }}</p>
          </div>
        </aside>

        <main class="card flex min-h-[420px] flex-col p-4">
          <div class="flex flex-col gap-3 border-b border-gray-100 pb-4 dark:border-dark-700 sm:flex-row sm:items-center sm:justify-between">
            <div class="min-w-0">
              <h3 class="text-base font-semibold text-gray-900 dark:text-white">{{ t('imageGeneration.preview') }}</h3>
              <p class="mt-1 line-clamp-2 text-sm text-gray-500 dark:text-dark-400">
                {{ selectedImage?.revisedPrompt || selectedImage?.prompt || t('imageGeneration.noPreviewHint') }}
              </p>
            </div>
            <div class="flex flex-wrap items-center gap-2">
              <span class="inline-flex h-10 items-center rounded-xl bg-gray-100 px-3 text-xs font-medium text-gray-600 dark:bg-dark-700 dark:text-dark-300">
                {{ endpointLabel }}
              </span>
              <button
                type="button"
                class="btn btn-secondary h-10 min-w-28"
                :disabled="!selectedImage"
                @click="downloadCurrentImage"
              >
                <Icon name="download" size="sm" />
                {{ t('imageGeneration.download') }}
              </button>
            </div>
          </div>

          <div class="mt-4 flex min-h-[340px] flex-1 items-center justify-center overflow-hidden rounded-3xl bg-[radial-gradient(circle_at_top,_rgba(99,102,241,0.14),_transparent_38%),linear-gradient(135deg,_rgba(255,255,255,0.9),_rgba(243,244,246,0.8))] p-4 dark:bg-[radial-gradient(circle_at_top,_rgba(99,102,241,0.18),_transparent_40%),linear-gradient(135deg,_rgba(17,24,39,0.8),_rgba(2,6,23,0.9))]">
            <img
              v-if="selectedImage"
              :src="selectedImage.src"
              :alt="selectedImage.prompt"
              class="max-h-[62vh] max-w-full rounded-2xl object-contain shadow-2xl"
            />
            <div v-else class="max-w-sm text-center">
              <Icon name="sparkles" size="xl" class="mx-auto text-primary-300 dark:text-primary-500" />
              <p class="mt-3 text-base font-semibold text-gray-800 dark:text-white">{{ t('imageGeneration.noPreviewTitle') }}</p>
              <p class="mt-2 text-sm text-gray-500 dark:text-dark-400">{{ t('imageGeneration.noPreviewHint') }}</p>
            </div>
          </div>
        </main>
      </section>

      <section class="card overflow-hidden">
        <button
          type="button"
          class="flex h-14 w-full items-center justify-between gap-4 px-4 text-left transition-colors hover:bg-gray-50 dark:hover:bg-dark-800/70"
          @click="promptPanelOpen = !promptPanelOpen"
        >
          <div class="min-w-0">
            <div class="text-sm font-semibold text-gray-900 dark:text-white">{{ t('imageGeneration.promptPanel') }}</div>
            <div class="truncate text-xs text-gray-500 dark:text-dark-400">{{ advancedSummary }}</div>
          </div>
          <Icon :name="promptPanelOpen ? 'chevronDown' : 'chevronUp'" size="sm" class="flex-shrink-0 text-gray-400" />
        </button>

        <div v-if="promptPanelOpen" class="border-t border-gray-100 p-4 dark:border-dark-700">
          <div class="grid gap-4 lg:grid-cols-[1fr_340px]">
            <label class="block">
              <span class="input-label">{{ t('imageGeneration.promptLabel') }}</span>
              <textarea
                v-model="prompt"
                class="input min-h-[138px] resize-y"
                :placeholder="t('imageGeneration.promptPlaceholder')"
                :disabled="generating"
              ></textarea>
            </label>

            <div class="space-y-3">
              <div v-if="supportsReferenceUpload" class="rounded-2xl border border-gray-100 bg-gray-50 p-3 dark:border-dark-700 dark:bg-dark-900/40">
                <div class="mb-3 flex items-center justify-between gap-3">
                  <div>
                    <div class="text-sm font-medium text-gray-800 dark:text-dark-100">{{ t('imageGeneration.referenceImages') }}</div>
                    <div class="text-xs text-gray-500 dark:text-dark-400">{{ t('imageGeneration.uploadHint') }}</div>
                  </div>
                  <label class="btn btn-secondary h-10 min-w-[118px] cursor-pointer">
                    <Icon name="upload" size="sm" />
                    {{ t('imageGeneration.uploadReference') }}
                    <input
                      ref="fileInputRef"
                      type="file"
                      accept="image/*"
                      multiple
                      class="hidden"
                      :disabled="generating"
                      @change="handleReferenceUpload"
                    />
                  </label>
                </div>

                <div v-if="referenceImages.length" class="grid grid-cols-4 gap-2">
                  <div v-for="image in referenceImages" :key="image.id" class="group relative overflow-hidden rounded-xl border border-gray-200 bg-white dark:border-dark-700 dark:bg-dark-800">
                    <img :src="image.previewUrl" :alt="image.name" class="aspect-square w-full object-cover" />
                    <button
                      type="button"
                      class="absolute right-1 top-1 flex h-8 w-8 items-center justify-center rounded-lg bg-black/60 text-white opacity-0 transition-opacity group-hover:opacity-100"
                      :title="t('imageGeneration.removeReference')"
                      @click="removeReferenceImage(image.id)"
                    >
                      <Icon name="x" size="sm" />
                    </button>
                  </div>
                </div>
              </div>

              <div v-else class="rounded-2xl border border-dashed border-gray-200 bg-gray-50 p-3 text-sm text-gray-500 dark:border-dark-700 dark:bg-dark-900/40 dark:text-dark-400">
                {{ t('imageGeneration.unsupportedUpload') }}
              </div>

              <div class="rounded-2xl border border-gray-100 bg-white p-3 dark:border-dark-700 dark:bg-dark-800/60">
                <div class="mb-3 text-sm font-medium text-gray-800 dark:text-dark-100">{{ t('imageGeneration.config') }}</div>
                <div class="flex flex-wrap gap-2">
                  <button
                    type="button"
                    class="btn btn-secondary h-10 min-w-[148px]"
                    :disabled="!supportsAdvancedOptions"
                    :title="advancedButtonTitle"
                    @click="openAdvancedSettings"
                  >
                    <Icon name="cog" size="sm" />
                    {{ t('imageGeneration.advancedSettings') }}
                  </button>
                  <button
                    type="button"
                    class="btn btn-secondary h-10 min-w-24"
                    :disabled="generating || (!prompt && referenceImages.length === 0)"
                    @click="clearPromptAndReferences"
                  >
                    {{ t('common.reset') }}
                  </button>
                </div>
                <p v-if="!supportsAdvancedOptions" class="mt-2 text-xs text-amber-600 dark:text-amber-300">
                  {{ t('imageGeneration.advancedLockedHint') }}
                </p>
              </div>
            </div>
          </div>

          <div class="mt-4 flex flex-col gap-3 border-t border-gray-100 pt-4 dark:border-dark-700 sm:flex-row sm:items-center sm:justify-between">
            <div class="text-xs text-gray-500 dark:text-dark-400">
              {{ t('imageGeneration.endpoint') }}: <span class="font-medium text-gray-700 dark:text-dark-200">{{ endpointLabel }}</span>
            </div>
            <button
              type="button"
              class="btn btn-primary h-11 min-w-[160px]"
              :disabled="!canGenerate"
              @click="generateImage"
            >
              <Icon name="sparkles" size="sm" :class="generating ? 'animate-pulse' : ''" />
              {{ generating ? t('imageGeneration.generating') : t('imageGeneration.generate') }}
            </button>
          </div>
        </div>
      </section>
    </div>

    <BaseDialog
      :show="advancedModalOpen"
      :title="t('imageGeneration.advancedSettings')"
      width="wide"
      @close="advancedModalOpen = false"
    >
      <div class="space-y-5">
        <div class="rounded-2xl border border-primary-100 bg-primary-50 p-3 text-sm text-primary-800 dark:border-primary-900/50 dark:bg-primary-900/20 dark:text-primary-200">
          {{ t('imageGeneration.advancedModalHint') }}
        </div>

        <div class="grid gap-4 md:grid-cols-2">
          <label class="block">
            <span class="input-label">{{ t('imageGeneration.model') }}</span>
            <input v-model.trim="advancedSettings.model" class="input" type="text" placeholder="gpt-image-2" />
          </label>

          <label class="block">
            <span class="input-label">{{ t('imageGeneration.size') }}</span>
            <select v-model="advancedSettings.size" class="input">
              <option value="auto">{{ t('imageGeneration.optionAuto') }}</option>
              <option value="1024x1024">1024 × 1024</option>
              <option value="1536x1024">1536 × 1024</option>
              <option value="1024x1536">1024 × 1536</option>
              <option value="2048x2048">2048 × 2048</option>
            </select>
          </label>

          <label class="block">
            <span class="input-label">{{ t('imageGeneration.count') }}</span>
            <input
              v-model.number="advancedSettings.n"
              class="input"
              type="number"
              min="1"
              :max="maxImageCount"
              @blur="normalizeImageCount"
            />
            <p class="input-hint">{{ t('imageGeneration.maxCountHint', { count: maxImageCount }) }}</p>
          </label>

          <label class="block">
            <span class="input-label">{{ t('imageGeneration.responseFormat') }}</span>
            <select v-model="advancedSettings.responseFormat" class="input">
              <option value="b64_json">{{ t('imageGeneration.responseFormatB64') }}</option>
              <option value="url">{{ t('imageGeneration.responseFormatUrl') }}</option>
            </select>
          </label>

          <label class="block">
            <span class="input-label">{{ t('imageGeneration.quality') }}</span>
            <select v-model="advancedSettings.quality" class="input" :disabled="capability?.supports_quality === false">
              <option value="auto">{{ t('imageGeneration.optionAuto') }}</option>
              <option value="low">{{ t('imageGeneration.optionLow') }}</option>
              <option value="medium">{{ t('imageGeneration.optionMedium') }}</option>
              <option value="high">{{ t('imageGeneration.optionHigh') }}</option>
            </select>
          </label>

          <label class="block">
            <span class="input-label">{{ t('imageGeneration.outputFormat') }}</span>
            <select v-model="advancedSettings.outputFormat" class="input" :disabled="capability?.supports_output_format === false">
              <option value="auto">{{ t('imageGeneration.optionAuto') }}</option>
              <option value="png">PNG</option>
              <option value="jpeg">JPEG</option>
              <option value="webp">WebP</option>
            </select>
          </label>

          <label class="block">
            <span class="input-label">{{ t('imageGeneration.background') }}</span>
            <select v-model="advancedSettings.background" class="input">
              <option value="auto">{{ t('imageGeneration.optionAuto') }}</option>
              <option value="transparent">{{ t('imageGeneration.optionTransparent') }}</option>
              <option value="opaque">{{ t('imageGeneration.optionOpaque') }}</option>
            </select>
          </label>

          <label class="block">
            <span class="input-label">{{ t('imageGeneration.moderation') }}</span>
            <select v-model="advancedSettings.moderation" class="input">
              <option value="auto">{{ t('imageGeneration.optionAuto') }}</option>
              <option value="low">{{ t('imageGeneration.optionLow') }}</option>
            </select>
          </label>

          <label class="block">
            <span class="input-label">{{ t('imageGeneration.inputFidelity') }}</span>
            <select v-model="advancedSettings.inputFidelity" class="input">
              <option value="auto">{{ t('imageGeneration.optionAuto') }}</option>
              <option value="low">{{ t('imageGeneration.optionLow') }}</option>
              <option value="high">{{ t('imageGeneration.optionHigh') }}</option>
            </select>
          </label>

          <label class="block">
            <span class="input-label">{{ t('imageGeneration.outputCompression') }}</span>
            <input
              v-model.number="advancedSettings.outputCompression"
              class="input"
              type="number"
              min="0"
              max="100"
              :disabled="!['jpeg', 'webp'].includes(advancedSettings.outputFormat)"
            />
          </label>
        </div>

        <div v-if="capability?.unsupported_params?.length" class="rounded-2xl border border-gray-100 bg-gray-50 p-3 text-xs text-gray-500 dark:border-dark-700 dark:bg-dark-900/40 dark:text-dark-400">
          {{ t('imageGeneration.unsupportedParams') }}: {{ capability.unsupported_params.join(', ') }}
        </div>
      </div>

      <template #footer>
        <div class="flex justify-end gap-3">
          <button type="button" class="btn btn-secondary h-10 min-w-24" @click="resetAdvancedSettings()">
            {{ t('imageGeneration.resetAdvanced') }}
          </button>
          <button type="button" class="btn btn-primary h-10 min-w-24" @click="advancedModalOpen = false">
            {{ t('imageGeneration.saveAdvanced') }}
          </button>
        </div>
      </template>
    </BaseDialog>
  </AppLayout>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, reactive, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import AppLayout from '@/components/layout/AppLayout.vue'
import BaseDialog from '@/components/common/BaseDialog.vue'
import Icon from '@/components/icons/Icon.vue'
import { keysAPI } from '@/api'
import {
  createImageGeneration,
  createImageMultipart,
  getImageCapability,
  type ImageGenerationCapability,
  type ImageGenerationEndpoint,
  type ImageGenerationPayload,
  type ImageGenerationResponse,
} from '@/api/imageGeneration'
import { useAppStore } from '@/stores/app'
import type { ApiKey } from '@/types'
import { extractApiErrorMessage } from '@/utils/apiError'

type StatusIcon = 'checkCircle' | 'xCircle' | 'refresh' | 'infoCircle'

interface ReferenceImage {
  id: string
  file: File
  name: string
  previewUrl: string
}

interface GeneratedImage {
  id: string
  src: string
  downloadUrl: string
  objectUrl?: string
  prompt: string
  revisedPrompt?: string
  filename: string
  endpointLabel: string
}

interface ImageSource {
  src: string
  downloadUrl: string
  objectUrl?: string
  extension: string
}

interface AdvancedSettings {
  model: string
  size: string
  n: number
  responseFormat: 'b64_json' | 'url'
  quality: string
  outputFormat: string
  background: string
  moderation: string
  inputFidelity: string
  outputCompression: number
}

const { t } = useI18n()
const appStore = useAppStore()

const apiKeys = ref<ApiKey[]>([])
const selectedKeyId = ref<number | null>(null)
const capability = ref<ImageGenerationCapability | null>(null)
const keysLoading = ref(false)
const capabilityLoading = ref(false)
const generating = ref(false)
const promptPanelOpen = ref(true)
const advancedModalOpen = ref(false)
const prompt = ref('')
const results = ref<GeneratedImage[]>([])
const selectedImageId = ref<string | null>(null)
const referenceImages = ref<ReferenceImage[]>([])
const fileInputRef = ref<HTMLInputElement | null>(null)
const capabilityRequestId = ref(0)

const advancedSettings = reactive<AdvancedSettings>({
  model: 'gpt-image-2',
  size: 'auto',
  n: 1,
  responseFormat: 'b64_json',
  quality: 'auto',
  outputFormat: 'auto',
  background: 'auto',
  moderation: 'auto',
  inputFidelity: 'auto',
  outputCompression: 100,
})

const selectedApiKey = computed(() => apiKeys.value.find((key) => key.id === selectedKeyId.value) ?? null)
const selectedApiKeyValue = computed(() => selectedApiKey.value?.key ?? '')
const supportsAdvancedOptions = computed(() => capability.value?.supports_advanced_options === true)
const supportsReferenceUpload = computed(() => capabilitySupportsReference(capability.value))
const hasReferences = computed(() => referenceImages.value.length > 0)
const maxImageCount = computed(() => Math.max(1, capability.value?.max_n || 1))
const activeEndpoint = computed<ImageGenerationEndpoint>(() => {
  if (hasReferences.value && supportsAdvancedOptions.value && capability.value?.supports_edits) return 'edits'
  return 'generations'
})
const endpointLabel = computed(() => activeEndpoint.value === 'edits' ? t('imageGeneration.routeEdit') : t('imageGeneration.routeGeneration'))
const selectedImage = computed(() => results.value.find((image) => image.id === selectedImageId.value) ?? results.value[0] ?? null)
const canGenerate = computed(() => {
  return Boolean(
    selectedApiKeyValue.value &&
    capability.value &&
    capability.value.available !== false &&
    prompt.value.trim() &&
    !generating.value &&
    !capabilityLoading.value,
  )
})
const advancedButtonTitle = computed(() => supportsAdvancedOptions.value ? t('imageGeneration.advancedSettings') : t('imageGeneration.advancedLockedHint'))
const advancedSummary = computed(() => {
  if (!supportsAdvancedOptions.value) return t('imageGeneration.basicHint')
  const size = advancedSettings.size === 'auto' ? t('imageGeneration.optionAuto') : advancedSettings.size
  return t('imageGeneration.advancedSummary', {
    model: advancedSettings.model || 'gpt-image-2',
    size,
    count: advancedSettings.n,
    format: advancedSettings.responseFormat,
  })
})

const capabilityStatusText = computed(() => {
  if (capabilityLoading.value) return t('imageGeneration.capabilityLoading')
  if (!selectedApiKeyValue.value) return t('imageGeneration.capabilityUnknown')
  if (!capability.value || capability.value.available === false) return t('imageGeneration.unavailable')
  if (supportsAdvancedOptions.value) return t('imageGeneration.advancedMode')
  if (capability.value.supports_basic) return t('imageGeneration.basicMode')
  return t('imageGeneration.unavailable')
})
const capabilityStatusIcon = computed<StatusIcon>(() => {
  if (capabilityLoading.value) return 'refresh'
  if (!selectedApiKeyValue.value) return 'infoCircle'
  if (!capability.value || capability.value.available === false) return 'xCircle'
  if (supportsAdvancedOptions.value || capability.value.supports_basic) return 'checkCircle'
  return 'infoCircle'
})
const capabilityStatusClass = computed(() => {
  if (capabilityLoading.value) return 'border-primary-200 bg-primary-50 text-primary-700 dark:border-primary-900/50 dark:bg-primary-900/20 dark:text-primary-300'
  if (!selectedApiKeyValue.value) return 'border-gray-200 bg-gray-50 text-gray-600 dark:border-dark-700 dark:bg-dark-900/40 dark:text-dark-300'
  if (!capability.value || capability.value.available === false) return 'border-red-200 bg-red-50 text-red-700 dark:border-red-900/50 dark:bg-red-900/20 dark:text-red-300'
  if (supportsAdvancedOptions.value) return 'border-emerald-200 bg-emerald-50 text-emerald-700 dark:border-emerald-900/50 dark:bg-emerald-900/20 dark:text-emerald-300'
  return 'border-amber-200 bg-amber-50 text-amber-700 dark:border-amber-900/50 dark:bg-amber-900/20 dark:text-amber-300'
})

function capabilitySupportsReference(value: ImageGenerationCapability | null): boolean {
  if (!value || value.available === false) return false
  return Boolean(
    value.supports_input_images ||
    value.supports_uploads ||
    value.supports_edits ||
    value.transport === 'web2api' ||
    value.image_mode === 'basic_web2api',
  )
}

async function loadApiKeys() {
  keysLoading.value = true
  try {
    const allKeys: ApiKey[] = []
    let page = 1
    let pages = 1
    do {
      const response = await keysAPI.list(page, 100)
      allKeys.push(...response.items)
      pages = response.pages || 1
      page += 1
    } while (page <= pages && page <= 20)

    apiKeys.value = allKeys.filter((key) => key.status === 'active' && key.group?.platform === 'openai')
    if (!selectedKeyId.value || !apiKeys.value.some((key) => key.id === selectedKeyId.value)) {
      selectedKeyId.value = apiKeys.value[0]?.id ?? null
    }
    await refreshCapability()
  } catch (error) {
    appStore.showError(extractApiErrorMessage(error, t('imageGeneration.keysLoadFailed')))
  } finally {
    keysLoading.value = false
  }
}

async function refreshCapability() {
  const apiKey = selectedApiKeyValue.value
  capability.value = null
  if (!apiKey) return

  const requestId = capabilityRequestId.value + 1
  capabilityRequestId.value = requestId
  capabilityLoading.value = true
  try {
    const nextCapability = await getImageCapability(apiKey)
    if (requestId !== capabilityRequestId.value) return
    capability.value = nextCapability
    resetAdvancedSettings(nextCapability)
    if (!capabilitySupportsReference(nextCapability)) clearReferenceImages()
  } catch (error) {
    if (requestId !== capabilityRequestId.value) return
    appStore.showError(extractApiErrorMessage(error, t('imageGeneration.capabilityLoadFailed')))
  } finally {
    if (requestId === capabilityRequestId.value) capabilityLoading.value = false
  }
}

function resetAdvancedSettings(nextCapability: ImageGenerationCapability | null = capability.value) {
  advancedSettings.model = nextCapability?.model || 'gpt-image-2'
  advancedSettings.size = 'auto'
  advancedSettings.n = 1
  advancedSettings.responseFormat = 'b64_json'
  advancedSettings.quality = 'auto'
  advancedSettings.outputFormat = 'auto'
  advancedSettings.background = 'auto'
  advancedSettings.moderation = 'auto'
  advancedSettings.inputFidelity = 'auto'
  advancedSettings.outputCompression = 100
}

function normalizeImageCount() {
  if (!Number.isFinite(advancedSettings.n) || advancedSettings.n < 1) {
    advancedSettings.n = 1
    return
  }
  if (advancedSettings.n > maxImageCount.value) advancedSettings.n = maxImageCount.value
}

function openAdvancedSettings() {
  if (!supportsAdvancedOptions.value) {
    appStore.showInfo(t('imageGeneration.advancedLockedHint'))
    return
  }
  advancedModalOpen.value = true
}

function handleReferenceUpload(event: Event) {
  const input = event.target as HTMLInputElement
  const files = Array.from(input.files ?? [])
  if (!files.length) return

  const availableSlots = Math.max(0, 4 - referenceImages.value.length)
  if (files.length > availableSlots) {
    appStore.showWarning(t('imageGeneration.maxReferencesReached', { count: 4 }))
  }

  for (const file of files.slice(0, availableSlots)) {
    if (!file.type.startsWith('image/')) {
      appStore.showError(t('imageGeneration.invalidImage'))
      continue
    }
    if (file.size > 20 * 1024 * 1024) {
      appStore.showError(t('imageGeneration.imageTooLarge', { size: formatBytes(20 * 1024 * 1024) }))
      continue
    }
    referenceImages.value.push({
      id: crypto.randomUUID(),
      file,
      name: file.name,
      previewUrl: URL.createObjectURL(file),
    })
  }

  if (fileInputRef.value) fileInputRef.value.value = ''
}

function removeReferenceImage(id: string) {
  const target = referenceImages.value.find((image) => image.id === id)
  if (target) URL.revokeObjectURL(target.previewUrl)
  referenceImages.value = referenceImages.value.filter((image) => image.id !== id)
}

function clearReferenceImages() {
  referenceImages.value.forEach((image) => URL.revokeObjectURL(image.previewUrl))
  referenceImages.value = []
}

function clearPromptAndReferences() {
  prompt.value = ''
  clearReferenceImages()
}

function buildAdvancedFields(endpoint: ImageGenerationEndpoint): ImageGenerationPayload {
  if (!supportsAdvancedOptions.value) return { response_format: 'b64_json' }

  normalizeImageCount()
  const fields: ImageGenerationPayload = {
    model: advancedSettings.model.trim() || capability.value?.model || 'gpt-image-2',
    response_format: advancedSettings.responseFormat,
  }

  if (advancedSettings.size !== 'auto') fields.size = advancedSettings.size
  if (advancedSettings.n > 1) fields.n = advancedSettings.n
  if (capability.value?.supports_quality !== false && advancedSettings.quality !== 'auto') fields.quality = advancedSettings.quality
  if (capability.value?.supports_output_format !== false && advancedSettings.outputFormat !== 'auto') fields.output_format = advancedSettings.outputFormat
  if (advancedSettings.background !== 'auto') fields.background = advancedSettings.background
  if (advancedSettings.moderation !== 'auto') fields.moderation = advancedSettings.moderation
  if (endpoint === 'edits' && advancedSettings.inputFidelity !== 'auto') fields.input_fidelity = advancedSettings.inputFidelity
  if (['jpeg', 'webp'].includes(advancedSettings.outputFormat)) {
    fields.output_compression = Math.min(100, Math.max(0, advancedSettings.outputCompression || 0))
  }

  return fields
}

function buildJsonPayload(): ImageGenerationPayload {
  return {
    prompt: prompt.value.trim(),
    ...buildAdvancedFields('generations'),
  }
}

async function generateImage() {
  if (!selectedApiKeyValue.value) {
    appStore.showError(t('imageGeneration.selectKeyRequired'))
    return
  }
  if (!prompt.value.trim()) {
    appStore.showError(t('imageGeneration.promptRequired'))
    return
  }
  if (!capability.value || capability.value.available === false) {
    appStore.showError(t('imageGeneration.unavailable'))
    return
  }

  generating.value = true
  try {
    const endpoint = activeEndpoint.value
    const response = hasReferences.value
      ? await createImageMultipart({
        apiKey: selectedApiKeyValue.value,
        endpoint,
        prompt: prompt.value.trim(),
        images: referenceImages.value.map((image) => image.file),
        fields: buildAdvancedFields(endpoint),
      })
      : await createImageGeneration(selectedApiKeyValue.value, buildJsonPayload())

    const newImages = materializeImages(response, endpoint)
    if (!newImages.length) throw new Error(t('imageGeneration.emptyResponse'))
    results.value = [...newImages, ...results.value]
    selectedImageId.value = newImages[0].id
    appStore.showSuccess(t('imageGeneration.generationSuccess', { count: newImages.length }))
  } catch (error) {
    appStore.showError(extractApiErrorMessage(error, t('imageGeneration.generationFailed')))
  } finally {
    generating.value = false
  }
}

function materializeImages(response: ImageGenerationResponse, endpoint: ImageGenerationEndpoint): GeneratedImage[] {
  const createdAt = response.created ? response.created * 1000 : Date.now()
  const images: GeneratedImage[] = []
  const items = response.data ?? []

  items.forEach((item, index) => {
    const source = item.b64_json
      ? createSourceFromBase64(item.b64_json)
      : item.url
        ? createSourceFromUrl(item.url)
        : null
    if (!source) return

    images.push({
      id: crypto.randomUUID(),
      src: source.src,
      downloadUrl: source.downloadUrl,
      objectUrl: source.objectUrl,
      prompt: prompt.value.trim(),
      revisedPrompt: item.revised_prompt,
      filename: `image2-${createdAt}-${index + 1}.${source.extension}`,
      endpointLabel: endpoint === 'edits' ? t('imageGeneration.routeEdit') : t('imageGeneration.routeGeneration'),
    })
  })

  return images
}

function createSourceFromBase64(raw: string): ImageSource {
  const trimmed = raw.trim()
  if (trimmed.startsWith('data:')) return createSourceFromUrl(trimmed)

  const mime = mimeFromOutputFormat(advancedSettings.outputFormat)
  const blob = base64ToBlob(trimmed, mime)
  const objectUrl = URL.createObjectURL(blob)
  return {
    src: objectUrl,
    downloadUrl: objectUrl,
    objectUrl,
    extension: extensionFromMime(mime),
  }
}

function createSourceFromUrl(url: string): ImageSource {
  const mime = url.startsWith('data:') ? mimeFromDataUrl(url) : mimeFromOutputFormat(advancedSettings.outputFormat)
  return {
    src: url,
    downloadUrl: url,
    extension: extensionFromMime(mime),
  }
}

function base64ToBlob(base64: string, mime: string): Blob {
  const clean = base64.replace(/\s/g, '')
  const binary = atob(clean)
  const chunks: Uint8Array[] = []
  for (let offset = 0; offset < binary.length; offset += 1024) {
    const slice = binary.slice(offset, offset + 1024)
    const bytes = new Uint8Array(slice.length)
    for (let i = 0; i < slice.length; i += 1) bytes[i] = slice.charCodeAt(i)
    chunks.push(bytes)
  }
  return new Blob(chunks, { type: mime })
}

function mimeFromOutputFormat(format: string): string {
  if (format === 'jpeg') return 'image/jpeg'
  if (format === 'webp') return 'image/webp'
  return 'image/png'
}

function mimeFromDataUrl(dataUrl: string): string {
  const match = /^data:([^;,]+)[;,]/.exec(dataUrl)
  return match?.[1] || 'image/png'
}

function extensionFromMime(mime: string): string {
  if (mime.includes('jpeg') || mime.includes('jpg')) return 'jpg'
  if (mime.includes('webp')) return 'webp'
  return 'png'
}

async function downloadCurrentImage() {
  const image = selectedImage.value
  if (!image) return

  if (/^https?:\/\//i.test(image.downloadUrl)) {
    try {
      const response = await fetch(image.downloadUrl)
      if (response.ok) {
        const blob = await response.blob()
        const tempUrl = URL.createObjectURL(blob)
        triggerDownload(tempUrl, image.filename)
        setTimeout(() => URL.revokeObjectURL(tempUrl), 5000)
        return
      }
    } catch {
      // Fall back to direct anchor download below.
    }
  }

  try {
    triggerDownload(image.downloadUrl, image.filename)
  } catch (error) {
    appStore.showError(extractApiErrorMessage(error, t('imageGeneration.downloadFailed')))
  }
}

function triggerDownload(url: string, filename: string) {
  const link = document.createElement('a')
  link.href = url
  link.download = filename
  link.rel = 'noopener noreferrer'
  document.body.appendChild(link)
  link.click()
  document.body.removeChild(link)
}

function clearResults() {
  results.value.forEach((image) => {
    if (image.objectUrl) URL.revokeObjectURL(image.objectUrl)
  })
  results.value = []
  selectedImageId.value = null
}

function formatBytes(bytes: number): string {
  return `${(bytes / 1024 / 1024).toFixed(0)} MB`
}

onMounted(() => {
  void loadApiKeys()
})

onBeforeUnmount(() => {
  clearResults()
  clearReferenceImages()
})
</script>
