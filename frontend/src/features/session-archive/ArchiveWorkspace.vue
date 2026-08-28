<template>
  <section class="py-6" aria-labelledby="archive-sessions-title">
    <div class="flex flex-wrap items-start justify-between gap-3">
      <div>
        <h2 id="archive-sessions-title" class="text-base font-semibold text-gray-950 dark:text-white">
          {{ t('admin.sessionArchive.workspace.title') }}
        </h2>
        <p class="mt-1 text-sm text-gray-500 dark:text-dark-300">
          {{ t('admin.sessionArchive.workspace.description') }}
        </p>
      </div>
      <div class="flex flex-wrap gap-2">
        <button type="button" class="btn btn-secondary btn-sm" :disabled="loading" @click="emit('export', 'archive')">
          {{ t('admin.sessionArchive.actions.exportArchive') }}
        </button>
        <button type="button" class="btn btn-secondary btn-sm" :disabled="loading" @click="emit('export', 'sft')">
          {{ t('admin.sessionArchive.actions.exportSft') }}
        </button>
        <button type="button" class="btn btn-danger btn-sm" :disabled="loading" @click="emit('delete-filter')">
          {{ t('admin.sessionArchive.actions.deleteFiltered') }}
        </button>
      </div>
    </div>

    <form class="mt-5 grid gap-3 sm:grid-cols-2 lg:grid-cols-4 xl:grid-cols-5" @submit.prevent="applyFilters">
      <FilterInput v-model="localFilters.correlation_request_id" :label="t('admin.sessionArchive.fields.correlationRequestId')" @change="filtersChanged" />
      <FilterInput v-model="localFilters.user_id" :label="t('admin.sessionArchive.fields.userId')" type="number" @change="filtersChanged" />
      <FilterInput v-model="localFilters.api_key_id" :label="t('admin.sessionArchive.fields.apiKeyId')" type="number" @change="filtersChanged" />
      <FilterInput v-model="localFilters.group_id" :label="t('admin.sessionArchive.fields.groupId')" type="number" @change="filtersChanged" />
      <FilterInput v-model="localFilters.model" :label="t('admin.sessionArchive.fields.model')" @change="filtersChanged" />
      <FilterInput v-model="localFilters.client" :label="t('admin.sessionArchive.fields.client')" @change="filtersChanged" />
      <label class="text-xs text-gray-600 dark:text-dark-200">
        <span>{{ t('admin.sessionArchive.fields.status') }}</span>
        <select v-model="localFilters.status" class="input mt-1 w-full" @change="filtersChanged">
          <option value="">{{ t('common.all') }}</option>
          <option v-for="status in statuses" :key="status" :value="status">
            {{ t(`admin.sessionArchive.status.${status}`) }}
          </option>
        </select>
      </label>
      <FilterInput v-model="localFilters.start_at" :label="t('admin.sessionArchive.fields.startAt')" type="datetime-local" @change="filtersChanged" />
      <FilterInput v-model="localFilters.end_at" :label="t('admin.sessionArchive.fields.endAt')" type="datetime-local" @change="filtersChanged" />
      <div class="flex items-end gap-2 sm:col-span-2">
        <button type="submit" class="btn btn-primary btn-sm">{{ t('common.search') }}</button>
        <button type="button" class="btn btn-ghost btn-sm" @click="resetFilters">{{ t('common.reset') }}</button>
      </div>
    </form>

    <div v-if="error" role="alert" class="mt-4 rounded-lg bg-red-50 px-4 py-3 text-sm text-red-700 dark:bg-red-950/30 dark:text-red-300">
      {{ error }}
    </div>

    <div class="mt-5 overflow-x-auto rounded-xl border border-gray-200 dark:border-dark-700/60">
      <table class="w-full min-w-[1080px] text-left text-sm">
        <thead class="bg-gray-50 text-xs uppercase tracking-wide text-gray-500 dark:bg-dark-900/70 dark:text-dark-400">
          <tr>
            <th class="px-4 py-3 font-medium">{{ t('admin.sessionArchive.fields.lastActivity') }}</th>
            <th class="px-4 py-3 font-medium">{{ t('admin.sessionArchive.fields.identity') }}</th>
            <th class="px-4 py-3 font-medium">{{ t('admin.sessionArchive.fields.route') }}</th>
            <th class="px-4 py-3 font-medium">{{ t('admin.sessionArchive.fields.turnsRequests') }}</th>
            <th class="px-4 py-3 font-medium">{{ t('admin.sessionArchive.fields.status') }}</th>
            <th class="px-4 py-3 font-medium">{{ t('admin.sessionArchive.fields.expiresAt') }}</th>
            <th class="px-4 py-3 text-right font-medium">{{ t('common.actions') }}</th>
          </tr>
        </thead>
        <tbody class="divide-y divide-gray-100 bg-white dark:divide-dark-700 dark:bg-transparent">
          <tr v-if="loading">
            <td colspan="7" class="px-4 py-12 text-center text-gray-500" aria-busy="true">{{ t('common.loading') }}</td>
          </tr>
          <tr v-else-if="sessions.length === 0">
            <td colspan="7" class="px-4 py-12 text-center text-gray-500">{{ t('admin.sessionArchive.workspace.empty') }}</td>
          </tr>
          <tr v-for="session in sessions" v-else :key="session.id" class="align-top hover:bg-gray-50/70 dark:hover:bg-dark-800/70">
            <td class="whitespace-nowrap px-4 py-3 text-xs text-gray-600 dark:text-dark-300">{{ formatDate(session.last_activity_at) }}</td>
            <td class="px-4 py-3">
              <p class="font-medium text-gray-900 dark:text-white">{{ session.user_email || session.username || formatID(session.user_id) }}</p>
              <p class="mt-1 text-xs text-gray-500">{{ session.api_key_name || formatID(session.api_key_id) }} · {{ session.group_name || formatID(session.group_id) }}</p>
            </td>
            <td class="px-4 py-3">
              <p class="font-medium text-gray-900 dark:text-white">{{ session.last_model || session.first_model || '—' }}</p>
              <p class="mt-1 text-xs text-gray-500">{{ session.protocol }} · {{ session.client || '—' }}</p>
              <p v-if="session.capture_coverage === 'control_plane_only'" class="mt-1 text-xs text-amber-600 dark:text-amber-300">
                {{ t('admin.sessionArchive.workspace.controlPlaneOnly') }}
              </p>
            </td>
            <td class="px-4 py-3 tabular-nums text-gray-700 dark:text-dark-200">
              {{ session.turn_count }} / {{ session.request_count }}
            </td>
            <td class="px-4 py-3">
              <span class="rounded-full px-2 py-0.5 text-xs font-medium" :class="statusClass(session.status)">
                {{ statusLabel(session.status) }}
              </span>
              <span v-if="session.has_truncated" class="ml-2 rounded-full bg-amber-100 px-2 py-0.5 text-xs text-amber-700 dark:bg-amber-950/50 dark:text-amber-300">
                {{ t('admin.sessionArchive.workspace.truncated') }}
              </span>
            </td>
            <td class="whitespace-nowrap px-4 py-3 text-xs text-gray-600 dark:text-dark-300">{{ formatDate(session.expires_at) }}</td>
            <td class="whitespace-nowrap px-4 py-3 text-right">
              <button type="button" class="btn btn-ghost btn-sm" @click="emit('view', session.id)">{{ t('common.view') }}</button>
              <button type="button" class="btn btn-ghost btn-sm" @click="emit('export-session', session.id)">{{ t('admin.sessionArchive.actions.exportArchive') }}</button>
              <button type="button" class="btn btn-ghost btn-sm text-red-600" @click="emit('delete-session', session.id)">{{ t('common.delete') }}</button>
            </td>
          </tr>
        </tbody>
      </table>
      <Pagination :total="total" :page="page" :page-size="pageSize" @update:page="emit('page', $event)" @update:page-size="emit('page-size', $event)" />
    </div>

    <section v-if="deletionJobs.length" class="mt-6" aria-labelledby="archive-deletion-jobs-title">
      <h3 id="archive-deletion-jobs-title" class="text-sm font-semibold text-gray-900 dark:text-white">{{ t('admin.sessionArchive.deletion.title') }}</h3>
      <div class="mt-3 space-y-3">
        <article v-for="job in deletionJobs" :key="job.id" class="rounded-xl border border-gray-200 p-4 dark:border-dark-700">
          <div class="flex flex-wrap items-center justify-between gap-2 text-sm">
            <span class="font-mono text-xs text-gray-500">#{{ job.id }}</span>
            <span class="font-medium text-gray-800 dark:text-dark-100">{{ statusLabel(job.status) }}</span>
          </div>
          <div class="mt-3 h-2 overflow-hidden rounded-full bg-gray-100 dark:bg-dark-700" role="progressbar" :aria-valuenow="jobProgress(job)" aria-valuemin="0" aria-valuemax="100">
            <div class="h-full rounded-full bg-primary-500 transition-[width]" :style="{ width: `${jobProgress(job)}%` }"></div>
          </div>
          <p class="mt-2 text-xs text-gray-500 dark:text-dark-400">
            {{ t('admin.sessionArchive.deletion.progress', { processed: job.processed_sessions, total: job.matched_sessions, failed: job.failed_sessions }) }}
          </p>
          <p v-if="job.last_error" class="mt-1 break-words text-xs text-red-600 dark:text-red-300">{{ job.last_error }}</p>
        </article>
      </div>
    </section>
  </section>
