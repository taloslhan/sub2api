import { beforeEach, describe, expect, it, vi } from 'vitest'
import { emptyArchiveFilters } from '../viewModel'

const client = vi.hoisted(() => ({ get: vi.fn(), put: vi.fn(), post: vi.fn(), delete: vi.fn() }))
vi.mock('@/api/client', () => ({ apiClient: client }))
vi.mock('@/api/url', () => ({ buildApiUrl: (path: string) => `/api/v1${path}` }))

import sessionArchiveAPI from '../api'

describe('session archive API', () => {
  beforeEach(() => Object.values(client).forEach((mock) => mock.mockReset()))

  it('keeps list and sensitive content endpoints separate', async () => {
    client.get.mockResolvedValue({ data: { items: [], total: 0 } })
    const filters = emptyArchiveFilters('corr-1')
    filters.start_at = '2026-08-28T10:00'
    filters.end_at = '2026-08-28T11:00'
    await sessionArchiveAPI.listSessions(filters, 2, 20)
    expect(client.get).toHaveBeenCalledWith('/admin/session-archive/sessions', {
      params: {
        page: 2,
        page_size: 20,
        correlation_request_id: 'corr-1',
        from: new Date(filters.start_at).toISOString(),
        to: new Date(filters.end_at).toISOString(),
      },
    })

    client.get.mockResolvedValue({ data: { request_id: 8, kind: 'raw' } })
    await sessionArchiveAPI.getRequestContent(8, 'raw')
    expect(client.get).toHaveBeenCalledWith('/admin/session-archive/requests/8/content', { params: { kind: 'raw' } })
  })

  it('issues tickets and builds a native one-time download URL', async () => {
    client.post.mockResolvedValueOnce({ data: { format: 'sft', matched_sessions: 7, eligible_samples: 5, skipped_samples: 2, skipped_reasons: { truncated: 2 } } })
    await expect(sessionArchiveAPI.preflightExport({ format: 'sft', filter: { model: 'gpt-test' } })).resolves.toMatchObject({ matched_sessions: 7, skipped_samples: 2 })
    expect(client.post).toHaveBeenCalledWith('/admin/session-archive/export-preflight', { format: 'sft', filter: { model: 'gpt-test' } })

    client.post.mockResolvedValue({ data: { ticket: 'opaque-ticket', expires_at: '2026-08-28T00:01:00Z' } })
    const ticket = await sessionArchiveAPI.issueExportTicket({ format: 'archive', session_id: 7 })
    expect(client.post).toHaveBeenCalledWith('/admin/session-archive/export-tickets', { format: 'archive', session_id: 7 })
    expect(sessionArchiveAPI.exportDownloadURL(ticket)).toBe('/api/v1/session-archive/download/opaque-ticket')
  })

  it('uses persistent deletion jobs instead of reporting synchronous completion', async () => {
    client.post.mockResolvedValue({ data: { id: 11, status: 'pending' } })
    const result = await sessionArchiveAPI.createDeletionJob({ session_ids: [3] })
    expect(client.post).toHaveBeenCalledWith('/admin/session-archive/deletion-jobs', { session_ids: [3] })
    expect(result).toMatchObject({ id: 11, status: 'pending' })
  })
})
