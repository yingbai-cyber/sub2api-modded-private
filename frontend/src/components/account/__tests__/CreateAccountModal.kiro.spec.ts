import { defineComponent } from 'vue'
import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'

const { createAccountMock } = vi.hoisted(() => ({
  createAccountMock: vi.fn(),
}))

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({
    showError: vi.fn(),
    showSuccess: vi.fn(),
    showWarning: vi.fn(),
  }),
}))

vi.mock('@/stores/auth', () => ({
  useAuthStore: () => ({ isSimpleMode: true }),
}))

vi.mock('@/api/admin', () => ({
  adminAPI: {
    accounts: {
      create: createAccountMock,
      probeUpstreamBilling: vi.fn().mockResolvedValue({}),
      checkMixedChannelRisk: vi.fn().mockResolvedValue({ has_risk: false }),
      importCodexSession: vi.fn().mockResolvedValue({}),
      createOpenAICodexPAT: vi.fn().mockResolvedValue({}),
    },
    settings: {
      getWebSearchEmulationConfig: vi.fn().mockResolvedValue({ enabled: false, providers: [] }),
      getSettings: vi.fn().mockResolvedValue({}),
    },
    tlsFingerprintProfiles: {
      list: vi.fn().mockResolvedValue([]),
    },
  },
}))

vi.mock('@/api/admin/accounts', () => ({
  getAntigravityDefaultModelMapping: vi.fn().mockResolvedValue([]),
}))

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({ t: (key: string) => key }),
  }
})

import CreateAccountModal from '../CreateAccountModal.vue'
import KiroNativeCredentials from '../KiroNativeCredentials.vue'

const LONG_RT = 'r'.repeat(120)

const BaseDialogStub = defineComponent({
  name: 'BaseDialog',
  props: { show: { type: Boolean, default: false } },
  template: '<div v-if="show"><slot /><slot name="footer" /></div>',
})

function mountModal() {
  return mount(CreateAccountModal, {
    props: { show: true, proxies: [], groups: [] },
    global: {
      stubs: {
        BaseDialog: BaseDialogStub,
        OAuthAuthorizationFlow: true,
        ConfirmDialog: true,
        Select: true,
        Icon: true,
        PlatformIcon: true,
        ProxySelector: true,
        ProxyAdBanner: true,
        GroupSelector: true,
        ModelWhitelistSelector: true,
        QuotaLimitCard: true,
        // KiroNativeCredentials 保持真实渲染
      },
    },
  })
}

async function clickButtonByText(wrapper: ReturnType<typeof mountModal>, text: string) {
  const button = wrapper.findAll('button').find((c) => c.text().includes(text))
  expect(button, `button "${text}" not found`).toBeDefined()
  await button?.trigger('click')
}

async function openKiroNative() {
  const wrapper = mountModal()
  // platform 默认 anthropic → Kiro 分类可见
  await clickButtonByText(wrapper, 'Kiro')
  // 切到原生直连
  await clickButtonByText(wrapper, '原生直连')
  return wrapper
}

describe('CreateAccountModal Kiro native', () => {
  beforeEach(() => {
    createAccountMock.mockReset().mockResolvedValue({ id: 7, platform: 'anthropic', type: 'kiro' })
  })

  it('默认 legacy 模式：提交 base_url 代理凭证', async () => {
    const wrapper = mountModal()
    await clickButtonByText(wrapper, 'Kiro')
    await wrapper.get('form#create-account-form input[type="text"]').setValue('kiro-legacy')
    await wrapper.get('form#create-account-form').trigger('submit.prevent')
    await flushPromises()

    expect(createAccountMock).toHaveBeenCalledTimes(1)
    const creds = createAccountMock.mock.calls[0]?.[0]?.credentials
    expect(creds?.base_url).toBeTruthy()
    expect(creds?.auth_method).toBeUndefined()
  })

  it('native social：提交 auth_method + refresh_token，不含 base_url', async () => {
    const wrapper = await openKiroNative()
    await wrapper.get('form#create-account-form input[type="text"]').setValue('kiro-native')
    // native 组件唯一的 textarea = refresh_token
    await wrapper.findComponent(KiroNativeCredentials).get('textarea').setValue(LONG_RT)
    await wrapper.get('form#create-account-form').trigger('submit.prevent')
    await flushPromises()

    expect(createAccountMock).toHaveBeenCalledTimes(1)
    const payload = createAccountMock.mock.calls[0]?.[0]
    expect(payload?.type).toBe('kiro')
    expect(payload?.credentials?.auth_method).toBe('social')
    expect(payload?.credentials?.refresh_token).toBe(LONG_RT)
    expect(payload?.credentials).not.toHaveProperty('base_url')
  })

  it('native api_key：提交 kiro_api_key', async () => {
    const wrapper = await openKiroNative()
    await wrapper.get('form#create-account-form input[type="text"]').setValue('kiro-native-key')
    // 切 auth_method 到 api_key
    await wrapper.findComponent(KiroNativeCredentials).get('select').setValue('api_key')
    await flushPromises()
    await wrapper
      .findComponent(KiroNativeCredentials)
      .get('input[type="password"]')
      .setValue('ksk_test123')
    await wrapper.get('form#create-account-form').trigger('submit.prevent')
    await flushPromises()

    expect(createAccountMock).toHaveBeenCalledTimes(1)
    const creds = createAccountMock.mock.calls[0]?.[0]?.credentials
    expect(creds?.auth_method).toBe('api_key')
    expect(creds?.kiro_api_key).toBe('ksk_test123')
    expect(creds).not.toHaveProperty('base_url')
  })

  it('native social 缺 refresh_token：不提交（校验拦截）', async () => {
    const wrapper = await openKiroNative()
    await wrapper.get('form#create-account-form input[type="text"]').setValue('kiro-native-bad')
    await wrapper.get('form#create-account-form').trigger('submit.prevent')
    await flushPromises()

    expect(createAccountMock).not.toHaveBeenCalled()
  })
})
