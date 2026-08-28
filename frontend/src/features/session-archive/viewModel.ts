import type {
  ArchiveContent,
  ArchiveContentPart,
  ArchiveFilters,
  ArchivePolicy,
  ArchivePolicyScope,
} from './types'

export const DEFAULT_BODY_LIMIT_BYTES = 64 * 1024 * 1024
export const DEFAULT_RETENTION_DAYS = 30

export function emptyArchiveFilters(correlationRequestId = ''): ArchiveFilters {
  return {
    correlation_request_id: correlationRequestId,
    user_id: '',
    api_key_id: '',
    group_id: '',
    model: '',
    client: '',
    status: '',
    start_at: '',
    end_at: '',
  }
}

function toISO(value: string): string | undefined {
  if (!value.trim()) return undefined
  const date = new Date(value)
  return Number.isNaN(date.getTime()) ? undefined : date.toISOString()
}

export function archiveFilterParams(filters: ArchiveFilters): Record<string, string | number> {
  const params: Record<string, string | number> = {}
  for (const key of ['correlation_request_id', 'model', 'client', 'status'] as const) {
    const value = filters[key].trim()
    if (value) params[key] = value
  }
  for (const key of ['user_id', 'api_key_id', 'group_id'] as const) {
    const value = Number(filters[key])
    if (Number.isInteger(value) && value > 0) params[key] = value
  }
  const startAt = toISO(filters.start_at)
  const endAt = toISO(filters.end_at)
  if (startAt) params.from = startAt
  if (endAt) params.to = endAt
  return params
}

export function cloneArchiveData<T>(value: T): T {
  return JSON.parse(JSON.stringify(value)) as T
}

export function createArchivePolicy(scopeType: ArchivePolicyScope = 'global'): ArchivePolicy {
  return {
    scope_type: scopeType,
    scope_id: scopeType === 'global' ? 0 : undefined,
    state: scopeType === 'global' ? 'off' : 'inherit',
    capture_request: true,
    capture_response: true,
    capture_transformed_request: false,
    capture_tools: true,
    capture_attachments: true,
    body_limit_bytes: DEFAULT_BODY_LIMIT_BYTES,
    retention_days: DEFAULT_RETENTION_DAYS,
  }
}

export function contentPartText(content: ArchiveContentPart | ArchiveContent): string {
  if (content.encoding === 'base64' && typeof content.base64 === 'string') return content.base64
  if (typeof content.text === 'string') return content.text
  if (content.value === undefined || content.value === null) return ''
  if (typeof content.value === 'string') return content.value
  return JSON.stringify(content.value, null, 2)
}

export function contentText(content: ArchiveContent | null): string {
  if (!content) return ''
  if (content.parts?.length) {
    return content.parts.map((part, index) => {
      const metadata = [`Part ${index + 1}`, `ref ${String(part.ref_id)}`, part.content_type || 'unknown'].join(' · ')
      const body = part.available ? contentPartText(part) : `[unavailable${part.dropped_reason ? `: ${part.dropped_reason}` : ''}]`
      return `${metadata}\n${body}`
    }).join('\n\n')
  }
  return contentPartText(content)
}

export function deletionProgress(processed: number, total: number): number {
  if (total <= 0) return 0
  return Math.min(100, Math.max(0, Math.round((processed / total) * 100)))
}
