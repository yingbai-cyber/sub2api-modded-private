import { flushPromises, mount } from '@vue/test-utils'
import { describe, expect, it } from 'vitest'

import KiroNativeCredentials, {
  buildKiroNativeCredentials,
  emptyKiroNativeCreds,
  parseKiroNativeCreds,
  KIRO_NATIVE_MANAGED_KEYS,
  type KiroNativeCreds,
} from '../KiroNativeCredentials.vue'

// 一个满足后端 ≥100 字符约束的 refresh token。
const LONG_RT = 'r'.repeat(120)

describe('buildKiroNativeCredentials (create)', () => {
  it('api_key: 缺少 kiro_api_key 报错', () => {
    const res = buildKiroNativeCredentials({ auth_method: 'api_key' }, 'create')
    expect(res.error).toBeTruthy()
    expect(res.credentials).toBeUndefined()
  })

  it('api_key: 填入 ksk_ 后产出 auth_method + kiro_api_key，不含 base_url', () => {
    const res = buildKiroNativeCredentials(
      { auth_method: 'api_key', kiro_api_key: 'ksk_abc123' },
      'create'
    )
    expect(res.error).toBeUndefined()
    expect(res.credentials).toMatchObject({ auth_method: 'api_key', kiro_api_key: 'ksk_abc123' })
    expect(res.credentials).not.toHaveProperty('base_url')
    expect(res.credentials).not.toHaveProperty('api_key')
  })

  it('social: 缺少 refresh_token 报错', () => {
    const res = buildKiroNativeCredentials({ auth_method: 'social' }, 'create')
    expect(res.error).toBeTruthy()
  })

  it('social: refresh_token 过短报错', () => {
    const res = buildKiroNativeCredentials(
      { auth_method: 'social', refresh_token: 'short' },
      'create'
    )
    expect(res.error).toContain('100')
  })

  it('social: 合法 refresh_token + 可选字段', () => {
    const res = buildKiroNativeCredentials(
      { auth_method: 'social', refresh_token: LONG_RT, region: 'us-east-1', profile_arn: 'arn:x' },
      'create'
    )
    expect(res.error).toBeUndefined()
    expect(res.credentials).toMatchObject({
      auth_method: 'social',
      refresh_token: LONG_RT,
      region: 'us-east-1',
      profile_arn: 'arn:x',
    })
  })

  it('idc: 需 refresh_token + client_id + client_secret', () => {
    expect(
      buildKiroNativeCredentials({ auth_method: 'idc', refresh_token: LONG_RT }, 'create').error
    ).toBeTruthy()
    const ok = buildKiroNativeCredentials(
      {
        auth_method: 'idc',
        refresh_token: LONG_RT,
        client_id: 'cid',
        client_secret: 'csec',
      },
      'create'
    )
    expect(ok.error).toBeUndefined()
    expect(ok.credentials).toMatchObject({
      auth_method: 'idc',
      client_id: 'cid',
      client_secret: 'csec',
    })
  })

  it('external_idp: 需 refresh_token + token_endpoint', () => {
    expect(
      buildKiroNativeCredentials(
        { auth_method: 'external_idp', refresh_token: LONG_RT },
        'create'
      ).error
    ).toBeTruthy()
    const ok = buildKiroNativeCredentials(
      {
        auth_method: 'external_idp',
        refresh_token: LONG_RT,
        token_endpoint: 'https://idp/token',
        scopes: 'openid',
      },
      'create'
    )
    expect(ok.error).toBeUndefined()
    expect(ok.credentials).toMatchObject({
      auth_method: 'external_idp',
      token_endpoint: 'https://idp/token',
      scopes: 'openid',
    })
  })
})

describe('buildKiroNativeCredentials (edit)', () => {
  it('edit 下秘密留空不报错，仅回传非空字段（秘密由后端 Merge 保留）', () => {
    const res = buildKiroNativeCredentials(
      { auth_method: 'api_key', kiro_api_key: '' },
      'edit'
    )
    expect(res.error).toBeUndefined()
    expect(res.credentials).toEqual({ auth_method: 'api_key' })
    expect(res.credentials).not.toHaveProperty('kiro_api_key')
  })

  it('edit 下填入新秘密则回传（旋转）', () => {
    const res = buildKiroNativeCredentials(
      { auth_method: 'api_key', kiro_api_key: 'ksk_new' },
      'edit'
    )
    expect(res.credentials).toMatchObject({ auth_method: 'api_key', kiro_api_key: 'ksk_new' })
  })
})

