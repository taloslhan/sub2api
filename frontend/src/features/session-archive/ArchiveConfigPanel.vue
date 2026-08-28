<template>
  <section class="space-y-6 py-6" aria-labelledby="archive-config-title">
    <div>
      <h2 id="archive-config-title" class="text-base font-semibold text-gray-950 dark:text-white">{{ t('admin.sessionArchive.config.title') }}</h2>
      <p class="mt-1 text-sm text-gray-500 dark:text-dark-300">{{ t('admin.sessionArchive.config.description') }}</p>
    </div>

    <div class="grid gap-4 sm:grid-cols-2 xl:grid-cols-4">
      <article class="rounded-xl border border-gray-200 p-4 dark:border-dark-700">
        <p class="text-xs uppercase tracking-wide text-gray-500">{{ t('admin.sessionArchive.config.process') }}</p>
        <p class="mt-2 text-lg font-semibold text-gray-900 dark:text-white">{{ runtime ? statusLabel(runtime.process_status) : '—' }}</p>
        <p class="mt-1 text-xs text-gray-500">{{ runtime?.enabled ? t('admin.sessionArchive.config.collecting') : t('admin.sessionArchive.config.defaultOff') }}</p>
      </article>
      <article class="rounded-xl border border-gray-200 p-4 dark:border-dark-700">
        <p class="text-xs uppercase tracking-wide text-gray-500">{{ t('admin.sessionArchive.config.queue') }}</p>
        <p class="mt-2 text-lg font-semibold tabular-nums text-gray-900 dark:text-white">{{ runtime?.queue_events ?? 0 }} / {{ runtime?.queue_event_capacity ?? 0 }}</p>
        <p class="mt-1 text-xs text-gray-500">{{ formatBytes(runtime?.queue_bytes || 0) }} / {{ formatBytes(runtime?.queue_byte_capacity || 0) }}</p>
      </article>
      <article class="rounded-xl border border-gray-200 p-4 dark:border-dark-700">
        <p class="text-xs uppercase tracking-wide text-gray-500">{{ t('admin.sessionArchive.config.delivery') }}</p>
        <p class="mt-2 text-lg font-semibold tabular-nums text-gray-900 dark:text-white">{{ runtime?.stored_total ?? 0 }}</p>
        <p class="mt-1 text-xs text-gray-500">{{ t('admin.sessionArchive.config.deliveryDetail', { dropped: runtime?.dropped_total ?? 0, failed: runtime?.failed_total ?? 0, truncated: runtime?.truncated_total ?? 0 }) }}</p>
      </article>
      <article class="rounded-xl border border-gray-200 p-4 dark:border-dark-700">
        <p class="text-xs uppercase tracking-wide text-gray-500">{{ t('admin.sessionArchive.config.storage') }}</p>
        <p class="mt-2 text-lg font-semibold text-gray-900 dark:text-white">{{ runtime?.storage_status || '—' }}</p>
        <p class="mt-1 truncate text-xs text-gray-500" :title="runtime?.bucket">{{ runtime?.bucket || '—' }} / {{ runtime?.prefix || '—' }} · {{ runtime?.active_key_id || '—' }}</p>
      </article>
    </div>

    <div v-if="runtime?.last_error" role="alert" class="rounded-xl bg-red-50 px-4 py-3 text-sm text-red-700 dark:bg-red-950/30 dark:text-red-300">
      {{ runtime.last_error }}
    </div>

    <div class="rounded-xl border border-gray-200 p-5 dark:border-dark-700">
      <div class="flex flex-wrap items-start justify-between gap-3">
        <div>
          <h3 class="text-sm font-semibold text-gray-900 dark:text-white">{{ t('admin.sessionArchive.config.policies') }}</h3>
          <p class="mt-1 text-xs text-gray-500">{{ t('admin.sessionArchive.config.precedence') }}</p>
        </div>
        <button type="button" class="btn btn-secondary btn-sm" :disabled="loading" @click="resetDraft">{{ t('admin.sessionArchive.actions.newPolicy') }}</button>
      </div>

      <form class="mt-5 grid gap-4 lg:grid-cols-3" @submit.prevent="emit('save-policy', cloneArchiveData(draft))">
        <label class="text-xs text-gray-600 dark:text-dark-200">
          <span>{{ t('admin.sessionArchive.config.scope') }}</span>
          <select v-model="draft.scope_type" class="input mt-1 w-full" @change="scopeChanged">
            <option value="global">{{ t('admin.sessionArchive.scope.global') }}</option>
            <option value="group">{{ t('admin.sessionArchive.scope.group') }}</option>
            <option value="user">{{ t('admin.sessionArchive.scope.user') }}</option>
            <option value="api_key">{{ t('admin.sessionArchive.scope.api_key') }}</option>
          </select>
        </label>
        <label class="text-xs text-gray-600 dark:text-dark-200">
          <span>{{ t('admin.sessionArchive.config.scopeId') }}</span>
          <input v-model.number="draft.scope_id" type="number" min="1" class="input mt-1 w-full" :disabled="draft.scope_type === 'global'" required />
        </label>
        <label class="text-xs text-gray-600 dark:text-dark-200">
          <span>{{ t('admin.sessionArchive.config.policyState') }}</span>
          <select v-model="draft.state" class="input mt-1 w-full">
            <option value="inherit" :disabled="draft.scope_type === 'global'">{{ t('admin.sessionArchive.policyState.inherit') }}</option>
            <option value="on">{{ t('admin.sessionArchive.policyState.on') }}</option>
            <option value="off">{{ t('admin.sessionArchive.policyState.off') }}</option>
          </select>
        </label>

        <label class="text-xs text-gray-600 dark:text-dark-200">
          <span>{{ t('admin.sessionArchive.config.retentionDays') }}</span>
          <input v-model.number="draft.retention_days" type="number" min="1" max="3650" class="input mt-1 w-full" required />
        </label>
        <label class="text-xs text-gray-600 dark:text-dark-200">
          <span>{{ t('admin.sessionArchive.config.bodyLimit') }}</span>
          <input v-model.number="draft.body_limit_bytes" type="number" min="1024" :max="DEFAULT_BODY_LIMIT_BYTES" class="input mt-1 w-full" required />
        </label>
        <div class="flex items-end gap-2">
          <button type="submit" class="btn btn-primary" :disabled="loading">{{ t('common.save') }}</button>
          <button v-if="draft.id" type="button" class="btn btn-danger" :disabled="loading" @click="emit('delete-policy', cloneArchiveData(draft))">{{ t('common.delete') }}</button>
        </div>

        <div class="grid gap-2 text-sm sm:grid-cols-2 lg:col-span-3 lg:grid-cols-5">
          <label v-for="field in captureFields" :key="field" class="flex items-center gap-2 rounded-lg bg-gray-50 px-3 py-2 dark:bg-dark-900">
            <input v-model="draft[field]" type="checkbox" :disabled="draft.state === 'off'" />
            <span>{{ t(`admin.sessionArchive.config.${field}`) }}</span>
          </label>
        </div>
      </form>

      <div class="mt-6 overflow-x-auto">
        <table class="w-full min-w-[720px] text-left text-sm">
          <thead class="text-xs uppercase tracking-wide text-gray-500"><tr>
            <th class="px-3 py-2 font-medium">{{ t('admin.sessionArchive.config.scope') }}</th>
            <th class="px-3 py-2 font-medium">{{ t('admin.sessionArchive.config.policyState') }}</th>
            <th class="px-3 py-2 font-medium">{{ t('admin.sessionArchive.config.capture') }}</th>
            <th class="px-3 py-2 font-medium">{{ t('admin.sessionArchive.config.retentionDays') }}</th>
            <th class="px-3 py-2 text-right font-medium">{{ t('common.actions') }}</th>
          </tr></thead>
          <tbody class="divide-y divide-gray-100 dark:divide-dark-700">
            <tr v-for="policy in policies" :key="`${policy.scope_type}:${policy.scope_id || 0}`">
              <td class="px-3 py-3">{{ scopeLabel(policy) }}</td>
              <td class="px-3 py-3">{{ t(`admin.sessionArchive.policyState.${policy.state}`) }}</td>
              <td class="px-3 py-3 text-xs text-gray-500">{{ captureSummary(policy) }}</td>
              <td class="px-3 py-3 tabular-nums">{{ policy.retention_days }}</td>
              <td class="px-3 py-3 text-right"><button type="button" class="btn btn-ghost btn-sm" @click="editPolicy(policy)">{{ t('common.edit') }}</button></td>
            </tr>
            <tr v-if="!policies.length"><td colspan="5" class="px-3 py-8 text-center text-gray-500">{{ t('common.noData') }}</td></tr>
          </tbody>
        </table>
      </div>
    </div>

    <div class="rounded-xl border border-amber-200 bg-amber-50 p-4 text-sm text-amber-900 dark:border-amber-900/70 dark:bg-amber-950/30 dark:text-amber-200">
      {{ t('admin.sessionArchive.config.sensitiveBoundary') }}
    </div>
  </section>
