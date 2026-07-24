<template>
  <div class="space-y-4">
    <!-- 认证方式 auth_method -->
    <div>
      <label class="input-label">认证方式 (auth_method)</label>
      <select :value="modelValue.auth_method" class="input" @change="onAuthMethodChange">
        <option value="social">Social（社交登录 GitHub/Google，用 refresh_token 刷新）</option>
        <option value="api_key">API Key（ksk_ 直连密钥，无需刷新）</option>
        <option value="idc">IdC（AWS SSO / Builder ID）</option>
        <option value="external_idp">External IdP（Entra/Azure 等外部 IdP）</option>
      </select>
      <p class="input-hint">{{ authMethodHint }}</p>
    </div>

    <!-- API Key 方式 -->
    <div v-if="modelValue.auth_method === 'api_key'">
      <label class="input-label">Kiro API Key (ksk_*)</label>
      <input
        :value="modelValue.kiro_api_key || ''"
        type="password"
        class="input"
        autocomplete="off"
        :placeholder="secretPlaceholder('has_kiro_api_key', 'ksk_...')"
        @input="onField('kiro_api_key', $event)"
      />
      <p class="input-hint">ksk_ 开头的密钥，直接作为 Bearer 使用。{{ keepHint('has_kiro_api_key') }}</p>
    </div>
    <!-- refresh_token（social / idc / external_idp 共用） -->
    <div v-if="usesRefreshToken">
      <label class="input-label">Refresh Token</label>
      <textarea
        :value="modelValue.refresh_token || ''"
        rows="2"
        class="input font-mono text-xs"
        autocomplete="off"
        :placeholder="secretPlaceholder('has_refresh_token', 'refresh token（≥100 字符）')"
        @input="onField('refresh_token', $event)"
      />
      <p class="input-hint">用于向上游刷新 access_token（后端要求 ≥100 字符）。{{ keepHint('has_refresh_token') }}</p>
    </div>

    <!-- IdC：client_id + client_secret -->
    <template v-if="modelValue.auth_method === 'idc'">
      <div>
        <label class="input-label">Client ID</label>
        <input
          :value="modelValue.client_id || ''"
          type="text"
          class="input"
          autocomplete="off"
          placeholder="AWS SSO OIDC client_id"
          @input="onField('client_id', $event)"
        />
      </div>
      <div>
        <label class="input-label">Client Secret</label>
        <input
          :value="modelValue.client_secret || ''"
          type="password"
          class="input"
          autocomplete="off"
          :placeholder="secretPlaceholder('has_client_secret', 'AWS SSO OIDC client_secret')"
          @input="onField('client_secret', $event)"
        />
        <p class="input-hint">{{ keepHint('has_client_secret') }}</p>
      </div>
    </template>

    <!-- External IdP：token_endpoint + client_id + issuer_url + scopes -->
    <template v-if="modelValue.auth_method === 'external_idp'">
      <div>
        <label class="input-label">Token Endpoint</label>
        <input
          :value="modelValue.token_endpoint || ''"
          type="text"
          class="input"
          autocomplete="off"
          placeholder="https://login.microsoftonline.com/.../oauth2/v2.0/token"
          @input="onField('token_endpoint', $event)"
        />
        <p class="input-hint">外部 IdP 的 token 刷新端点</p>
      </div>
      <div>
        <label class="input-label">Client ID（可选）</label>
        <input
          :value="modelValue.client_id || ''"
          type="text"
          class="input"
          autocomplete="off"
          @input="onField('client_id', $event)"
        />
      </div>
      <div>
        <label class="input-label">Issuer URL（可选）</label>
        <input
          :value="modelValue.issuer_url || ''"
          type="text"
          class="input"
          autocomplete="off"
          @input="onField('issuer_url', $event)"
        />
      </div>
      <div>
        <label class="input-label">Scopes（可选）</label>
        <input
          :value="modelValue.scopes || ''"
          type="text"
          class="input"
          autocomplete="off"
          placeholder="openid profile offline_access"
          @input="onField('scopes', $event)"
        />
      </div>
    </template>

    <!-- social 可选：access_token / expires_at / profile_arn -->
    <template v-if="modelValue.auth_method === 'social'">
      <div>
        <label class="input-label">Access Token（可选）</label>
        <input
          :value="modelValue.access_token || ''"
          type="password"
          class="input"
          autocomplete="off"
          :placeholder="secretPlaceholder('has_access_token', '当前 access_token，可留空由刷新获取')"
          @input="onField('access_token', $event)"
        />
        <p class="input-hint">{{ keepHint('has_access_token') }}</p>
      </div>
      <div>
        <label class="input-label">Profile ARN（可选）</label>
        <input
          :value="modelValue.profile_arn || ''"
          type="text"
          class="input"
          autocomplete="off"
          placeholder="arn:aws:codewhisperer:..."
          @input="onField('profile_arn', $event)"
        />
      </div>
    </template>

    <!-- 通用可选：region + endpoint -->
    <div class="grid grid-cols-2 gap-3">
      <div>
        <label class="input-label">Region（可选）</label>
        <input
          :value="modelValue.region || ''"
          type="text"
          class="input"
          autocomplete="off"
          placeholder="us-east-1"
          @input="onField('region', $event)"
        />
      </div>
      <div>
        <label class="input-label">Endpoint（可选）</label>
        <select :value="modelValue.endpoint || ''" class="input" @change="onField('endpoint', $event)">
          <option value="">默认</option>
          <option value="ide">IDE</option>
          <option value="cli">CLI</option>
        </select>
      </div>
    </div>
  </div>
