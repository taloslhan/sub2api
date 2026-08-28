<template>
  <BaseDialog :show="show" :title="t('admin.sessionArchive.detail.title')" width="full" @close="emit('close')">
    <div v-if="loading" class="py-16 text-center text-sm text-gray-500" aria-busy="true">{{ t('common.loading') }}</div>
    <div v-else-if="session" class="grid min-h-[min(68vh,44rem)] gap-5 lg:grid-cols-[minmax(18rem,0.85fr)_minmax(0,1.65fr)]">
      <section class="min-h-0 overflow-y-auto border-b border-gray-200 pb-5 dark:border-dark-700 lg:border-b-0 lg:border-r lg:pb-0 lg:pr-5" aria-labelledby="archive-timeline-title">
        <div class="flex flex-wrap items-start justify-between gap-2">
          <div>
            <h3 id="archive-timeline-title" class="text-sm font-semibold text-gray-950 dark:text-white">{{ t('admin.sessionArchive.detail.timeline') }}</h3>
            <p class="mt-1 text-xs text-gray-500">{{ session.protocol }} · {{ session.last_model || session.first_model || '—' }}</p>
          </div>
          <span v-if="session.capture_coverage === 'control_plane_only'" class="rounded-full bg-amber-100 px-2 py-1 text-xs text-amber-700 dark:bg-amber-950/50 dark:text-amber-300">
            {{ t('admin.sessionArchive.workspace.controlPlaneOnly') }}
          </span>
        </div>

        <ol class="mt-5 space-y-5">
          <li v-for="turn in session.turns" :key="turn.id" class="relative pl-6">
            <span class="absolute left-[0.2rem] top-1 h-3 w-3 rounded-full border-2 border-primary-500 bg-white dark:bg-dark-900"></span>
            <span class="absolute bottom-[-1.5rem] left-[0.55rem] top-4 w-px bg-gray-200 last:hidden dark:bg-dark-700"></span>
            <div class="flex items-center justify-between gap-2">
              <h4 class="text-sm font-medium text-gray-900 dark:text-white">{{ t('admin.sessionArchive.detail.turn', { sequence: turn.sequence }) }}</h4>
              <span class="text-xs text-gray-500">{{ formatDate(turn.started_at) }}</span>
            </div>
            <div class="mt-2 space-y-2">
              <button
                v-for="request in turn.requests"
                :key="request.id"
                type="button"
                class="w-full rounded-lg border px-3 py-2 text-left transition-colors"
                :class="selectedRequest?.id === request.id
                  ? 'border-primary-300 bg-primary-50 dark:border-primary-800 dark:bg-primary-950/30'
                  : 'border-gray-200 hover:border-primary-200 hover:bg-gray-50 dark:border-dark-700 dark:hover:border-primary-900 dark:hover:bg-dark-800'"
                @click="selectRequest(request)"
              >
                <span class="flex items-center justify-between gap-2">
                  <span class="truncate text-xs font-medium text-gray-800 dark:text-dark-100">{{ request.model || request.endpoint || `#${request.id}` }}</span>
                  <span class="text-[11px] text-gray-500">{{ statusLabel(request.status) }}</span>
                </span>
                <span class="mt-1 block truncate font-mono text-[11px] text-gray-500" :title="request.correlation_request_id">
                  {{ request.correlation_request_id || '—' }}
                </span>
              </button>
            </div>
          </li>
        </ol>
      </section>

      <section v-if="selectedRequest" class="min-w-0" aria-labelledby="archive-request-title">
        <div class="flex flex-wrap items-start justify-between gap-3">
          <div class="min-w-0">
            <h3 id="archive-request-title" class="text-sm font-semibold text-gray-950 dark:text-white">
              {{ t('admin.sessionArchive.detail.request', { sequence: selectedRequest.sequence }) }}
            </h3>
            <p class="mt-1 break-all font-mono text-xs text-gray-500">{{ selectedRequest.correlation_request_id || '—' }}</p>
          </div>
          <div v-if="selectedRequest.correlation_request_id" class="flex flex-wrap gap-2">
            <RouterLink class="btn btn-ghost btn-sm" :to="correlationLink('/admin/usage')">{{ t('admin.sessionArchive.links.usage') }}</RouterLink>
            <RouterLink class="btn btn-ghost btn-sm" :to="correlationLink('/admin/prompt-audit')">{{ t('admin.sessionArchive.links.promptAudit') }}</RouterLink>
            <RouterLink class="btn btn-ghost btn-sm" :to="correlationLink('/admin/ops', { open_error_details: '1', error_type: 'request' })">{{ t('admin.sessionArchive.links.ops') }}</RouterLink>
          </div>
        </div>

        <dl class="mt-4 grid grid-cols-[auto_minmax(0,1fr)] gap-x-4 gap-y-2 rounded-xl bg-gray-50 p-4 text-sm dark:bg-dark-900">
          <dt class="text-gray-500">{{ t('admin.sessionArchive.fields.endpoint') }}</dt><dd class="break-all">{{ selectedRequest.endpoint || '—' }}</dd>
          <dt class="text-gray-500">{{ t('admin.sessionArchive.fields.model') }}</dt><dd>{{ selectedRequest.model || '—' }}</dd>
          <dt class="text-gray-500">{{ t('admin.sessionArchive.fields.status') }}</dt><dd>{{ statusLabel(selectedRequest.status) }}</dd>
          <dt class="text-gray-500">{{ t('admin.sessionArchive.fields.billingRequestId') }}</dt><dd class="break-all font-mono text-xs">{{ selectedRequest.billing_request_id || '—' }}</dd>
          <dt class="text-gray-500">{{ t('admin.sessionArchive.fields.upstreamRequestId') }}</dt><dd class="break-all font-mono text-xs">{{ selectedRequest.upstream_request_id || '—' }}</dd>
          <dt class="text-gray-500">{{ t('admin.sessionArchive.fields.createdAt') }}</dt><dd>{{ formatDate(selectedRequest.created_at) }}</dd>
        </dl>

        <div class="mt-5 flex flex-wrap gap-2 border-b border-gray-200 pb-3 dark:border-dark-700" role="tablist">
          <button
            v-for="tab in contentTabs"
            :key="tab"
            type="button"
            role="tab"
            class="rounded-md px-3 py-1.5 text-sm"
            :class="activeContentTab === tab ? 'bg-primary-50 text-primary-700 dark:bg-primary-950/40 dark:text-primary-300' : 'text-gray-600 dark:text-dark-300'"
            :aria-selected="activeContentTab === tab"
            @click="activeContentTab = tab"
          >
            {{ t(`admin.sessionArchive.detail.tabs.${tab}`) }}
          </button>
        </div>

        <div class="mt-4 min-h-[18rem]">
          <div v-if="activeContentTab === 'attempts'" class="space-y-3">
            <article v-for="attempt in selectedRequest.attempts" :key="attempt.id" class="rounded-xl border border-gray-200 p-4 dark:border-dark-700">
              <div class="flex flex-wrap items-center justify-between gap-2">
                <h4 class="text-sm font-medium text-gray-900 dark:text-white">{{ t('admin.sessionArchive.detail.attempt', { sequence: attempt.sequence }) }}</h4>
                <span v-if="attempt.final" class="rounded-full bg-primary-100 px-2 py-0.5 text-xs text-primary-700 dark:bg-primary-950/50 dark:text-primary-300">{{ t('admin.sessionArchive.detail.finalAttempt') }}</span>
              </div>
              <dl class="mt-3 grid gap-2 text-xs sm:grid-cols-2">
                <div><dt class="text-gray-500">{{ t('admin.sessionArchive.fields.account') }}</dt><dd>{{ attempt.account_name || (attempt.account_id ? `#${attempt.account_id}` : '—') }}</dd></div>
                <div><dt class="text-gray-500">{{ t('admin.sessionArchive.fields.upstreamStatus') }}</dt><dd>{{ attempt.upstream_status || attempt.upstream_status_code || '—' }}</dd></div>
                <div><dt class="text-gray-500">{{ t('admin.sessionArchive.fields.transform') }}</dt><dd>{{ attempt.transform_type || '—' }}</dd></div>
                <div><dt class="text-gray-500">{{ t('admin.sessionArchive.fields.latency') }}</dt><dd>{{ attempt.latency_ms == null ? '—' : `${attempt.latency_ms} ms` }}</dd></div>
              </dl>
            </article>
            <p v-if="selectedRequest.attempts.length === 0" class="py-8 text-center text-sm text-gray-500">{{ t('common.noData') }}</p>
          </div>

          <div v-else>
            <div class="flex flex-wrap items-center justify-between gap-3">
              <p class="text-xs text-gray-500 dark:text-dark-400">{{ t('admin.sessionArchive.detail.sensitiveHint') }}</p>
              <div class="flex gap-2">
                <button type="button" class="btn btn-secondary btn-sm" :disabled="isContentLoading" @click="emit('load-content', selectedRequest.id, activeContentTab)">
                  {{ loadedContent ? t('common.refresh') : t('admin.sessionArchive.actions.loadContent') }}
                </button>
                <button v-if="hasAvailableContent" type="button" class="btn btn-ghost btn-sm" @click="copyLoadedContent">
                  {{ t('common.copy') }}
                </button>
              </div>
            </div>
            <div v-if="isContentLoading" class="py-16 text-center text-sm text-gray-500" aria-busy="true">{{ t('common.loading') }}</div>
            <div v-else-if="loadedContent" class="mt-3">
              <div v-if="loadedContent.truncated || loadedContent.dropped_reason" class="mb-3 rounded-lg bg-amber-50 px-3 py-2 text-xs text-amber-800 dark:bg-amber-950/30 dark:text-amber-200">
                {{ t('admin.sessionArchive.detail.contentIncomplete', { observed: formatBytes(loadedContent.observed_bytes), stored: formatBytes(loadedContent.stored_bytes), reason: loadedContent.dropped_reason || 'limit' }) }}
              </div>
              <div v-if="loadedContent.parts?.length" class="space-y-3">
                <article v-for="part in loadedContent.parts" :key="String(part.ref_id)" class="overflow-hidden rounded-xl border border-gray-200 dark:border-dark-700">
                  <div class="flex flex-wrap items-center justify-between gap-2 bg-gray-50 px-3 py-2 text-xs dark:bg-dark-900">
                    <span class="font-mono text-gray-700 dark:text-dark-200">#{{ part.sequence_no }} · ref {{ part.ref_id }}</span>
                    <span class="text-gray-500">{{ part.content_type || '—' }} · {{ part.direction || '—' }}</span>
                  </div>
                  <div class="border-t border-gray-200 px-3 py-2 text-[11px] text-gray-500 dark:border-dark-700">
                    {{ formatBytes(part.observed_bytes) }} / {{ formatBytes(part.stored_bytes) }}
                    <span v-if="part.truncated || part.dropped_reason" class="ml-2 text-amber-700 dark:text-amber-300">{{ part.dropped_reason || 'limit' }}</span>
                  </div>
                  <pre class="max-h-[min(40vh,26rem)] overflow-auto whitespace-pre-wrap break-words bg-gray-950 p-4 font-mono text-xs leading-5 text-gray-100">{{ part.available ? contentPartText(part) || '—' : '—' }}</pre>
                </article>
              </div>
              <pre v-else class="max-h-[min(48vh,30rem)] overflow-auto whitespace-pre-wrap break-words rounded-xl bg-gray-950 p-4 font-mono text-xs leading-5 text-gray-100">{{ displayContent }}</pre>
            </div>
            <div v-else class="mt-3 rounded-xl border border-dashed border-gray-200 px-4 py-12 text-center text-sm text-gray-500 dark:border-dark-700">
              {{ t('admin.sessionArchive.detail.contentNotLoaded') }}
            </div>
          </div>
        </div>
      </section>
      <div v-else class="flex items-center justify-center text-sm text-gray-500">{{ t('admin.sessionArchive.detail.noRequests') }}</div>
    </div>
  </BaseDialog>
