// Money formatting for the quote surfaces (ADR 0028).
//
// Prices cross the wire as integer 分 because the Go schema has zero REAL columns and a quote is
// the seed of a record we may later persist. This module is the single boundary where one of those
// amounts stops being a number and becomes a string for a person, and it is a module rather than a
// private helper because the price strip and the candlestick chart sit on the SAME screen: the
// moment each formats its own 分, the same 成交额 reads one way in the strip and another way six
// lines below it.

/**
 * Digits grouped in threes with a comma. The fraction is dropped; the sign is NOT.
 *
 * Intl.NumberFormat would take its separator from the UI locale and its grouping from whichever ICU
 * build the runtime was compiled with — the same 成交额 would read 1,234,567 for one reader,
 * 1 234 567 for another and 1234567 on a minimal-ICU build. A share count is grouped in threes with
 * a comma on every A-share terminal regardless of the language of the page around it.
 *
 * Keeping the sign is the point of the Math.abs this no longer does. The only callers that pass a
 * raw number are 成交量 and 成交额, neither of which can be negative in any real body — which is
 * exactly why swallowing a minus here was dangerous: a parser that had landed on the wrong column
 * would have rendered as a plausible, unremarkable positive. fenToYuan below is unaffected: it
 * takes the absolute value itself and prepends its own sign.
 */
export function groupDigits(n: number): string {
  const t = Math.trunc(n)
  return `${t < 0 ? '-' : ''}${String(Math.abs(t)).replace(/\B(?=(\d{3})+(?!\d))/g, ',')}`
}

/**
 * One integer 分 amount as a 元 string with exactly two decimals and grouped 元 digits.
 *
 * The split is integer division rejoined as text, but NOT because the arithmetic would otherwise be
 * wrong: (123456789 / 100).toFixed(2) is digit-for-digit '1234567.89', and a double still resolves
 * 0.01 steps at magnitudes tens of thousands of times larger than anything an A-share prints. What
 * `(fen / 100).toFixed(2)` actually gets wrong is that it is only half the job — it has no
 * separator — so writing it inline anywhere gives the page a second money format that disagrees
 * with this one on every seven-figure amount. That is the trap: one implementation of money
 * formatting on the reading page, not two that happen to agree on small numbers.
 */
export function fenToYuan(fen: number): string {
  const n = Math.trunc(fen)
  const abs = Math.abs(n)
  const cents = abs % 100
  return `${n < 0 ? '-' : ''}${groupDigits(Math.floor(abs / 100))}.${String(cents).padStart(2, '0')}`
}

/**
 * The same string with an explicit leading + when the amount is positive.
 *
 * The + is ours to add because the number is ours: it is derived from the integer snapshot.change,
 * not from the vendor's changePct string, which is rendered verbatim wherever it appears and never
 * gets a sign we invented.
 */
export function signedFenToYuan(fen: number): string {
  return `${Math.trunc(fen) > 0 ? '+' : ''}${fenToYuan(fen)}`
}
