import { describe, expect, it } from 'vitest'
import {
  archiveFilterParams,
  contentText,
  createArchivePolicy,
  DEFAULT_BODY_LIMIT_BYTES,
  deletionProgress,
  emptyArchiveFilters,
} from '../viewModel'

describe('session archive view model', () => {
  it('hydrates and normalizes exact filters without sending empty values', () => {
    const filters = emptyArchiveFilters('corr-123')
    filters.user_id = '9'
    filters.group_id = 'invalid'
    filters.start_at = '2026-08-28T10:00'
    expect(archiveFilterParams(filters)).toEqual({
      correlation_request_id: 'corr-123',
      user_id: 9,
      from: new Date(filters.start_at).toISOString(),
    })
  })

  it('keeps transformed upstream capture off in a default global policy', () => {
    expect(createArchivePolicy()).toMatchObject({
      scope_type: 'global',
      scope_id: 0,
      state: 'off',
      capture_request: true,
      capture_response: true,
      capture_transformed_request: false,
      body_limit_bytes: DEFAULT_BODY_LIMIT_BYTES,
      retention_days: 30,
    })
    expect(createArchivePolicy('user')).toMatchObject({ state: 'inherit', scope_id: undefined })
  })

  it('renders structured content as inert text and bounds deletion progress', () => {
    expect(contentText({
      request_id: 1, kind: 'tool', value: { html: '<script>alert(1)</script>' },
      observed_bytes: 10, stored_bytes: 10, truncated: false, available: true,
    })).toContain('<script>alert(1)</script>')
    expect(contentText({
      request_id: 2, kind: 'attachment', encoding: 'base64', base64: 'iVBORw0KGgo=',
      observed_bytes: 8, stored_bytes: 8, truncated: false, available: true,
    })).toBe('iVBORw0KGgo=')
    expect(contentText({
      request_id: 3, kind: 'attachment', observed_bytes: 20, stored_bytes: 20, truncated: false, available: true,
      parts: [
        { ref_id: 31, owner_type: 'request', owner_id: 3, sequence_no: 1, content_type: 'application/json', value: { file_id: 'file-1' }, observed_bytes: 12, stored_bytes: 12, truncated: false, available: true },
        { ref_id: 32, owner_type: 'request', owner_id: 3, sequence_no: 2, content_type: 'image/png', encoding: 'base64', base64: 'iVBORw0KGgo=', observed_bytes: 8, stored_bytes: 8, truncated: false, available: true },
      ],
    })).toBe('Part 1 · ref 31 · application/json\n{\n  "file_id": "file-1"\n}\n\nPart 2 · ref 32 · image/png\niVBORw0KGgo=')
    expect(deletionProgress(5, 10)).toBe(50)
    expect(deletionProgress(20, 10)).toBe(100)
    expect(deletionProgress(1, 0)).toBe(0)
  })
})