</template>

<script setup lang="ts">
import { reactive, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import type { ArchivePolicy, ArchiveRuntimeStatus } from './types'
import { cloneArchiveData, createArchivePolicy, DEFAULT_BODY_LIMIT_BYTES } from './viewModel'

const props = defineProps<{ runtime: ArchiveRuntimeStatus | null; policies: ArchivePolicy[]; loading: boolean }>()
const emit = defineEmits<{
  (event: 'save-policy', policy: ArchivePolicy): void
  (event: 'delete-policy', policy: ArchivePolicy): void
}>()
const { t } = useI18n()
const draft = reactive<ArchivePolicy>(createArchivePolicy())
const captureFields = ['capture_request', 'capture_response', 'capture_transformed_request', 'capture_tools', 'capture_attachments'] as const

watch(() => props.policies, (policies) => {
  if (!draft.id && policies.length) {
    const global = policies.find((policy) => policy.scope_type === 'global')
    if (global) Object.assign(draft, cloneArchiveData(global))
  }
}, { immediate: true })

function resetDraft() {
  Object.assign(draft, createArchivePolicy('global'))
  delete draft.id
}
function editPolicy(policy: ArchivePolicy) {
  Object.assign(draft, cloneArchiveData(policy))
}
function scopeChanged() {
  draft.scope_id = draft.scope_type === 'global' ? 0 : undefined
  draft.state = draft.scope_type === 'global' ? 'off' : 'inherit'
  delete draft.id
}
function scopeLabel(policy: ArchivePolicy): string {
  const scope = t(`admin.sessionArchive.scope.${policy.scope_type}`)
  return policy.scope_type === 'global' ? scope : `${scope} · ${policy.scope_name || `#${policy.scope_id}`}`
}
function captureSummary(policy: ArchivePolicy): string {
  if (policy.state === 'off') return '—'
  return captureFields.filter((field) => policy[field]).map((field) => t(`admin.sessionArchive.config.${field}`)).join(' · ') || '—'
}
function statusLabel(status: string): string {
  const key = `admin.sessionArchive.status.${status}`
  const translated = t(key)
  return translated === key ? status : translated
}
function formatBytes(value: number): string {
  if (!Number.isFinite(value) || value <= 0) return '0 B'
  const units = ['B', 'KiB', 'MiB', 'GiB']
  const index = Math.min(units.length - 1, Math.floor(Math.log(value) / Math.log(1024)))
  return `${(value / 1024 ** index).toFixed(index === 0 ? 0 : 1)} ${units[index]}`
}
</script>
