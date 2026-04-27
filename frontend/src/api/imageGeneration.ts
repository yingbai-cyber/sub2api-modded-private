/**
 * User-side Image2 gateway helpers.
 *
 * Important: these calls intentionally use fetch against /v1/* gateway paths
 * instead of apiClient, because the selected user API key is the bearer token.
 */

export type ImageGenerationEndpoint = 'generations' | 'edits'

export interface ImageGenerationCapability {
  available: boolean
  ui_mode: string
  image_mode: string
  transport: string
  model: string
  supports_basic: boolean
  supports_advanced_options: boolean
  supports_stream: boolean
  supports_exact_size: boolean
  supports_custom_size: boolean
  supports_quality: boolean
  supports_output_format: boolean
  supports_partial_images: boolean
  supports_edits: boolean
  supports_input_images?: boolean
  supports_uploads?: boolean
  max_n: number
  unsupported_params?: string[]
  account_counts?: Record<string, number>
  warnings?: string[]
}

export interface ImageGenerationResultItem {
  b64_json?: string
  url?: string
  revised_prompt?: string
}

export interface ImageGenerationResponse {
  created?: number
  data?: ImageGenerationResultItem[]
}

export type ImageGenerationPayload = Record<string, string | number | boolean | null | undefined>

interface MultipartImageRequest {
  apiKey: string
  endpoint: ImageGenerationEndpoint
  prompt: string
  images: File[]
  fields?: ImageGenerationPayload
  signal?: AbortSignal
}

function buildAuthHeaders(apiKey: string, extra?: HeadersInit): Headers {
  const headers = new Headers(extra)
  headers.set('Authorization', `Bearer ${apiKey}`)
  headers.set('Accept', 'application/json')
  return headers
}

function getErrorMessage(payload: unknown, fallback: string): string {
  if (!payload || typeof payload !== 'object') return fallback

  const record = payload as Record<string, unknown>
  const error = record.error
  if (typeof error === 'string' && error.trim()) return error
  if (error && typeof error === 'object') {
    const errorRecord = error as Record<string, unknown>
    const message = errorRecord.message
    if (typeof message === 'string' && message.trim()) return message
  }

  for (const key of ['message', 'detail', 'error_description']) {
    const value = record[key]
    if (typeof value === 'string' && value.trim()) return value
  }

  return fallback
}

async function parseResponse(response: Response): Promise<unknown> {
  const text = await response.text()
  if (!text) return null
  try {
    return JSON.parse(text) as unknown
  } catch {
    return text
  }
}

async function gatewayFetch<T>(path: string, apiKey: string, init: RequestInit = {}): Promise<T> {
  const response = await fetch(path, {
    ...init,
    headers: buildAuthHeaders(apiKey, init.headers),
    credentials: 'same-origin',
  })
  const payload = await parseResponse(response)

  if (!response.ok) {
    throw new Error(getErrorMessage(payload, `Gateway request failed (${response.status})`))
  }

  return payload as T
}

export function getImageCapability(apiKey: string, signal?: AbortSignal): Promise<ImageGenerationCapability> {
  return gatewayFetch<ImageGenerationCapability>('/v1/images/capability', apiKey, {
    method: 'GET',
    signal,
  })
}

export function createImageGeneration(
  apiKey: string,
  payload: ImageGenerationPayload,
  signal?: AbortSignal,
): Promise<ImageGenerationResponse> {
  return gatewayFetch<ImageGenerationResponse>('/v1/images/generations', apiKey, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(payload),
    signal,
  })
}

export function createImageMultipart(request: MultipartImageRequest): Promise<ImageGenerationResponse> {
  const form = new FormData()
  form.append('prompt', request.prompt)

  for (const [key, value] of Object.entries(request.fields ?? {})) {
    if (value === undefined || value === null || value === '') continue
    form.append(key, String(value))
  }

  request.images.forEach((file, index) => {
    const fieldName = request.images.length === 1 ? 'image' : `image[${index}]`
    form.append(fieldName, file, file.name)
  })

  return gatewayFetch<ImageGenerationResponse>(`/v1/images/${request.endpoint}`, request.apiKey, {
    method: 'POST',
    body: form,
    signal: request.signal,
  })
}

export const imageGenerationAPI = {
  getCapability: getImageCapability,
  generate: createImageGeneration,
  multipart: createImageMultipart,
}

export default imageGenerationAPI