</template>

<script lang="ts">
export type KiroAuthMethod = 'social' | 'api_key' | 'idc' | 'external_idp'

// KiroNativeCreds 使用 snake_case 键，直接对齐后端 kiro.ParseCredentials 契约。
export interface KiroNativeCreds {
  auth_method: KiroAuthMethod
  kiro_api_key?: string
  refresh_token?: string
  access_token?: string
  expires_at?: string
  profile_arn?: string
  client_id?: string
  client_secret?: string
  token_endpoint?: string
  issuer_url?: string
  scopes?: string
  region?: string
  endpoint?: string
}

// 空白 native 凭证工厂：默认 social（最常见的社交登录刷新方式）。
export function emptyKiroNativeCreds(): KiroNativeCreds {
  return { auth_method: 'social' }
}

// 敏感键：Edit 留空表示"保留原值"，不回传给后端（由 MergePreservingSensitiveCreds 保留）。
const KIRO_SECRET_KEYS: (keyof KiroNativeCreds)[] = [
  'kiro_api_key',
  'refresh_token',
  'access_token',
  'client_secret'
]

// native 表单托管的全部凭证键（含 legacy 代理键）。Edit 切到 native 时先从既有 credentials
// 剥离这些键，使表单成为权威来源，再并入 buildKiroNativeCredentials 的结果。
export const KIRO_NATIVE_MANAGED_KEYS: string[] = [
  'auth_method',
  'kiro_api_key',
  'refresh_token',
  'access_token',
  'expires_at',
  'profile_arn',
  'client_id',
  'client_secret',
  'token_endpoint',
  'issuer_url',
  'scopes',
  'region',
  'endpoint',
  // legacy 代理字段：切到 native 后不应残留
  'base_url',
  'api_key'
]

// 非敏感键：始终按当前值回传（后端可见，用于 Edit 预填/编辑）。
const KIRO_PLAIN_KEYS: (keyof KiroNativeCreds)[] = [
  'expires_at',
  'profile_arn',
  'client_id',
  'token_endpoint',
  'issuer_url',
  'scopes',
  'region',
  'endpoint'
]

function trimmed(v: string | undefined): string {
  return (v ?? '').trim()
}