</template>

<script setup lang="ts">
import { defineComponent, h, reactive, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import Pagination from '@/components/common/Pagination.vue'
import type { ArchiveDeletionJob, ArchiveExportFormat, ArchiveFilters, ArchiveID, ArchiveSessionSummary } from './types'
import { cloneArchiveData, deletionProgress, emptyArchiveFilters } from './viewModel'

const props = defineProps<{
  sessions: ArchiveSessionSummary[]
  total: number
  page: number
  pageSize: number
  filters: ArchiveFilters
  loading: boolean
  error: string
  deletionJobs: ArchiveDeletionJob[]
}>()
const emit = defineEmits<{
  (event: 'filters-change', filters: ArchiveFilters): void
  (event: 'search', filters: ArchiveFilters): void
  (event: 'page', page: number): void
  (event: 'page-size', pageSize: number): void
  (event: 'view', id: ArchiveID): void
  (event: 'export', format: ArchiveExportFormat): void
  (event: 'export-session', id: ArchiveID): void
  (event: 'delete-session', id: ArchiveID): void
  (event: 'delete-filter'): void
}>()

const { t, locale } = useI18n()
const localFilters = reactive<ArchiveFilters>(cloneArchiveData(props.filters))
const statuses = ['active', 'completed', 'failed', 'deleting'] as const
watch(() => props.filters, (value) => Object.assign(localFilters, cloneArchiveData(value)), { deep: true })

const FilterInput = defineComponent({
  props: {
    modelValue: { type: String, required: true },
    label: { type: String, required: true },
    type: { type: String, default: 'text' },
  },
  emits: ['update:modelValue', 'change'],
  setup(componentProps, { emit: componentEmit }) {
    return () => h('label', { class: 'text-xs text-gray-600 dark:text-dark-200' }, [
      h('span', componentProps.label),
      h('input', {
        value: componentProps.modelValue,
        type: componentProps.type,
        class: 'input mt-1 w-full',
        'aria-label': componentProps.label,
        onInput: (event: Event) => componentEmit('update:modelValue', (event.target as HTMLInputElement).value),
        onChange: () => componentEmit('change'),
      }),
    ])
  },
})

function filtersChanged() {
  emit('filters-change', cloneArchiveData(localFilters))
}
function applyFilters() {
  const value = cloneArchiveData(localFilters)
  emit('filters-change', value)
  emit('search', value)
}
function resetFilters() {
  Object.assign(localFilters, emptyArchiveFilters())
  applyFilters()
}
function formatDate(value?: string | null): string {
  if (!value) return '—'
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) return value
  return new Intl.DateTimeFormat(locale.value, { dateStyle: 'short', timeStyle: 'medium' }).format(date)
}
function formatID(value?: number | null): string {
  return value ? `#${value}` : '—'
}
function statusLabel(status: string): string {
  const key = `admin.sessionArchive.status.${status}`
  const translated = t(key)
  return translated === key ? status : translated
}
function statusClass(status: string): string {
  if (status === 'failed') return 'bg-red-100 text-red-700 dark:bg-red-950/50 dark:text-red-300'
  if (status === 'deleting') return 'bg-amber-100 text-amber-700 dark:bg-amber-950/50 dark:text-amber-300'
  if (status === 'completed') return 'bg-emerald-100 text-emerald-700 dark:bg-emerald-950/50 dark:text-emerald-300'
  return 'bg-blue-100 text-blue-700 dark:bg-blue-950/50 dark:text-blue-300'
}
function jobProgress(job: ArchiveDeletionJob): number {
  return deletionProgress(job.processed_sessions, job.matched_sessions)
}
</script>
