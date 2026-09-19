import { afterEach, describe, expect, it, vi } from 'vitest'

vi.mock('vue-i18n', async () => {
  const { default: translations } = await import('@/i18n/locales/en/dashboard')
  return {
    useI18n: () => ({
      t: (key: string, params: Record<string, string> = {}) => {
        const value = key.split('.').reduce<unknown>((node, part) => (node as Record<string, unknown>)?.[part], translations)
        return String(value ?? key).replace(/\{(\w+)\}/g, (_, name) => params[name] ?? '')
      },
    }),
  }
})
import { mount, type VueWrapper } from '@vue/test-utils'
import UsageTurnStateCell from '../UsageTurnStateCell.vue'
import type { AdminUsageLog } from '@/types'

const wrappers: VueWrapper[] = []
function render(row: Partial<AdminUsageLog>) {
  const wrapper = mount(UsageTurnStateCell, {
    props: { row: row as AdminUsageLog },
    attachTo: document.body,
  })
  wrappers.push(wrapper)
  return wrapper
}
afterEach(() => {
  wrappers.splice(0).forEach(wrapper => wrapper.unmount())
  document.body.innerHTML = ''
})

describe('UsageTurnStateCell', () => {
  it('distinguishes historical rows from collected responses with no state', () => {
    expect(render({}).text()).toContain('Not collected')
    const row = render({ turn_state_transport: 'http' })
    expect(row.text()).toContain('Request')
    expect(row.text()).toContain('Response')
    expect(row.text()).not.toContain('Not collected')
    expect(row.findAll('button')).toHaveLength(0)
    expect(row.text().match(/—/g)).toHaveLength(2)
  })

  it('shows the correct full value for each side without interpreting HTML', async () => {
    const request = 'a'.repeat(292)
    const response = '<img src=x onerror=alert(1)>' + 'b'.repeat(286)
    const wrapper = render({
      turn_state_transport: 'http',
      upstream_request_turn_state: request,
      upstream_response_turn_state: response,
      upstream_request_turn_state_length: 292,
      upstream_response_turn_state_length: 312,
    })
    expect(wrapper.text()).toContain('292')
    expect(wrapper.text()).toContain('312')
    await wrapper.get('button[aria-label="Show Request state value"]').trigger('click')
    let visible = Array.from(document.querySelectorAll('[role=tooltip]')).filter(el => (el as HTMLElement).style.display !== 'none')
    expect(visible).toHaveLength(1)
    expect(visible[0].textContent).toContain(request)
    await wrapper.get('button[aria-label="Show Response state value"]').trigger('click')
    visible = Array.from(document.querySelectorAll('[role=tooltip]')).filter(el => (el as HTMLElement).style.display !== 'none')
    expect(visible.some(el => el.textContent?.includes(response))).toBe(true)
    expect(document.querySelector('img')).toBeNull()
    document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape' }))
    await wrapper.vm.$nextTick()
    expect(Array.from(document.querySelectorAll('[role=tooltip]')).every(el => (el as HTMLElement).style.display === 'none')).toBe(true)
  })

  it('labels both WS handshake origins and connection reuse', async () => {
    const wrapper = render({
      turn_state_transport: 'ws',
      turn_state_connection_reused: true,
      upstream_response_turn_state: 'state',
      upstream_response_turn_state_length: 5,
    })
    expect(wrapper.text()).toContain('Handshake request')
    expect(wrapper.text()).toContain('Handshake response')
    expect(wrapper.text()).toContain('Reused')
    await wrapper.get('button[aria-label="Show Handshake response state value"]').trigger('click')
    expect(document.body.textContent).toContain('not a new state issued for this turn')
  })
})