function canonicalizeAuthMethod(v: string): KiroAuthMethod {
  const m = v.toLowerCase().trim()
  switch (m) {
    case 'api_key':
    case 'apikey':
      return 'api_key'
    case 'idc':
    case 'builder-id':
    case 'builderid':
    case 'iam':
    case 'iam_sso':
      return 'idc'
    case 'external_idp':
    case 'azuread':
    case 'azure':
    case 'entra':
    case 'microsoft':
    case 'm365':
      return 'external_idp'
    case 'social':
    case 'github':
    case 'google':
      return 'social'
    default:
      return 'social'
  }
}

// 从账号（脱敏后的）credentials + credentials_status 解析出 native 表单值，并判定是否 native 模式。
// - isNative：显式 auth_method 为已知 native 方法，或后端标记存在 kiro_api_key / refresh_token。
// - creds：仅回填非敏感字段（秘密字段已脱敏，留空由 credentials_status 提示"已配置"）。
export function parseKiroNativeCreds(
  credentials: Record<string, unknown> | null | undefined,
  credentialsStatus?: Record<string, boolean> | null
): { creds: KiroNativeCreds; isNative: boolean } {
  const c = credentials || {}
  const status = credentialsStatus || {}
  const str = (v: unknown): string => (typeof v === 'string' ? v : '')

  const rawAuth = str(c.auth_method).trim()
  const hasApiKey = status.has_kiro_api_key === true
  const hasRefresh = status.has_refresh_token === true
  const tokenEndpoint = str(c.token_endpoint)
  const clientId = str(c.client_id)

  // 解析 auth_method：优先显式值；否则按可见字段/状态推断（对齐后端 EffectiveAuthMethod）。
  let authMethod: KiroAuthMethod
  if (rawAuth) {
    authMethod = canonicalizeAuthMethod(rawAuth)
  } else if (hasApiKey) {
    authMethod = 'api_key'
  } else if (tokenEndpoint) {
    authMethod = 'external_idp'
  } else if (clientId) {
    authMethod = 'idc'
  } else {
    authMethod = 'social'
  }

  const isNative =
    hasApiKey ||
    hasRefresh ||
    rawAuth === 'api_key' ||
    rawAuth === 'social' ||
    rawAuth === 'idc' ||
    rawAuth === 'external_idp'

  const creds: KiroNativeCreds = {
    auth_method: authMethod,
    // 秘密字段留空（脱敏），Edit 下由 credentials_status 提示"已配置（留空保留）"。
    kiro_api_key: '',
    refresh_token: '',
    access_token: '',
    client_secret: '',
    // 非敏感字段回填。
    expires_at: str(c.expires_at),
    profile_arn: str(c.profile_arn),
    client_id: clientId,
    token_endpoint: tokenEndpoint,
    issuer_url: str(c.issuer_url),
    scopes: str(c.scopes),
    region: str(c.region),
    endpoint: str(c.endpoint)
  }

  return { creds, isNative }
}

