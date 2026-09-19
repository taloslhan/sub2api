<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import HelpTooltip from '@/components/common/HelpTooltip.vue'
import Icon from '@/components/icons/Icon.vue'
import type { AdminUsageLog } from '@/types'

const props = defineProps<{ row: AdminUsageLog }>()
const { t } = useI18n()
const entries = computed(() => [
  {
    side: 'request',
    label: t(props.row.turn_state_transport === 'ws' ? 'usage.stateHandshakeRequest' : 'usage.stateRequest'),
    value: props.row.upstream_request_turn_state,
    length: props.row.upstream_request_turn_state_length,
  },
  {
    side: 'response',
    label: t(props.row.turn_state_transport === 'ws' ? 'usage.stateHandshakeResponse' : 'usage.stateResponse'),
    value: props.row.upstream_response_turn_state,
    length: props.row.upstream_response_turn_state_length,
  },
])
</script>

<template>
  <span v-if="!row.turn_state_transport" class="text-xs text-gray-400">{{ t('usage.stateNotCollected') }}</span>
  <div v-else class="flex flex-col gap-1 text-xs">
    <div v-for="entry in entries" :key="entry.side" class="flex items-center gap-1.5">
      <span
        class="whitespace-nowrap rounded px-1.5 py-0.5"
        :class="entry.side === 'request'
          ? 'bg-blue-50 text-blue-700 dark:bg-blue-900/30 dark:text-blue-300'
          : 'bg-violet-50 text-violet-700 dark:bg-violet-900/30 dark:text-violet-300'"
      >{{ entry.label }}</span>
      <span class="font-mono tabular-nums text-gray-700 dark:text-gray-200">{{ entry.length ?? '—' }}</span>
      <HelpTooltip v-if="entry.value != null" trigger="click" width-class="w-80 max-w-[calc(100vw-2rem)]">
        <template #trigger>
          <button
            type="button"
            :aria-label="t('usage.stateShowValue', { source: entry.label })"
            class="inline-flex h-5 w-5 items-center justify-center rounded-full text-gray-400 hover:text-primary-600 focus-visible:outline focus-visible:outline-2 focus-visible:outline-primary-500 dark:hover:text-primary-400"
          ><Icon name="infoCircle" size="xs" /></button>
        </template>
        <div class="pr-4 font-medium">{{ entry.label }} · {{ entry.length }} {{ t('usage.stateBytes') }}</div>
        <div class="mt-1 font-mono text-gray-300">x-codex-turn-state</div>
        <div v-if="row.turn_state_transport === 'ws'" class="mt-1 text-gray-300">{{ t('usage.stateHandshakeNote') }}</div>
        <pre class="mt-2 max-h-60 select-text overflow-y-auto whitespace-pre-wrap break-all font-mono text-xs">{{ entry.value }}</pre>
      </HelpTooltip>
    </div>
    <span v-if="row.turn_state_transport === 'ws' && row.turn_state_connection_reused" class="w-fit rounded bg-gray-100 px-1.5 py-0.5 text-gray-500 dark:bg-gray-700 dark:text-gray-300">{{ t('usage.stateReused') }}</span>
  </div>
</template>
