import { describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'

import UsageStatsCards from '../UsageStatsCards.vue'

const messages: Record<string, string> = {
  'usage.totalRequests': 'Total Requests',
  'usage.inSelectedRange': 'in selected range',
  'usage.totalTokens': 'Total Tokens',
  'usage.in': 'In',
  'usage.out': 'Out',
  'usage.cacheTotal': 'Cache',
  'usage.cacheBreakdown': 'Cache Token Breakdown',
  'usage.cacheCreationTokensLabel': 'Cache Creation',
  'usage.cacheReadTokensLabel': 'Cache Read',
  'usage.totalCost': 'Total Cost',
  'usage.accountCost': 'Cost',
  'usage.standardCost': 'Standard',
  'usage.avgDuration': 'Avg Duration',
  'usage.avgFirstToken': 'Avg First Token',
  // CAPYBARA-PATCH: 用量统计卡解码速度语义与提示。
  'usage.avgOutputSpeed': 'Avg Decoding Speed',
  'usage.speedSamples': '{count} valid samples',
  'usage.outputSpeedHint':
    'Decoding speed = output tokens × 1000 / (duration_ms - first_token_ms); only requests with a recorded first-token time and duration_ms > first_token_ms are valid; excludes the first-token wait',
}

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string, params?: Record<string, unknown>) => {
        const template = messages[key] ?? key
        if (!params) return template
        return template.replace(/\{(\w+)\}/g, (_match, name: string) => String(params[name] ?? ''))
      },
    }),
  }
})

const stats = {
  total_requests: 1,
  total_input_tokens: 100,
  total_output_tokens: 50,
  total_cache_tokens: 34,
  total_cache_creation_tokens: 12,
  total_cache_read_tokens: 22,
  total_tokens: 184,
  total_cost: 0.001,
  total_actual_cost: 0.001,
  total_account_cost: 0.001,
  average_duration_ms: 250,
}

const mountCards = (statsProp: typeof stats & Record<string, unknown>) =>
  mount(UsageStatsCards, {
    props: { stats: statsProp },
    global: { stubs: { Icon: true } },
  })

describe('UsageStatsCards', () => {
  it('shows cache token breakdown values', () => {
    const wrapper = mountCards(stats)

    const text = wrapper.text()
    expect(text).toContain('Cache: 34')
    expect(text).toContain('Cache Token Breakdown')
    expect(text).toContain('Cache Creation')
    expect(text).toContain('12')
    expect(text).toContain('Cache Read')
    expect(text).toContain('22')
  })

  it('keeps the cache tooltip out of the layout while it is hidden', () => {
    const wrapper = mount(UsageStatsCards, {
      props: {
        stats,
      },
      global: {
        stubs: {
          Icon: true,
        },
      },
    })

    const tooltip = wrapper.findAll('span').find((el) => el.classes().includes('group-hover:block'))

    expect(tooltip).toBeDefined()
    // `opacity-0` hides the tooltip visually but keeps it in the layout, so its
    // fixed width still widens the document and causes horizontal scrolling on
    // narrow screens. `hidden` (display: none) takes it out of the flow.
    expect(tooltip?.classes()).toContain('hidden')
    expect(tooltip?.classes()).not.toContain('opacity-0')
  })
})

describe('UsageStatsCards performance card', () => {
  it('shows average first token, average decoding speed and their sample counts', () => {
    const wrapper = mountCards({
      ...stats,
      average_first_token_ms: 850,
      first_token_ms_samples: 12,
      average_output_tokens_per_second: 42.5,
      output_tokens_per_second_samples: 10,
    })

    const detail = wrapper.get('[data-testid="usage-perf-detail"]')
    const text = detail.text()
    expect(text).toContain('Avg First Token')
    expect(text).toContain('850ms')
    expect(text).toContain('12 valid samples')
    expect(text).toContain('Avg Decoding Speed')
    expect(text).toContain('42.50 tok/s')
    expect(text).toContain('10 valid samples')
    // 平均耗时主值不受影响
    expect(wrapper.text()).toContain('250ms')
  })

  it('exposes the decoding speed hint on the card', () => {
    const wrapper = mountCards({
      ...stats,
      average_output_tokens_per_second: 42.5,
      output_tokens_per_second_samples: 10,
    })

    expect(wrapper.get('[data-testid="usage-perf-detail"]').attributes('title')).toBe(
      messages['usage.outputSpeedHint']
    )
  })

  it('degrades to a dash when there is no valid sample', () => {
    const wrapper = mountCards({
      ...stats,
      average_first_token_ms: null,
      first_token_ms_samples: 0,
      // 后端不应返回 0 均值，这里断言 0 样本时前端不会把 0 冒充成真实解码速度
      average_output_tokens_per_second: 0,
      output_tokens_per_second_samples: 0,
    })

    const detail = wrapper.get('[data-testid="usage-perf-detail"]')
    const values = detail.findAll('span')
    expect(values.map((span) => span.text())).toEqual([
      'Avg First Token',
      '-',
      '0 valid samples',
      'Avg Decoding Speed',
      '-',
      '0 valid samples',
    ])
    expect(detail.text()).not.toContain('NaN')
    expect(detail.text()).not.toContain('Infinity')
    expect(detail.text()).not.toContain('0.00 tok/s')
  })

  it('degrades to a dash and hides sample counts when the fields are missing (old backend)', () => {
    const wrapper = mountCards(stats)

    const detail = wrapper.get('[data-testid="usage-perf-detail"]')
    expect(detail.findAll('span').map((span) => span.text())).toEqual([
      'Avg First Token',
      '-',
      'Avg Decoding Speed',
      '-',
    ])
    expect(detail.text()).not.toContain('NaN')
    expect(detail.text()).not.toContain('valid samples')
  })

  it('degrades to a dash for non-finite values', () => {
    const wrapper = mountCards({
      ...stats,
      average_first_token_ms: Number.NaN,
      first_token_ms_samples: 5,
      average_output_tokens_per_second: Number.POSITIVE_INFINITY,
      output_tokens_per_second_samples: 5,
    })

    const detail = wrapper.get('[data-testid="usage-perf-detail"]')
    expect(detail.findAll('span').map((span) => span.text())).toEqual([
      'Avg First Token',
      '-',
      '5 valid samples',
      'Avg Decoding Speed',
      '-',
      '5 valid samples',
    ])
  })

  it('renders null stats without crashing', () => {
    const wrapper = mount(UsageStatsCards, {
      props: { stats: null },
      global: { stubs: { Icon: true } },
    })

    const detail = wrapper.get('[data-testid="usage-perf-detail"]')
    expect(detail.text()).toContain('-')
    expect(detail.text()).not.toContain('NaN')
  })
})
