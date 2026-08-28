import { beforeEach, describe, expect, it, vi } from 'vitest'
import { defineComponent } from 'vue'
import { flushPromises, mount } from '@vue/test-utils'
import SessionArchiveView from '../SessionArchiveView.vue'

const mocks = vi.hoisted(() => ({
  getRuntime: vi.fn(), listPolicies: vi.fn(), listSessions: vi.fn(), listDeletionJobs: vi.fn(),
  getSession: vi.fn(), getRequestContent: vi.fn(), savePolicy: vi.fn(), deletePolicy: vi.fn(),
  preflightExport: vi.fn(), issueExportTicket: vi.fn(), exportDownloadURL: vi.fn(), createDeletionJob: vi.fn(), getDeletionJob: vi.fn(),
  stepUpRun: vi.fn(), showSuccess: vi.fn(), showError: vi.fn(), copy: vi.fn(), replace: vi.fn(),
  route: { query: { correlation_request_id: 'corr-route' } as Record<string, string> },
}))

vi.mock('../api', () => ({ default: mocks }))
vi.mock('@/stores/app', () => ({ useAppStore: () => ({ showSuccess: mocks.showSuccess, showError: mocks.showError }) }))
vi.mock('@/composables/useClipboard', () => ({ useClipboard: () => ({ copyToClipboard: mocks.copy }) }))
vi.mock('@/composables/useStepUp', () => ({
  useStepUp: () => ({ visible: { value: false }, blockedReason: { value: '' }, run: mocks.stepUpRun, onVerified: vi.fn(), onCancel: vi.fn() }),
  isStepUpBlocked: () => false,
  isStepUpCancelled: () => false,
  stepUpBlockReason: () => '',
}))
vi.mock('vue-router', () => ({ useRoute: () => mocks.route, useRouter: () => ({ replace: mocks.replace }) }))
vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string, params?: Record<string, unknown>) => params ? `${key} ${Object.values(params).join(' ')}` : key,
    }),
  }
})

const WorkspaceStub = defineComponent({
  props: ['sessions', 'total', 'page', 'pageSize', 'filters', 'loading', 'error', 'deletionJobs'],
  emits: ['filters-change', 'search', 'page', 'page-size', 'view', 'export', 'export-session', 'delete-session', 'delete-filter'],
  template: '<div data-test="workspace"><button data-test="view" @click="$emit(\'view\', 1)">view</button><button data-test="export" @click="$emit(\'export\', \'archive\')">export</button><button data-test="export-sft" @click="$emit(\'export\', \'sft\')">sft</button></div>',
})
const DetailStub = defineComponent({
  props: ['show', 'session', 'loading', 'content', 'contentLoadingKey'],
  emits: ['close', 'load-content', 'copy-content'],
  template: '<div data-test="detail" :data-show="show"><span data-test="content-count">{{ Object.keys(content).length }}</span><button data-test="load-content" @click="$emit(\'load-content\', 2, \'raw\')">load</button><button data-test="close" @click="$emit(\'close\')">close</button></div>',
})
const ConfirmStub = defineComponent({
  props: ['show', 'message'],
  emits: ['confirm', 'cancel'],
  template: '<div v-if="show" data-test="confirm-dialog" :data-message="message"><button data-test="confirm-action" @click="$emit(\'confirm\')">confirm</button></div>',
})

function mountView() {
  return mount(SessionArchiveView, {
    global: {
      stubs: {
        AppLayout: { template: '<div><slot /></div>' },
        ArchiveWorkspace: WorkspaceStub,
        ArchiveConfigPanel: true,
        SessionDetailDialog: DetailStub,
        ConfirmDialog: ConfirmStub,
        TotpStepUpDialog: true,
      },
    },
  })
}

