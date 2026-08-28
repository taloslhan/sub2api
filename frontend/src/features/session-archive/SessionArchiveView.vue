<template>
  <AppLayout>
    <div class="mx-auto max-w-[1600px] pb-8">
      <header class="mb-6 flex flex-wrap items-end justify-between gap-4">
        <div>
          <p class="text-xs font-semibold uppercase tracking-[0.16em] text-primary-600 dark:text-primary-400">{{ t('nav.securityAudit') }}</p>
          <h1 class="mt-1 text-2xl font-semibold tracking-tight text-gray-950 dark:text-white">{{ t('admin.sessionArchive.title') }}</h1>
          <p class="mt-2 max-w-3xl text-sm text-gray-500 dark:text-dark-300">{{ t('admin.sessionArchive.description') }}</p>
        </div>
        <button type="button" class="btn btn-secondary btn-sm" :disabled="loading.initial" @click="refreshActiveTab">{{ t('common.refresh') }}</button>
      </header>

      <div class="mb-4" role="tablist" :aria-label="t('admin.sessionArchive.title')">
        <div class="tabs inline-flex">
          <button
            v-for="tab in pageTabs"
            :key="tab.id"
            type="button"
            role="tab"
            class="tab"
            :class="{ 'tab-active': activeTab === tab.id }"
            :aria-selected="activeTab === tab.id"
            @click="activeTab = tab.id"
          >
            {{ tab.label }}
          </button>
        </div>
      </div>

      <main class="card px-4 sm:px-6 lg:px-8">
        <ArchiveWorkspace
          v-show="activeTab === 'sessions'"
          :sessions="sessions.items"
          :total="sessions.total"
          :page="sessions.page"
          :page-size="sessions.page_size"
          :filters="filters"
          :loading="loading.sessions"
          :error="loadErrors.sessions"
          :deletion-jobs="deletionJobs"
          @filters-change="filters = $event"
          @search="applyFilters"
          @page="changePage"
          @page-size="changePageSize"
          @view="openSession"
          @export="exportFiltered"
          @export-session="exportSession"
          @delete-session="requestDeleteSession"
          @delete-filter="requestDeleteFiltered"
        />
        <ArchiveConfigPanel
          v-show="activeTab === 'config'"
          :runtime="runtime"
          :policies="policies"
          :loading="loading.config"
          @save-policy="savePolicy"
          @delete-policy="deletePolicy"
        />
      </main>
    </div>

    <SessionDetailDialog
      :show="showDetail"
      :session="activeSession"
      :loading="loading.detail"
      :content="requestContent"
      :content-loading-key="contentLoadingKey"
      @close="closeDetail"
      @load-content="loadRequestContent"
      @copy-content="copyContent"
    />
    <ConfirmDialog
      :show="pendingDelete !== null"
      :title="t('admin.sessionArchive.deletion.confirmTitle')"
      :message="pendingDelete?.kind === 'session' ? t('admin.sessionArchive.deletion.confirmSession') : t('admin.sessionArchive.deletion.confirmFiltered')"
      :confirm-text="t('admin.sessionArchive.deletion.confirm')"
      danger
      @confirm="confirmDelete"
      @cancel="pendingDelete = null"
    />
    <ConfirmDialog
      :show="pendingExport !== null"
      :title="t('admin.sessionArchive.export.preflightTitle')"
      :message="exportPreflightMessage"
      :confirm-text="t('admin.sessionArchive.export.confirm')"
      @confirm="confirmExport"
      @cancel="pendingExport = null"
    />
    <a
      ref="downloadAnchor"
      :href="downloadURL"
      class="sr-only"
      rel="noopener noreferrer"
      referrerpolicy="no-referrer"
      data-test="archive-native-download"
    >{{ t('admin.sessionArchive.actions.download') }}</a>
  </AppLayout>
</template>

