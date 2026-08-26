import { describe, expect, it } from 'vitest'

import en from '../locales/en'
import zh from '../locales/zh'

// CAPYBARA-PATCH: 用量解码速度相关文案的双语兜底测试
// 组件测试的 i18n mock 在缺键时会回显 key，导致假绿；这里直接断言 locale 模块本身
const REQUIRED_KEYS = [
  'latencyOutputSpeed',
  'avgFirstToken',
  'avgOutputSpeed',
  'speedSamples',
  'outputSpeedHint',
  'exportOutputSpeed',
] as const

const LOCALES = [
  ['zh', zh],
  ['en', en],
] as const

describe('usage token speed locale keys', () => {
  for (const [name, locale] of LOCALES) {
    it(`defines every decoding speed key in ${name}`, () => {
      const usage = (locale as { usage: Record<string, unknown> }).usage
      for (const key of REQUIRED_KEYS) {
        const value = usage[key]
        expect(typeof value, `${name}.usage.${key} should be a string`).toBe('string')
        expect((value as string).trim(), `${name}.usage.${key} should not be empty`).not.toBe('')
      }
    })

    it(`keeps the {count} placeholder in ${name} speedSamples`, () => {
      const usage = (locale as { usage: Record<string, unknown> }).usage
      expect(usage.speedSamples as string).toContain('{count}')
    })
  }

  it('names the metric as decoding speed and documents the TTFT-excluding formula', () => {
    const zhUsage = (zh as { usage: Record<string, string> }).usage
    const enUsage = (en as { usage: Record<string, string> }).usage

    expect(zhUsage.latencyOutputSpeed).toBe('解码速度')
    expect(zhUsage.avgOutputSpeed).toContain('解码速度')
    expect(zhUsage.exportOutputSpeed).toContain('解码速度')
    expect(enUsage.latencyOutputSpeed).toBe('Decoding Speed')
    expect(enUsage.avgOutputSpeed).toContain('Decoding Speed')
    expect(enUsage.exportOutputSpeed).toContain('Decoding Speed')

    expect(zhUsage.outputSpeedHint).toContain('duration_ms - first_token_ms')
    expect(zhUsage.outputSpeedHint).toContain('duration_ms > first_token_ms')
    expect(zhUsage.outputSpeedHint).toContain('已记录首 Token 耗时')
    expect(zhUsage.outputSpeedHint).toContain('不含首 Token 等待')
    expect(enUsage.outputSpeedHint).toContain('duration_ms - first_token_ms')
    expect(enUsage.outputSpeedHint).toContain('duration_ms > first_token_ms')
    expect(enUsage.outputSpeedHint).toContain('recorded first-token time')
    expect(enUsage.outputSpeedHint).toContain('excludes the first-token wait')
  })
})