</template>

<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import BaseDialog from '@/components/common/BaseDialog.vue'
import type { ArchiveContent, ArchiveContentKind, ArchiveID, ArchiveRequest, ArchiveSessionDetail } from './types'
import { contentPartText, contentText } from './viewModel'

const props = defineProps<{
  show: boolean
  session: ArchiveSessionDetail | null
  loading: boolean
  content: Record<string, ArchiveContent>
  contentLoadingKey: string
}>()
const emit = defineEmits<{
  (event: 'close'): void
  (event: 'load-content', requestID: ArchiveID, kind: ArchiveContentKind): void
  (event: 'copy-content', content: ArchiveContent): void
}>()
const { t, locale } = useI18n()
const selectedRequest = ref<ArchiveRequest | null>(null)
const contentTabs = ['attempts', 'request', 'upstream', 'response', 'tool', 'attachment', 'raw'] as const
type DetailTab = (typeof contentTabs)[number]
const activeContentTab = ref<DetailTab>('attempts')

function contentKey(requestID: ArchiveID, kind: ArchiveContentKind): string {
  return `${String(requestID)}:${kind}`
}
const loadedContent = computed(() => {
  if (!selectedRequest.value || activeContentTab.value === 'attempts') return null
  return props.content[contentKey(selectedRequest.value.id, activeContentTab.value)] || null
})
const displayContent = computed(() => contentText(loadedContent.value) || '—')
const hasAvailableContent = computed(() => loadedContent.value?.parts?.some((part) => part.available) ?? loadedContent.value?.available ?? false)
const isContentLoading = computed(() => {
  if (!selectedRequest.value || activeContentTab.value === 'attempts') return false
  return props.contentLoadingKey === contentKey(selectedRequest.value.id, activeContentTab.value)
})

