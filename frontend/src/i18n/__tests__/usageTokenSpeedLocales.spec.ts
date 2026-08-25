import { describe, expect, it } from 'vitest'

import en from '../locales/en'
import zh from '../locales/zh'

// CAPYBARA-PATCH: 用量输出吞吐相关文案的双语兜底测试
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
    it(`defines every output speed key in ${name}`, () => {
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
})
