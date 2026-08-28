import { flushPromises, shallowMount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import OpsErrorDetailsModal from '../OpsErrorDetailsModal.vue'

const mocks = vi.hoisted(() => ({ listRequestErrors: vi.fn(), listUpstreamErrors: vi.fn() }))
vi.mock('@/api/admin/ops', () => ({ opsAPI: mocks }))
vi.mock('vue-i18n', async (importOriginal) => {
  const actual = await importOriginal<typeof import('vue-i18n')>()
  return { ...actual, useI18n: () => ({ t: (key: string) => key }) }
})

describe('Ops session archive correlation', () => {
  beforeEach(() => {
    mocks.listRequestErrors.mockReset().mockResolvedValue({ items: [], total: 0 })
    mocks.listUpstreamErrors.mockReset().mockResolvedValue({ items: [], total: 0 })
  })

  it('forwards hydrated correlation_request_id to the request error query', async () => {
    const wrapper = shallowMount(OpsErrorDetailsModal, {
      props: {
        show: false,
        timeRange: '1h',
        errorType: 'request',
        correlationRequestId: 'corr-ops-route',
      },
      global: { stubs: { BaseDialog: { template: '<div><slot /></div>' }, Select: true, OpsErrorLogTable: true } },
    })
    await wrapper.setProps({ show: true })
    await flushPromises()
    expect(mocks.listRequestErrors).toHaveBeenCalledWith(expect.objectContaining({
      correlation_request_id: 'corr-ops-route',
    }))
  })
})
