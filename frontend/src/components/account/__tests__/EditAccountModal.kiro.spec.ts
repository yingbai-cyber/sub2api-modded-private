import { beforeEach, describe, expect, it, vi } from 'vitest'
import { defineComponent } from 'vue'
import { flushPromises, mount } from '@vue/test-utils'

const { updateAccountMock, checkMixedChannelRiskMock } = vi.hoisted(() => ({
  updateAccountMock: vi.fn(),
  checkMixedChannelRiskMock: vi.fn(),
}))

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({
    showError: vi.fn(),
    showSuccess: vi.fn(),
    showInfo: vi.fn(),
  }),
}))

vi.mock('@/stores/auth', () => ({
  useAuthStore: () => ({ isSimpleMode: true }),
}))

vi.mock('@/api/admin', () => ({
  adminAPI: {
    accounts: {
      update: updateAccountMock,
      checkMixedChannelRisk: checkMixedChannelRiskMock,
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
  getAntigravityDefaultModelMapping: vi.fn(),
}))

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({ t: (key: string) => key }),
  }
})

import EditAccountModal from '../EditAccountModal.vue'
import KiroNativeCredentials from '../KiroNativeCredentials.vue'

const LONG_RT = 'r'.repeat(120)

const BaseDialogStub = defineComponent({
  name: 'BaseDialog',
  props: { show: { type: Boolean, default: false } },
  template: '<div v-if="show"><slot /><slot name="footer" /></div>',
})

function buildKiroBase() {
  return {
    id: 20,
    name: 'Kiro Account',
    notes: '',
    platform: 'anthropic',
    type: 'kiro',
    credentials: {},
    credentials_status: {},
    extra: { credits_per_dollar: 50 },
    proxy_id: null,
    concurrency: 1,
    priority: 1,
    rate_multiplier: 1,
    status: 'active',
    group_ids: [],
    expires_at: null,
    auto_pause_on_expired: false,
  } as any
}

function buildKiroLegacyAccount() {
  return {
    ...buildKiroBase(),
    credentials: { base_url: 'http://127.0.0.1:8080' },
    credentials_status: {},
  } as any
}

function buildKiroNativeApiKeyAccount() {
  return {
    ...buildKiroBase(),
    credentials: { auth_method: 'api_key' },
    credentials_status: { has_kiro_api_key: true },
  } as any
}

function buildKiroNativeIdcAccount() {
  return {
    ...buildKiroBase(),
    credentials: { auth_method: 'idc', client_id: 'cid-existing', region: 'us-east-1' },
    credentials_status: { has_refresh_token: true, has_client_secret: true },
  } as any
}

function mountModal(account: any) {
  return mount(EditAccountModal, {
    props: { show: true, account, proxies: [], groups: [] },
    global: {
      stubs: {
        BaseDialog: BaseDialogStub,
        Select: true,
        Icon: true,
        ProxySelector: true,
        GroupSelector: true,
        ModelWhitelistSelector: true,
        QuotaLimitCard: true,
        // KiroNativeCredentials 保持真实渲染
      },
    },
  })
}

async function submit(wrapper: ReturnType<typeof mountModal>) {
  await wrapper.get('form#edit-account-form').trigger('submit.prevent')
  await flushPromises()
}

describe('EditAccountModal Kiro native', () => {
  beforeEach(() => {
    updateAccountMock.mockReset().mockResolvedValue({})
    checkMixedChannelRiskMock.mockReset().mockResolvedValue({ has_risk: false })
  })

  it('legacy 账号推断为 legacy 模式，不渲染 native 组件', () => {
    const wrapper = mountModal(buildKiroLegacyAccount())
    expect(wrapper.findComponent(KiroNativeCredentials).exists()).toBe(false)
  })

  it('has_kiro_api_key 账号推断为 native，渲染 native 组件且占位显示已配置', () => {
    const wrapper = mountModal(buildKiroNativeApiKeyAccount())
    const comp = wrapper.findComponent(KiroNativeCredentials)
    expect(comp.exists()).toBe(true)
    const pwd = comp.get('input[type="password"]')
    expect(pwd.attributes('placeholder')).toContain('已配置')
  })

  it('native 编辑：秘密留空不回传（由后端 Merge 保留），仅回传 auth_method 等非空', async () => {
    const wrapper = mountModal(buildKiroNativeApiKeyAccount())
    await submit(wrapper)

    expect(updateAccountMock).toHaveBeenCalledTimes(1)
    const payload = updateAccountMock.mock.calls[0]?.[1]
    expect(payload?.credentials?.auth_method).toBe('api_key')
    expect(payload?.credentials).not.toHaveProperty('kiro_api_key')
    expect(payload?.credentials).not.toHaveProperty('base_url')
  })

  it('native idc：回填 client_id，切 native 时剥离 legacy base_url', async () => {
    const wrapper = mountModal(buildKiroNativeIdcAccount())
    const comp = wrapper.findComponent(KiroNativeCredentials)
    expect(comp.exists()).toBe(true)
    // client_id 已回填
    const textInputs = comp.findAll('input[type="text"]')
    const clientIdInput = textInputs.find(
      (i) => (i.element as HTMLInputElement).value === 'cid-existing'
    )
    expect(clientIdInput, 'client_id 应回填').toBeDefined()

    await submit(wrapper)
    const payload = updateAccountMock.mock.calls[0]?.[1]
    expect(payload?.credentials?.auth_method).toBe('idc')
    expect(payload?.credentials?.client_id).toBe('cid-existing')
    expect(payload?.credentials).not.toHaveProperty('base_url')
    // 秘密（refresh_token / client_secret）脱敏留空 → 不回传，靠后端保留
    expect(payload?.credentials).not.toHaveProperty('refresh_token')
    expect(payload?.credentials).not.toHaveProperty('client_secret')
  })

  it('native 输入新 kiro_api_key 则回传（旋转密钥）', async () => {
    const wrapper = mountModal(buildKiroNativeApiKeyAccount())
    await wrapper
      .findComponent(KiroNativeCredentials)
      .get('input[type="password"]')
      .setValue('ksk_rotated')
    await submit(wrapper)

    const payload = updateAccountMock.mock.calls[0]?.[1]
    expect(payload?.credentials?.kiro_api_key).toBe('ksk_rotated')
  })

  // 原生直连按 token 计费，credits_per_dollar 不生效，故不应写入 extra
  it('native 账号剥离 credits_per_dollar（按 token 计费）', async () => {
    const wrapper = mountModal(buildKiroNativeApiKeyAccount())
    await submit(wrapper)

    const payload = updateAccountMock.mock.calls[0]?.[1]
    expect(payload?.extra).not.toHaveProperty('credits_per_dollar')
  })

  it('legacy 账号保留 credits_per_dollar（按 credits 换算计费）', async () => {
    const wrapper = mountModal(buildKiroLegacyAccount())
    await submit(wrapper)

    const payload = updateAccountMock.mock.calls[0]?.[1]
    expect(payload?.extra?.credits_per_dollar).toBe(50)
  })

  it('native 模式下 Credits Per Dollar 输入框禁用', () => {
    const wrapper = mountModal(buildKiroNativeApiKeyAccount())
    const input = wrapper
      .findAll('input[type="number"]')
      .find((i) => i.attributes('placeholder')?.includes('credits'))
    expect(input, 'Credits Per Dollar 输入框应存在').toBeDefined()
    expect(input?.attributes('disabled')).toBeDefined()
  })
})