describe('SessionArchiveView', () => {
  beforeEach(() => {
    Object.values(mocks).forEach((value) => {
      if (typeof value === 'function' && 'mockReset' in value) (value as ReturnType<typeof vi.fn>).mockReset()
    })
    mocks.route.query = { correlation_request_id: 'corr-route' }
    mocks.stepUpRun.mockImplementation((action: () => Promise<unknown>) => action())
    mocks.listSessions.mockResolvedValue({ items: [], total: 0, page: 1, page_size: 20, pages: 0 })
    mocks.getRuntime.mockResolvedValue({ enabled: false, process_status: 'disabled' })
    mocks.listPolicies.mockResolvedValue({ items: [] })
    mocks.listDeletionJobs.mockResolvedValue({ items: [], total: 0, page: 1, page_size: 10 })
    mocks.getSession.mockResolvedValue({ id: 1, protocol: 'openai', status: 'completed', turn_count: 1, request_count: 1, has_truncated: false, created_at: '', last_activity_at: '', turns: [] })
    mocks.getRequestContent.mockResolvedValue({ request_id: 2, kind: 'raw', text: 'secret', observed_bytes: 6, stored_bytes: 6, truncated: false, available: true })
    mocks.preflightExport.mockResolvedValue({ format: 'archive', matched_sessions: 1, eligible_samples: 1, skipped_samples: 0, skipped_reasons: {} })
    mocks.issueExportTicket.mockResolvedValue({ ticket: 'opaque', expires_at: '' })
    mocks.exportDownloadURL.mockReturnValue('/api/v1/session-archive/download/opaque')
  })

  it('hydrates correlation filters and never prefetches details or bodies', async () => {
    mountView()
    await flushPromises()
    expect(mocks.listSessions).toHaveBeenCalledWith(expect.objectContaining({ correlation_request_id: 'corr-route' }), 1, 20)
    expect(mocks.getSession).not.toHaveBeenCalled()
    expect(mocks.getRequestContent).not.toHaveBeenCalled()
  })

  it('loads narrow detail directly, loads Raw through step-up, and clears content on close', async () => {
    const wrapper = mountView()
    await flushPromises()
    await wrapper.get('[data-test="view"]').trigger('click')
    await flushPromises()
    expect(mocks.getSession).toHaveBeenCalledWith(1)
    expect(mocks.stepUpRun).not.toHaveBeenCalled()

    await wrapper.get('[data-test="load-content"]').trigger('click')
    await flushPromises()
    expect(mocks.stepUpRun).toHaveBeenCalledOnce()
    expect(mocks.getRequestContent).toHaveBeenCalledWith(2, 'raw')
    expect(wrapper.get('[data-test="content-count"]').text()).toBe('1')

    await wrapper.get('[data-test="close"]').trigger('click')
    await flushPromises()
    expect(wrapper.get('[data-test="content-count"]').text()).toBe('0')
  })

  it('shows export preflight counts before issuing a ticket and starts the confirmed native download', async () => {
    const click = vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(() => undefined)
    const wrapper = mountView()
    await flushPromises()
    await wrapper.get('[data-test="export"]').trigger('click')
    await flushPromises()
    expect(mocks.preflightExport).toHaveBeenCalledWith({ format: 'archive', filter: { correlation_request_id: 'corr-route' } })
    expect(mocks.issueExportTicket).not.toHaveBeenCalled()
    expect(wrapper.get('[data-test="confirm-dialog"]').attributes('data-message')).toContain('admin.sessionArchive.export.preflightSummary')

    await wrapper.get('[data-test="confirm-action"]').trigger('click')
    await flushPromises()
    expect(mocks.issueExportTicket).toHaveBeenCalledWith({ format: 'archive', filter: { correlation_request_id: 'corr-route' } })
    expect(wrapper.get('[data-test="archive-native-download"]').attributes('href')).toBe('/api/v1/session-archive/download/opaque')
    expect(click).toHaveBeenCalledOnce()
    click.mockRestore()
  })

  it('shows SFT skip reasons during preflight', async () => {
    mocks.preflightExport.mockResolvedValueOnce({
      format: 'sft', matched_sessions: 4, eligible_samples: 2, skipped_samples: 2, skipped_reasons: { truncated: 2 },
    })
    const wrapper = mountView()
    await flushPromises()
    await wrapper.get('[data-test="export-sft"]').trigger('click')
    await flushPromises()
    expect(mocks.preflightExport).toHaveBeenCalledWith({ format: 'sft', filter: { correlation_request_id: 'corr-route' } })
    expect(wrapper.get('[data-test="confirm-dialog"]').attributes('data-message')).toContain('truncated: 2')
    expect(mocks.issueExportTicket).not.toHaveBeenCalled()
  })
})