describe('parseKiroNativeCreds', () => {
  it('legacy（仅 base_url）判定为非 native', () => {
    const { isNative } = parseKiroNativeCreds({ base_url: 'http://127.0.0.1:8080' }, {})
    expect(isNative).toBe(false)
  })

  it('has_kiro_api_key 判定为 native + api_key', () => {
    const { isNative, creds } = parseKiroNativeCreds({}, { has_kiro_api_key: true })
    expect(isNative).toBe(true)
    expect(creds.auth_method).toBe('api_key')
    expect(creds.kiro_api_key).toBe('') // 秘密脱敏，留空
  })

  it('显式 auth_method=idc 回填非敏感字段', () => {
    const { isNative, creds } = parseKiroNativeCreds(
      { auth_method: 'idc', client_id: 'cid', region: 'us-east-1' },
      { has_refresh_token: true, has_client_secret: true }
    )
    expect(isNative).toBe(true)
    expect(creds.auth_method).toBe('idc')
    expect(creds.client_id).toBe('cid')
    expect(creds.region).toBe('us-east-1')
    expect(creds.client_secret).toBe('') // 脱敏
    expect(creds.refresh_token).toBe('') // 脱敏
  })

  it('无 auth_method 但有 token_endpoint 推断 external_idp', () => {
    const { creds } = parseKiroNativeCreds(
      { token_endpoint: 'https://idp/token' },
      { has_refresh_token: true }
    )
    expect(creds.auth_method).toBe('external_idp')
  })

  it('别名 auth_method 规范化（github → social）', () => {
    const { creds } = parseKiroNativeCreds({ auth_method: 'github' }, { has_refresh_token: true })
    expect(creds.auth_method).toBe('social')
  })
})

describe('KIRO_NATIVE_MANAGED_KEYS', () => {
  it('包含 legacy 代理键，切 native 时会被剥离', () => {
    expect(KIRO_NATIVE_MANAGED_KEYS).toContain('base_url')
    expect(KIRO_NATIVE_MANAGED_KEYS).toContain('api_key')
    expect(KIRO_NATIVE_MANAGED_KEYS).toContain('kiro_api_key')
    expect(KIRO_NATIVE_MANAGED_KEYS).toContain('auth_method')
  })
})

describe('KiroNativeCredentials component', () => {
  function mountComp(modelValue: KiroNativeCreds, extra: Record<string, unknown> = {}) {
    return mount(KiroNativeCredentials, {
      props: { modelValue, mode: 'create', ...extra },
    })
  }

  it('api_key 模式渲染 kiro_api_key 输入，不渲染 refresh_token', () => {
    const w = mountComp({ auth_method: 'api_key' })
    expect(w.text()).toContain('Kiro API Key')
    expect(w.text()).not.toContain('Refresh Token')
  })

  it('social 模式渲染 Refresh Token', () => {
    const w = mountComp({ auth_method: 'social' })
    expect(w.text()).toContain('Refresh Token')
  })

  it('idc 模式渲染 Client ID / Client Secret', () => {
    const w = mountComp({ auth_method: 'idc' })
    expect(w.text()).toContain('Client ID')
    expect(w.text()).toContain('Client Secret')
  })

  it('external_idp 模式渲染 Token Endpoint', () => {
    const w = mountComp({ auth_method: 'external_idp' })
    expect(w.text()).toContain('Token Endpoint')
  })

  it('切换 auth_method 下拉会 emit update:modelValue', async () => {
    const w = mountComp({ auth_method: 'social' })
    const select = w.get('select')
    await select.setValue('api_key')
    await flushPromises()
    const events = w.emitted('update:modelValue')
    expect(events).toBeTruthy()
    const last = events?.[events.length - 1]?.[0] as KiroNativeCreds
    expect(last.auth_method).toBe('api_key')
  })

  it('edit 模式且 has_kiro_api_key 时占位显示"已配置"', () => {
    const w = mountComp({ auth_method: 'api_key' }, {
      mode: 'edit',
      credentialsStatus: { has_kiro_api_key: true },
    })
    const input = w.get('input[type="password"]')
    expect(input.attributes('placeholder')).toContain('已配置')
  })

  it('emptyKiroNativeCreds 默认 social', () => {
    expect(emptyKiroNativeCreds().auth_method).toBe('social')
  })
})
