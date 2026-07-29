import { beforeEach, describe, expect, it, vi } from 'vitest'
import { defineComponent } from 'vue'
import { mount } from '@vue/test-utils'

const { updateAccountMock, checkMixedChannelRiskMock, syncUpstreamModelsMock } = vi.hoisted(() => ({
  updateAccountMock: vi.fn(),
  checkMixedChannelRiskMock: vi.fn(),
  syncUpstreamModelsMock: vi.fn()
}))

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({
    showError: vi.fn(),
    showSuccess: vi.fn(),
    showInfo: vi.fn()
  })
}))

vi.mock('@/stores/auth', () => ({
  useAuthStore: () => ({ isSimpleMode: true })
}))

vi.mock('@/api/admin', () => ({
  adminAPI: {
    accounts: {
      update: updateAccountMock,
      checkMixedChannelRisk: checkMixedChannelRiskMock,
      syncUpstreamModels: syncUpstreamModelsMock
    },
    settings: {
      getWebSearchEmulationConfig: vi.fn().mockResolvedValue({ enabled: false, providers: [] }),
      getSettings: vi.fn().mockResolvedValue({})
    },
    tlsFingerprintProfiles: {
      list: vi.fn().mockResolvedValue([])
    }
  }
}))

vi.mock('@/api/admin/accounts', () => ({
  getAntigravityDefaultModelMapping: vi.fn()
}))

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({ t: (key: string) => key })
  }
})

import EditAccountModal from '../EditAccountModal.vue'

const BaseDialogStub = defineComponent({
  name: 'BaseDialog',
  props: { show: { type: Boolean, default: false } },
  template: '<div v-if="show"><slot /><slot name="footer" /></div>'
})

function buildLuminaAccount() {
  return {
    id: 7,
    name: 'Lumina',
    notes: '',
    platform: 'lumina',
    type: 'cookie',
    credentials: { cookie: [{ name: 'sid', value: 'x', domain: 'byteplus.com' }] },
    credentials_status: { has_cookie: true, has_password: false },
    extra: {},
    proxy_id: null,
    concurrency: 1,
    priority: 1,
    rate_multiplier: 1,
    status: 'active',
    group_ids: [],
    expires_at: null,
    auto_pause_on_expired: false
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
        ModelWhitelistSelector: true
      }
    }
  })
}

async function clickSyncUpstream(wrapper: ReturnType<typeof mountModal>) {
  const button = wrapper
    .findAll('button')
    .find((candidate) => candidate.text() === 'admin.accounts.syncUpstreamModels')
  expect(button).toBeDefined()
  await button!.trigger('click')
  await vi.waitFor(() => expect(syncUpstreamModelsMock).toHaveBeenCalledTimes(1))
  await wrapper.vm.$nextTick()
}

describe('EditAccountModal Lumina model mapping sync', () => {
  beforeEach(() => {
    updateAccountMock.mockReset()
    checkMixedChannelRiskMock.mockReset()
    syncUpstreamModelsMock.mockReset()
    checkMixedChannelRiskMock.mockResolvedValue({ has_risk: false })
  })

  it('fills the public ModelArk ID on the left and the Lumina req_key on the right', async () => {
    const account = buildLuminaAccount()
    updateAccountMock.mockResolvedValue(account)
    syncUpstreamModelsMock.mockResolvedValue({
      models: ['seedream-4-0-250828', 'dreamina-seedance-2-0-260128'],
      mappings: [
        { from: 'seedream-4-0-250828', to: 'high_aes_general_v40s' },
        { from: 'dreamina-seedance-2-0-260128', to: 'Doubao-Seedance-2.0-pro' }
      ]
    })

    const wrapper = mountModal(account)
    await clickSyncUpstream(wrapper)

    await wrapper.get('form#edit-account-form').trigger('submit.prevent')
    await vi.waitFor(() => expect(updateAccountMock).toHaveBeenCalledTimes(1))

    expect(updateAccountMock.mock.calls[0]?.[1]?.credentials?.model_mapping).toEqual({
      'seedream-4-0-250828': 'high_aes_general_v40s',
      'dreamina-seedance-2-0-260128': 'Doubao-Seedance-2.0-pro'
    })
  })

  it('falls back to identity mapping when the upstream reports no translation', async () => {
    const account = buildLuminaAccount()
    updateAccountMock.mockResolvedValue(account)
    syncUpstreamModelsMock.mockResolvedValue({ models: ['x2i_nano_lumina'] })

    const wrapper = mountModal(account)
    await clickSyncUpstream(wrapper)

    await wrapper.get('form#edit-account-form').trigger('submit.prevent')
    await vi.waitFor(() => expect(updateAccountMock).toHaveBeenCalledTimes(1))

    expect(updateAccountMock.mock.calls[0]?.[1]?.credentials?.model_mapping).toEqual({
      x2i_nano_lumina: 'x2i_nano_lumina'
    })
  })
})