<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, reactive, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRoute, useRouter } from 'vue-router'
import AppLayout from '@/components/layout/AppLayout.vue'
import ConfirmDialog from '@/components/common/ConfirmDialog.vue'
import { useClipboard } from '@/composables/useClipboard'
import { useAppStore } from '@/stores/app'
import { extractApiErrorMessage } from '@/utils/apiError'
import ArchiveWorkspace from './ArchiveWorkspace.vue'
import ArchiveConfigPanel from './ArchiveConfigPanel.vue'
import SessionDetailDialog from './SessionDetailDialog.vue'
import sessionArchiveAPI from './api'
import type {
  ArchiveContent,
  ArchiveContentKind,
  ArchiveDeletionJob,
  ArchiveExportFormat,
  ArchiveExportPreflight,
  ArchiveExportRequest,
  ArchiveFilters,
  ArchiveID,
  ArchivePageTab,
  ArchivePolicy,
  ArchiveRuntimeStatus,
  ArchiveSessionDetail,
  ArchiveSessionPage,
} from './types'
import { archiveFilterParams, cloneArchiveData, contentText, emptyArchiveFilters } from './viewModel'

const { t } = useI18n()
const route = useRoute()
const router = useRouter()
const appStore = useAppStore()
const { copyToClipboard } = useClipboard()

const activeTab = ref<ArchivePageTab>(route.query.tab === 'config' ? 'config' : 'sessions')
const pageTabs = computed(() => [
  { id: 'sessions' as const, label: t('admin.sessionArchive.tabs.sessions') },
  { id: 'config' as const, label: t('admin.sessionArchive.tabs.config') },
])
const routeCorrelation = queryString(route.query.correlation_request_id)
const filters = ref<ArchiveFilters>(emptyArchiveFilters(routeCorrelation))
const appliedFilters = ref<ArchiveFilters>(cloneArchiveData(filters.value))
const sessions = reactive<ArchiveSessionPage>({ items: [], total: 0, page: 1, page_size: 20, pages: 0 })
const runtime = ref<ArchiveRuntimeStatus | null>(null)
const policies = ref<ArchivePolicy[]>([])
const deletionJobs = ref<ArchiveDeletionJob[]>([])
const activeSession = ref<ArchiveSessionDetail | null>(null)
const showDetail = ref(false)
const requestContent = ref<Record<string, ArchiveContent>>({})
const contentLoadingKey = ref('')
const loading = reactive({ initial: false, sessions: false, detail: false, config: false, deleting: false, exporting: false })
const loadErrors = reactive({ sessions: '' })
const pendingDelete = ref<{ kind: 'session'; id: ArchiveID } | { kind: 'filter' } | null>(null)
const pendingExport = ref<{ payload: ArchiveExportRequest; preflight: ArchiveExportPreflight } | null>(null)
const downloadAnchor = ref<HTMLAnchorElement | null>(null)
const downloadURL = ref('')
const exportPreflightMessage = computed(() => {
  if (!pendingExport.value) return ''
  const { preflight } = pendingExport.value
  const summary = t('admin.sessionArchive.export.preflightSummary', {
    matched: preflight.matched_sessions,
    eligible: preflight.eligible_samples,
    skipped: preflight.skipped_samples,
  })
  const reasons = Object.entries(preflight.skipped_reasons || {})
    .filter(([, count]) => count > 0)
    .map(([reason, count]) => `${reason}: ${count}`)
    .join(' · ')
  return reasons ? `${summary} ${t('admin.sessionArchive.export.preflightReasons', { reasons })}` : summary
})
let downloadClearTimer: number | null = null
let deletionPollTimer: number | null = null

function queryString(value: unknown): string {
  if (typeof value === 'string') return value
  if (Array.isArray(value)) return value.find((item): item is string => typeof item === 'string') || ''
  return ''
}
function showActionError(error: unknown, fallbackKey: string) {
  appStore.showError(extractApiErrorMessage(error, t(fallbackKey)))
}

