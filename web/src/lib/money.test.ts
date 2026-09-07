import { describe, it, expect } from 'vitest'
import { fenToYuan, groupDigits, signedFenToYuan } from './money'

describe('fenToYuan', () => {
  it('renders an integer 分 amount as 元 with exactly two decimals', () => {
    expect(fenToYuan(3335)).toBe('33.35')
    // The fractional part is padded, not printed as the number it is: 0.7 元 is not 7 分.
    expect(fenToYuan(7)).toBe('0.07')
    expect(fenToYuan(0)).toBe('0.00')
    expect(fenToYuan(-50)).toBe('-0.50')
  })

  it('groups the 元 digits, which is what a naive (fen / 100).toFixed(2) drops', () => {
    // 1234567.89 元. The float route gets the DIGITS right here — (123456789 / 100).toFixed(2) is
    // '1234567.89' — so this case is not about precision, and no test in this file is. It is about
    // the separator: the second assertion is the one that fails for anyone who replaces the body
    // with the one-liner, and the reason this module exists at all is that the strip and the chart
    // must not disagree about how a seven-figure 成交额 reads.
    expect(fenToYuan(123456789)).toBe('1,234,567.89')
    expect(fenToYuan(123456789)).not.toBe((123456789 / 100).toFixed(2))
  })

  it('puts exactly one minus sign, in front, on a large negative amount', () => {
    expect(fenToYuan(-123456789)).toBe('-1,234,567.89')
  })
})

describe('signedFenToYuan', () => {
  it('adds a + only to a rise, because the sign is derived here and not quoted from a vendor', () => {
    expect(signedFenToYuan(4)).toBe('+0.04')
    expect(signedFenToYuan(-371)).toBe('-3.71')
    // Unchanged is neither a rise nor a fall, and '+0.00' would claim it rose.
    expect(signedFenToYuan(0)).toBe('0.00')
  })
})

describe('groupDigits', () => {
  it('groups a nine-digit share count in threes with commas', () => {
    expect(groupDigits(176934100)).toBe('176,934,100')
    expect(groupDigits(999)).toBe('999')
    expect(groupDigits(1000)).toBe('1,000')
    expect(groupDigits(0)).toBe('0')
  })

  // groupDigits used to take Math.abs, so a negative — which only a parser landing on the wrong
  // column can produce for 成交量 or 成交额 — rendered as an ordinary positive number. A wrong number
  // that looks wrong is recoverable; a wrong number that looks right is not.
  it('keeps a minus sign instead of rendering a bad parse as a plausible positive', () => {
    expect(groupDigits(-1234567)).toBe('-1,234,567')
    expect(groupDigits(1234567)).toBe('1,234,567')
    expect(groupDigits(-1)).toBe('-1')
    expect(groupDigits(0)).toBe('0')
    // fenToYuan supplies its own sign off the absolute value, so it must not gain a second one.
    expect(fenToYuan(-371)).toBe('-3.71')
    expect(fenToYuan(-123456789)).toBe('-1,234,567.89')
  })
})