watch(
  () => [props.show, props.session?.id] as const,
  ([show]) => {
    activeContentTab.value = 'attempts'
    selectedRequest.value = show ? props.session?.turns.flatMap((turn) => turn.requests)[0] || null : null
  },
  { immediate: true },
)

function selectRequest(request: ArchiveRequest) {
  selectedRequest.value = request
  activeContentTab.value = 'attempts'
}
function copyLoadedContent() {
  if (loadedContent.value) emit('copy-content', loadedContent.value)
}
function correlationLink(path: string, extra: Record<string, string> = {}) {
  return { path, query: { ...extra, correlation_request_id: selectedRequest.value?.correlation_request_id || '' } }
}
function formatDate(value?: string | null): string {
  if (!value) return '—'
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) return value
  return new Intl.DateTimeFormat(locale.value, { dateStyle: 'short', timeStyle: 'medium' }).format(date)
}
function formatBytes(value: number): string {
  if (!Number.isFinite(value) || value <= 0) return '0 B'
  const units = ['B', 'KiB', 'MiB', 'GiB']
  const index = Math.min(units.length - 1, Math.floor(Math.log(value) / Math.log(1024)))
  return `${(value / 1024 ** index).toFixed(index === 0 ? 0 : 1)} ${units[index]}`
}
function statusLabel(status: string): string {
  const key = `admin.sessionArchive.status.${status}`
  const translated = t(key)
  return translated === key ? status : translated
}
</script>
