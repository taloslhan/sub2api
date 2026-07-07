import { mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { createMemoryHistory, createRouter } from 'vue-router'
import { afterEach, describe, expect, it } from 'vitest'
import CrispWidget from '../CrispWidget.vue'
import { useAppStore, useAuthStore } from '@/stores'

describe('CrispWidget', () => {
  afterEach(() => {
    delete window.$crisp
    delete window.CRISP_WEBSITE_ID
    document
      .querySelectorAll('script[src="https://client.crisp.chat/l.js"]')
      .forEach((script) => script.remove())
  })

  it('注入 Crisp 并按路由和登录状态同步会话', async () => {
    setActivePinia(createPinia())
    const router = createRouter({
      history: createMemoryHistory(),
      routes: [
        { path: '/', component: { template: '<div />' } },
        { path: '/admin', component: { template: '<div />' } },
      ],
    })
    await router.push('/admin')
    await router.isReady()

    const appStore = useAppStore()
    const authStore = useAuthStore()
    appStore.cachedPublicSettings = {
      crisp_enabled: true,
      crisp_website_id: '08e6570c-0bb2-4798-9c7c-145fbf4105cb',
    } as never
    authStore.user = {
      email: 'user@example.com',
      username: 'crisp-user',
    } as never

    const wrapper = mount(CrispWidget, {
      global: {
        plugins: [router],
      },
    })
    await wrapper.vm.$nextTick()

    expect(window.CRISP_WEBSITE_ID).toBe('08e6570c-0bb2-4798-9c7c-145fbf4105cb')
    expect(document.querySelector('script[src="https://client.crisp.chat/l.js"]')).not.toBeNull()
    expect(window.$crisp).toContainEqual(['do', 'chat:hide'])
    expect(window.$crisp).toContainEqual(['set', 'user:email', ['user@example.com']])
    expect(window.$crisp).toContainEqual(['set', 'user:nickname', ['crisp-user']])

    await router.push('/')
    await wrapper.vm.$nextTick()
    expect(window.$crisp).toContainEqual(['do', 'chat:show'])

    authStore.user = null
    await wrapper.vm.$nextTick()
    expect(window.$crisp).toContainEqual(['do', 'session:reset'])

    wrapper.unmount()
  })
})
