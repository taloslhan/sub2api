import { describe, expect, it } from 'vitest'
import { formatOutputTokensPerSecond } from '../format'

describe('formatOutputTokensPerSecond', () => {
  it('formats finite values with two decimals and the tok/s unit', () => {
    expect(formatOutputTokensPerSecond(50)).toBe('50.00 tok/s')
    expect(formatOutputTokensPerSecond(12.345)).toBe('12.35 tok/s')
    expect(formatOutputTokensPerSecond(0.004)).toBe('0.00 tok/s')
  })

  it('treats zero as a real sample, not an empty value', () => {
    expect(formatOutputTokensPerSecond(0)).toBe('0.00 tok/s')
  })

  it('returns the empty placeholder for nullish input', () => {
    expect(formatOutputTokensPerSecond(null)).toBe('-')
    expect(formatOutputTokensPerSecond(undefined)).toBe('-')
  })

  it('returns the empty placeholder for non-finite and negative values', () => {
    expect(formatOutputTokensPerSecond(Number.NaN)).toBe('-')
    expect(formatOutputTokensPerSecond(Number.POSITIVE_INFINITY)).toBe('-')
    expect(formatOutputTokensPerSecond(Number.NEGATIVE_INFINITY)).toBe('-')
    expect(formatOutputTokensPerSecond(-1)).toBe('-')
  })

  it('supports omitting the unit', () => {
    expect(formatOutputTokensPerSecond(50, { withUnit: false })).toBe('50.00')
    expect(formatOutputTokensPerSecond(null, { withUnit: false })).toBe('-')
  })

  it('supports a custom empty placeholder', () => {
    expect(formatOutputTokensPerSecond(null, { emptyText: 'N/A' })).toBe('N/A')
    expect(formatOutputTokensPerSecond(Number.NaN, { emptyText: '' })).toBe('')
    expect(formatOutputTokensPerSecond(50, { emptyText: 'N/A' })).toBe('50.00 tok/s')
  })
})
