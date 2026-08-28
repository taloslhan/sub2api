import { describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import ArchiveWorkspace from '../ArchiveWorkspace.vue'
import SessionDetailDialog from '../SessionDetailDialog.vue'
import { emptyArchiveFilters } from '../viewModel'
import type { ArchiveSessionDetail } from '../types'

vi.mock('vue-i18n', async (importOriginal) => {
  const actual = await importOriginal<typeof import('vue-i18n')>()
  return {
    ...actual,
    useI18n: () => ({ locale: { value: 'en' }, t: (key: string, params?: Record<string, unknown>) => key.replace(/\{(\w+)\}/g, (_, token) => String(params?.[token] ?? '')) }),
  }
})

const detail: ArchiveSessionDetail = {
  id: 1,
  protocol: 'openai_responses',
  client: 'codex',
  first_model: 'gpt-test',
  status: 'completed',
  capture_coverage: 'complete',
  turn_count: 1,
  request_count: 1,
  has_truncated: false,
  created_at: '2026-08-28T00:00:00Z',
  last_activity_at: '2026-08-28T00:00:02Z',
  turns: [{
    id: 10,
    sequence: 1,
    status: 'completed',
    started_at: '2026-08-28T00:00:00Z',
    requests: [{
      id: 20,
      sequence: 1,
      correlation_request_id: 'corr-20',
      endpoint: '/v1/responses',
      model: 'gpt-test',
      status: 'completed',
      has_truncated: false,
      attempts: [],
      created_at: '2026-08-28T00:00:00Z',
    }],
  }],
}

describe('session archive components', () => {
  it('renders narrow rows and server deletion progress', () => {
    const wrapper = mount(ArchiveWorkspace, {
      props: {
        sessions: [detail], total: 1, page: 1, pageSize: 20, filters: emptyArchiveFilters(), loading: false, error: '',
        deletionJobs: [{ id: 9, status: 'running', matched_sessions: 10, processed_sessions: 4, deleted_sessions: 4, failed_sessions: 1, created_at: '' }],
      },
      global: { stubs: { Pagination: true, RouterLink: true } },
    })
    expect(wrapper.text()).toContain('gpt-test')
    expect(wrapper.get('[role="progressbar"]').attributes('aria-valuenow')).toBe('40')
    expect(wrapper.html()).not.toContain('secret response body')
  })

  it('shows the Turn timeline and emits an on-demand Raw read without rendering HTML', async () => {
    const wrapper = mount(SessionDetailDialog, {
      props: { show: true, session: detail, loading: false, content: {}, contentLoadingKey: '' },
      global: {
        stubs: {
          BaseDialog: { props: ['show'], template: '<div v-if="show"><slot /></div>' },
          RouterLink: { template: '<a><slot /></a>' },
        },
      },
    })
    expect(wrapper.text()).toContain('admin.sessionArchive.detail.turn')
    expect(wrapper.text()).toContain('corr-20')
    const tabs = wrapper.findAll('[role="tab"]')
    await tabs.find((tab) => tab.text().includes('.raw'))!.trigger('click')
    expect(wrapper.text()).toContain('admin.sessionArchive.detail.contentNotLoaded')
    await wrapper.get('button.btn-secondary').trigger('click')
    expect(wrapper.emitted('load-content')).toEqual([[20, 'raw']])

    await wrapper.setProps({
      content: {
        '20:raw': { request_id: 20, kind: 'raw', text: '<img src=x onerror=alert(1)>', observed_bytes: 10, stored_bytes: 10, truncated: false, available: true },
      },
    })
    expect(wrapper.get('pre').text()).toContain('<img src=x onerror=alert(1)>')
    expect(wrapper.find('pre img').exists()).toBe(false)

	await tabs.find((tab) => tab.text().includes('.attachment'))!.trigger('click')
	await wrapper.setProps({
		content: {
			'20:attachment': {
				request_id: 20, kind: 'attachment', observed_bytes: 12, stored_bytes: 12, truncated: false, available: true,
				parts: [
					{ ref_id: 41, owner_type: 'request', owner_id: 20, sequence_no: 1, direction: 'client_to_gateway', content_type: 'application/json', value: { file_id: 'file-1' }, observed_bytes: 4, stored_bytes: 4, truncated: false, available: true },
					{ ref_id: 42, owner_type: 'request', owner_id: 20, sequence_no: 2, direction: 'client_to_gateway', content_type: 'image/png', encoding: 'base64', base64: 'iVBORw0KGgo=', observed_bytes: 8, stored_bytes: 8, truncated: false, available: true },
				],
			},
		},
	})
	const partBodies = wrapper.findAll('article pre')
	expect(partBodies).toHaveLength(2)
	expect(partBodies[0].text()).toContain('file-1')
	expect(partBodies[1].text()).toBe('iVBORw0KGgo=')
  })
})
