<template>
  <div v-if="false" aria-hidden="true"></div>
</template>

<script setup lang="ts">
import { computed, watch } from 'vue'
import { useRoute } from 'vue-router'
import { useAppStore, useAuthStore } from '@/stores'

type CrispCommand = [string, string, unknown?]
type CrispQueue = CrispCommand[] & {
  push: (...items: CrispCommand[]) => number
}

declare global {
  interface Window {
    $crisp?: CrispQueue
    CRISP_WEBSITE_ID?: string
  }
}

const CRISP_SCRIPT_SRC = 'https://client.crisp.chat/l.js'

let scriptInjected = false
let activeWebsiteID = ''
let desiredVisibility: 'show' | 'hide' = 'show'

const route = useRoute()
const appStore = useAppStore()
const authStore = useAuthStore()

desiredVisibility = route.path.startsWith('/admin') ? 'hide' : 'show'

const crispSettings = computed(() => {
  const settings = appStore.cachedPublicSettings
  return {
    enabled: settings?.crisp_enabled === true,
    websiteID: (settings?.crisp_website_id || '').trim(),
  }
})

function ensureQueue(): CrispQueue {
  if (!window.$crisp) {
    window.$crisp = [] as unknown as CrispQueue
  }
  return window.$crisp
}

function pushCrisp(command: CrispCommand): void {
  ensureQueue().push(command)
}

function applyVisibility(): void {
  pushCrisp(['do', `chat:${desiredVisibility}`])
}

function syncUser(): void {
  const user = authStore.user
  if (!user) {
    return
  }

  if (user.email) {
    pushCrisp(['set', 'user:email', [user.email]])
  }
  if (user.username) {
    pushCrisp(['set', 'user:nickname', [user.username]])
  }
}

function injectCrisp(websiteID: string): void {
  if (!websiteID || scriptInjected) {
    return
  }

  scriptInjected = true
  activeWebsiteID = websiteID
  window.CRISP_WEBSITE_ID = websiteID
  ensureQueue()
  applyVisibility()
  syncUser()

  const script = document.createElement('script')
  script.src = CRISP_SCRIPT_SRC
  script.async = true
  document.head.appendChild(script)
}

watch(
  () => crispSettings.value,
  ({ enabled, websiteID }) => {
    if (enabled && websiteID) {
      if (!scriptInjected) {
        injectCrisp(websiteID)
        return
      }
      if (activeWebsiteID === websiteID) {
        applyVisibility()
      }
      return
    }

    if (scriptInjected) {
      pushCrisp(['do', 'chat:hide'])
    }
  },
  { immediate: true },
)

watch(
  () => route.path,
  (path) => {
    desiredVisibility = path.startsWith('/admin') ? 'hide' : 'show'
    if (scriptInjected) {
      applyVisibility()
    }
  },
  { immediate: true },
)

watch(
  () => authStore.user,
  (user, previousUser) => {
    if (user) {
      syncUser()
      return
    }
    if (previousUser && scriptInjected) {
      pushCrisp(['do', 'session:reset'])
    }
  },
  { immediate: true },
)
</script>
