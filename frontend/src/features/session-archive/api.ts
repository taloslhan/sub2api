import { apiClient } from '@/api/client'
import { buildApiUrl } from '@/api/url'
import type {
  ArchiveContent,
  ArchiveContentKind,
  ArchiveDeleteRequest,
  ArchiveDeletionJob,
  ArchiveDeletionJobPage,
  ArchiveExportPreflight,
  ArchiveExportRequest,
  ArchiveExportTicket,
  ArchiveFilters,
  ArchiveID,
  ArchivePolicy,
  ArchivePolicyList,
  ArchiveRuntimeStatus,
  ArchiveSessionDetail,
  ArchiveSessionPage,
} from './types'
import { archiveFilterParams } from './viewModel'

const basePath = '/admin/session-archive'

export async function getRuntime(): Promise<ArchiveRuntimeStatus> {
  const { data } = await apiClient.get<ArchiveRuntimeStatus>(`${basePath}/runtime`)
  return data
}

export async function listSessions(
  filters: ArchiveFilters,
  page: number,
  pageSize: number,
): Promise<ArchiveSessionPage> {
  const { data } = await apiClient.get<ArchiveSessionPage>(`${basePath}/sessions`, {
    params: { page, page_size: pageSize, ...archiveFilterParams(filters) },
  })
  return data
}

export async function getSession(id: ArchiveID): Promise<ArchiveSessionDetail> {
  const { data } = await apiClient.get<ArchiveSessionDetail>(`${basePath}/sessions/${encodeURIComponent(String(id))}`)
  return data
}

export async function getRequestContent(id: ArchiveID, kind: ArchiveContentKind): Promise<ArchiveContent> {
  const { data } = await apiClient.get<ArchiveContent>(`${basePath}/requests/${encodeURIComponent(String(id))}/content`, {
    params: { kind },
  })
  return data
}

export async function listPolicies(): Promise<ArchivePolicyList> {
  const { data } = await apiClient.get<ArchivePolicyList>(`${basePath}/policies`)
  return data
}

export async function savePolicy(policy: ArchivePolicy): Promise<ArchivePolicy> {
  const { data } = await apiClient.put<ArchivePolicy>(`${basePath}/policies`, policy)
  return data
}

export async function deletePolicy(policy: Pick<ArchivePolicy, 'scope_type' | 'scope_id'>): Promise<void> {
  await apiClient.delete(`${basePath}/policies`, { params: policy })
}

export async function preflightExport(payload: ArchiveExportRequest): Promise<ArchiveExportPreflight> {
  const { data } = await apiClient.post<ArchiveExportPreflight>(`${basePath}/export-preflight`, payload)
  return data
}

export async function issueExportTicket(payload: ArchiveExportRequest): Promise<ArchiveExportTicket> {
  const { data } = await apiClient.post<ArchiveExportTicket>(`${basePath}/export-tickets`, payload)
  return data
}

export function exportDownloadURL(ticket: ArchiveExportTicket): string {
  // The browser receives only the opaque capability; JWTs and filters never enter the URL.
  return buildApiUrl(`/session-archive/download/${encodeURIComponent(ticket.ticket)}`)
}

export async function createDeletionJob(payload: ArchiveDeleteRequest): Promise<ArchiveDeletionJob> {
  const { data } = await apiClient.post<ArchiveDeletionJob>(`${basePath}/deletion-jobs`, payload)
  return data
}

export async function listDeletionJobs(page = 1, pageSize = 10): Promise<ArchiveDeletionJobPage> {
  const { data } = await apiClient.get<ArchiveDeletionJobPage>(`${basePath}/deletion-jobs`, {
    params: { page, page_size: pageSize },
  })
  return data
}

export async function getDeletionJob(id: ArchiveID): Promise<ArchiveDeletionJob> {
  const { data } = await apiClient.get<ArchiveDeletionJob>(`${basePath}/deletion-jobs/${encodeURIComponent(String(id))}`)
  return data
}

export const sessionArchiveAPI = {
  getRuntime,
  listSessions,
  getSession,
  getRequestContent,
  listPolicies,
  savePolicy,
  deletePolicy,
  preflightExport,
  issueExportTicket,
  exportDownloadURL,
  createDeletionJob,
  listDeletionJobs,
  getDeletionJob,
}

export default sessionArchiveAPI