async function loadSessions() {
  loading.sessions = true
  loadErrors.sessions = ''
  try {
    const result = await sessionArchiveAPI.listSessions(appliedFilters.value, sessions.page, sessions.page_size)
    Object.assign(sessions, result, { items: result.items || [] })
  } catch (error) {
    loadErrors.sessions = extractApiErrorMessage(error, t('admin.sessionArchive.errors.loadSessions'))
  } finally {
    loading.sessions = false
  }
}
async function loadConfig() {
  loading.config = true
  try {
    const [nextRuntime, policyList] = await Promise.all([sessionArchiveAPI.getRuntime(), sessionArchiveAPI.listPolicies()])
    runtime.value = nextRuntime
    policies.value = policyList.items || []
  } catch (error) {
    appStore.showError(extractApiErrorMessage(error, t('admin.sessionArchive.errors.loadConfig')))
  } finally {
    loading.config = false
  }
}
async function loadDeletionJobs() {
  try {
    const result = await sessionArchiveAPI.listDeletionJobs(1, 10)
    deletionJobs.value = result.items || []
    scheduleDeletionPoll()
  } catch {
    deletionJobs.value = []
  }
}
async function loadInitial() {
  loading.initial = true
  await Promise.allSettled([loadSessions(), loadConfig(), loadDeletionJobs()])
  loading.initial = false
}

function applyFilters(value: ArchiveFilters) {
  filters.value = cloneArchiveData(value)
  appliedFilters.value = cloneArchiveData(value)
  sessions.page = 1
  void router.replace({
    query: {
      ...route.query,
      correlation_request_id: value.correlation_request_id.trim() || undefined,
    },
  })
  void loadSessions()
}
function changePage(page: number) {
  sessions.page = page
  void loadSessions()
}
function changePageSize(pageSize: number) {
  sessions.page_size = pageSize
  sessions.page = 1
  void loadSessions()
}
async function openSession(id: ArchiveID) {
  showDetail.value = true
  activeSession.value = null
  requestContent.value = {}
  loading.detail = true
  try {
    const detail = await sessionArchiveAPI.getSession(id)
    activeSession.value = {
      ...detail,
      turns: (detail.turns || []).map((turn) => ({
        ...turn,
        requests: (turn.requests || []).map((request) => ({ ...request, attempts: request.attempts || [] })),
      })),
    }
  } catch (error) {
    showDetail.value = false
    appStore.showError(extractApiErrorMessage(error, t('admin.sessionArchive.errors.loadDetail')))
  } finally {
    loading.detail = false
  }
}
function closeDetail() {
  showDetail.value = false
  activeSession.value = null
  requestContent.value = {}
  contentLoadingKey.value = ''
}
async function loadRequestContent(requestID: ArchiveID, kind: ArchiveContentKind) {
  const key = `${String(requestID)}:${kind}`
  contentLoadingKey.value = key
  try {
    const content = await sessionArchiveAPI.getRequestContent(requestID, kind)
    requestContent.value = { ...requestContent.value, [key]: content }
  } catch (error) {
    showActionError(error, 'admin.sessionArchive.errors.loadContent')
  } finally {
    if (contentLoadingKey.value === key) contentLoadingKey.value = ''
  }
}
async function copyContent(content: ArchiveContent) {
  try {
    await copyToClipboard(contentText(content), t('common.copiedToClipboard'))
  } catch (error) {
    showActionError(error, 'admin.sessionArchive.errors.copyContent')
  }
}

async function savePolicy(policy: ArchivePolicy) {
  loading.config = true
  try {
    await sessionArchiveAPI.savePolicy(policy)
    appStore.showSuccess(t('admin.sessionArchive.messages.policySaved'))
    await loadConfig()
  } catch (error) {
    showActionError(error, 'admin.sessionArchive.errors.savePolicy')
  } finally {
    loading.config = false
  }
}
async function deletePolicy(policy: ArchivePolicy) {
  loading.config = true
  try {
    await sessionArchiveAPI.deletePolicy(policy)
    appStore.showSuccess(t('admin.sessionArchive.messages.policyDeleted'))
    await loadConfig()
  } catch (error) {
    showActionError(error, 'admin.sessionArchive.errors.deletePolicy')
  } finally {
    loading.config = false
  }
}

