export type ArchiveID = string | number
export type ArchivePageTab = 'sessions' | 'config'
export type ArchiveContentKind = 'request' | 'upstream' | 'response' | 'tool' | 'attachment' | 'raw'
export type ArchivePolicyState = 'inherit' | 'on' | 'off'
export type ArchivePolicyScope = 'global' | 'group' | 'user' | 'api_key'
export type ArchiveExportFormat = 'archive' | 'sft'

export interface ArchiveFilters {
  correlation_request_id: string
  user_id: string
  api_key_id: string
  group_id: string
  model: string
  client: string
  status: string
  start_at: string
  end_at: string
}

export interface ArchiveSessionSummary {
  id: ArchiveID
  user_id?: number | null
  username?: string
  user_email?: string
  api_key_id?: number | null
  api_key_name?: string
  group_id?: number | null
  group_name?: string
  protocol: string
  client?: string
  first_model?: string
  last_model?: string
  status: string
  capture_coverage?: string
  merge_method?: string
  turn_count: number
  request_count: number
  has_truncated: boolean
  created_at: string
  last_activity_at: string
  expires_at?: string | null
}

export interface ArchiveAttempt {
  id: ArchiveID
  sequence: number
  account_id?: number | null
  account_name?: string
  transform_type?: string
  upstream_status?: string
  upstream_status_code?: number | null
  error_category?: string
  latency_ms?: number | null
  final: boolean
  created_at?: string
  completed_at?: string | null
}

export interface ArchiveRequest {
  id: ArchiveID
  sequence: number
  correlation_request_id: string
  billing_request_id?: string
  upstream_request_id?: string
  endpoint?: string
  model?: string
  status: string
  error_category?: string
  client_disconnected?: boolean
  has_truncated: boolean
  available_content?: ArchiveContentKind[]
  attempts: ArchiveAttempt[]
  created_at: string
  completed_at?: string | null
}

export interface ArchiveTurn {
  id: ArchiveID
  sequence: number
  protocol_turn_id?: string
  status: string
  started_at: string
  completed_at?: string | null
  requests: ArchiveRequest[]
}

export interface ArchiveSessionDetail extends ArchiveSessionSummary {
  stable_identifier_digest?: string
  policy_snapshot?: ArchivePolicySnapshot
  turns: ArchiveTurn[]
}

export interface ArchiveContentPart {
  ref_id: ArchiveID
  owner_type: string
  owner_id: ArchiveID
  sequence_no: number
  direction?: string
  occurred_at?: string
  content_type?: string
  encoding?: 'base64' | string
  base64?: string
  text?: string
  value?: unknown
  observed_bytes: number
  stored_bytes: number
  truncated: boolean
  dropped_reason?: string
  available: boolean
}

export interface ArchiveContent {
  request_id: ArchiveID
  kind: ArchiveContentKind
  content_type?: string
  observed_bytes: number
  stored_bytes: number
  truncated: boolean
  dropped_reason?: string
  available: boolean
  frame_count?: number
  direction?: string
  occurred_at?: string
  parts?: ArchiveContentPart[]
  // 兼容升级期间旧节点返回的单正文结构。
  encoding?: 'base64' | string
  base64?: string
  text?: string
  value?: unknown
}

export interface ArchiveSessionPage {
  items: ArchiveSessionSummary[]
  total: number
  page: number
  page_size: number
  pages: number
}

export interface ArchiveRuntimeStatus {
  enabled: boolean
  process_status: 'disabled' | 'running' | 'degraded' | 'error' | string
  storage_status: string
  database_status: string
  active_key_id?: string
  bucket?: string
  prefix?: string
  queue_events: number
  queue_event_capacity: number
  queue_bytes: number
  queue_byte_capacity: number
  enqueued_total: number
  dropped_total: number
  truncated_total: number
  stored_total: number
  failed_total: number
  storage_failures: number
  export_failures: number
  pending_backlog: number
  gc_backlog: number
  last_error?: string
  last_success_at?: string
}

export interface ArchivePolicySnapshot {
  state: Exclude<ArchivePolicyState, 'inherit'>
  capture_request: boolean
  capture_response: boolean
  capture_transformed_request: boolean
  capture_tools: boolean
  capture_attachments: boolean
  body_limit_bytes: number
  retention_days: number
  matched_scope_type?: ArchivePolicyScope
  matched_scope_id?: number
}

export interface ArchivePolicy {
  id?: ArchiveID
  scope_type: ArchivePolicyScope
  scope_id?: number
  scope_name?: string
  state: ArchivePolicyState
  capture_request: boolean
  capture_response: boolean
  capture_transformed_request: boolean
  capture_tools: boolean
  capture_attachments: boolean
  body_limit_bytes: number
  retention_days: number
  updated_at?: string
  updated_by?: number
}

export interface ArchivePolicyList {
  items: ArchivePolicy[]
  effective_global: ArchivePolicySnapshot
}

export interface ArchiveExportTicket {
  ticket: string
  expires_at: string
  download_url?: string
  matched_sessions?: number
  eligible_samples?: number
  skipped_samples?: number
  skipped_reasons?: Record<string, number>
}

export interface ArchiveExportRequest {
  format: ArchiveExportFormat
  session_id?: ArchiveID
  filter?: Record<string, string | number>
}

export interface ArchiveExportPreflight {
  format: ArchiveExportFormat
  matched_sessions: number
  eligible_samples: number
  skipped_samples: number
  skipped_reasons: Record<string, number>
}

export interface ArchiveDeletionJob {
  id: ArchiveID
  status: 'pending' | 'running' | 'completed' | 'failed' | 'canceled' | string
  matched_sessions: number
  processed_sessions: number
  deleted_sessions: number
  failed_sessions: number
  released_blobs?: number
  last_error?: string
  created_at: string
  started_at?: string | null
  finished_at?: string | null
}

export interface ArchiveDeletionJobPage {
  items: ArchiveDeletionJob[]
  total: number
  page: number
  page_size: number
}

export interface ArchiveDeleteRequest {
  session_ids?: ArchiveID[]
  filter?: Record<string, string | number>
}