// 从 native 凭证表单构建提交用 credentials（snake_case，对齐后端契约）。
// - create：秘密字段必填（缺失即报错）。
// - edit：秘密字段留空表示保留原值（不回传）。
// 返回 { credentials } 或 { error }（中文错误提示，由调用方 showError）。
export function buildKiroNativeCredentials(
  creds: KiroNativeCreds,
  mode: 'create' | 'edit'
): { credentials?: Record<string, unknown>; error?: string } {
  const method = creds.auth_method
  const out: Record<string, unknown> = { auth_method: method }

  const kiroApiKey = trimmed(creds.kiro_api_key)
  const refreshToken = trimmed(creds.refresh_token)
  const clientId = trimmed(creds.client_id)
  const clientSecret = trimmed(creds.client_secret)
  const tokenEndpoint = trimmed(creds.token_endpoint)

  // 按 auth_method 校验必填项（create 强制；edit 允许留空以保留原值）。
  if (mode === 'create') {
    if (method === 'api_key' && !kiroApiKey) {
      return { error: '请填写 Kiro API Key (ksk_*)' }
    }
    if ((method === 'social' || method === 'idc' || method === 'external_idp') && !refreshToken) {
      return { error: '请填写 Refresh Token' }
    }
    if (method === 'idc' && (!clientId || !clientSecret)) {
      return { error: 'IdC 方式需填写 Client ID 与 Client Secret' }
    }
    if (method === 'external_idp' && !tokenEndpoint) {
      return { error: 'External IdP 方式需填写 Token Endpoint' }
    }
  }

  // refresh_token 长度约束（后端 ValidateRefreshToken 要求 ≥100 字符），仅在有值时提示。
  if (refreshToken && refreshToken.length < 100) {
    return { error: 'Refresh Token 过短（后端要求 ≥100 字符）' }
  }

  // 秘密字段：有值才回传。
  for (const key of KIRO_SECRET_KEYS) {
    const val = trimmed(creds[key] as string | undefined)
    if (val) out[key] = val
  }
  // 非敏感字段：有值才回传（空值省略，避免写入空串）。
  for (const key of KIRO_PLAIN_KEYS) {
    const val = trimmed(creds[key] as string | undefined)
    if (val) out[key] = val
  }

  return { credentials: out }
}
</script>

<script setup lang="ts">
import { computed } from 'vue'

const props = defineProps<{
  modelValue: KiroNativeCreds
  /** 'create' 时秘密字段为必填；'edit' 时留空表示保留既有值 */
  mode: 'create' | 'edit'
  /** Edit 场景后端返回的 credentials_status（has_* 布尔），用于"留空保留"提示 */
  credentialsStatus?: Record<string, boolean> | null
}>()

const emit = defineEmits<{
  (e: 'update:modelValue', value: KiroNativeCreds): void
}>()

const usesRefreshToken = computed(
  () =>
    props.modelValue.auth_method === 'social' ||
    props.modelValue.auth_method === 'idc' ||
    props.modelValue.auth_method === 'external_idp'
)

const authMethodHint = computed(() => {
  switch (props.modelValue.auth_method) {
    case 'api_key':
      return 'ksk_ 开头的直连密钥，直接作为 Bearer，不做 token 刷新。'
    case 'idc':
      return 'AWS SSO OIDC 刷新（oidc.{region}.amazonaws.com/token），需 client_id + client_secret。'
    case 'external_idp':
      return '外部 IdP（Entra/Azure 等）刷新，需 token_endpoint。'
    case 'social':
    default:
      return 'GitHub/Google 等社交登录，用 refresh_token 向 prod.{region}.auth.desktop.kiro.dev 刷新。'
  }
})

function emitPatch(patch: Partial<KiroNativeCreds>) {
  emit('update:modelValue', { ...props.modelValue, ...patch })
}

function onAuthMethodChange(e: Event) {
  const value = (e.target as HTMLSelectElement).value as KiroAuthMethod
  emitPatch({ auth_method: value })
}

function onField(key: keyof KiroNativeCreds, e: Event) {
  const target = e.target as HTMLInputElement | HTMLTextAreaElement | HTMLSelectElement
  emitPatch({ [key]: target.value } as Partial<KiroNativeCreds>)
}

// Edit 下若后端标记该秘密已存在，则输入框显示"已配置（留空保留）"占位。
function isConfigured(statusKey: string): boolean {
  return props.mode === 'edit' && !!props.credentialsStatus?.[statusKey]
}

function secretPlaceholder(statusKey: string, fallback: string): string {
  return isConfigured(statusKey) ? '已配置（留空保留原值）' : fallback
}

function keepHint(statusKey: string): string {
  return isConfigured(statusKey) ? '留空则保留原有值。' : ''
}
</script>