async function prepareDownload(payload: ArchiveExportRequest) {
  if (loading.exporting) return
  loading.exporting = true
  try {
    const preflight = await sessionArchiveAPI.preflightExport(payload)
    pendingExport.value = { payload, preflight }
  } catch (error) {
    showActionError(error, 'admin.sessionArchive.errors.exportPreflight')
  } finally {
    loading.exporting = false
  }
}
async function issueDownload(payload: ArchiveExportRequest) {
  if (loading.exporting) return
  loading.exporting = true
  try {
    const ticket = await sessionArchiveAPI.issueExportTicket(payload)
    downloadURL.value = sessionArchiveAPI.exportDownloadURL(ticket)
    await nextTick()
    downloadAnchor.value?.click()
    appStore.showSuccess(t('admin.sessionArchive.messages.exportStarted'))
    if (downloadClearTimer) window.clearTimeout(downloadClearTimer)
    downloadClearTimer = window.setTimeout(() => { downloadURL.value = '' }, 1000)
  } catch (error) {
    showActionError(error, 'admin.sessionArchive.errors.export')
  } finally {
    loading.exporting = false
  }
}
function exportFiltered(format: ArchiveExportFormat) {
  void prepareDownload({ format, filter: archiveFilterParams(appliedFilters.value) })
}
function exportSession(id: ArchiveID) {
  void prepareDownload({ format: 'archive', session_id: id })
}
function confirmExport() {
  const request = pendingExport.value
  pendingExport.value = null
  if (request) void issueDownload(request.payload)
}
function requestDeleteSession(id: ArchiveID) {
  pendingDelete.value = { kind: 'session', id }
}
function requestDeleteFiltered() {
  pendingDelete.value = { kind: 'filter' }
}
async function confirmDelete() {
  const request = pendingDelete.value
  pendingDelete.value = null
  if (!request || loading.deleting) return
  loading.deleting = true
  try {
    const payload = request.kind === 'session'
      ? { session_ids: [request.id] }
      : { filter: archiveFilterParams(appliedFilters.value) }
    const job = await sessionArchiveAPI.createDeletionJob(payload)
    deletionJobs.value = [job, ...deletionJobs.value.filter((item) => String(item.id) !== String(job.id))]
    appStore.showSuccess(t('admin.sessionArchive.messages.deletionQueued'))
    scheduleDeletionPoll()
    await loadSessions()
  } catch (error) {
    showActionError(error, 'admin.sessionArchive.errors.delete')
  } finally {
    loading.deleting = false
  }
}
function scheduleDeletionPoll() {
  if (deletionPollTimer) window.clearTimeout(deletionPollTimer)
  const active = deletionJobs.value.some((job) => !['completed', 'failed', 'canceled'].includes(job.status))
  if (!active) return
  deletionPollTimer = window.setTimeout(async () => {
    await loadDeletionJobs()
    if (!deletionJobs.value.some((job) => !['completed', 'failed', 'canceled'].includes(job.status))) {
      await loadSessions()
    }
  }, 2500)
}
function refreshActiveTab() {
  if (activeTab.value === 'config') void loadConfig()
  else void Promise.allSettled([loadSessions(), loadDeletionJobs()])
}

watch(activeTab, (tab) => {
  void router.replace({ query: { ...route.query, tab: tab === 'config' ? 'config' : undefined } })
  if (tab === 'config' && !runtime.value) void loadConfig()
})
watch(() => route.query.correlation_request_id, (value) => {
  const correlation = queryString(value)
  if (correlation === filters.value.correlation_request_id) return
  filters.value = { ...filters.value, correlation_request_id: correlation }
  appliedFilters.value = cloneArchiveData(filters.value)
  sessions.page = 1
  void loadSessions()
})

onMounted(loadInitial)
onBeforeUnmount(() => {
  if (downloadClearTimer) window.clearTimeout(downloadClearTimer)
  if (deletionPollTimer) window.clearTimeout(deletionPollTimer)
  requestContent.value = {}
})
</script>
