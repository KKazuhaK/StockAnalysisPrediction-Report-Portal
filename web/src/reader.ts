import { useSyncExternalStore } from 'react'

// Reading preferences for the report view: body font size / weight and a wide
// layout toggle. Persisted in localStorage and shared across the reading pages
// (StockPage / RunPage) and AppLayout via a tiny external store, so the "Aa"
// popover, the rendered markdown, and the outer content width all stay in sync.

export const FONT_MIN = 13
export const FONT_MAX = 26
export const FONT_DEFAULT = 15
export const WEIGHTS = [400, 500, 600] as const

export interface ReaderPrefs {
  fontSize: number // px, applied as the .md-body base (headings/tables scale off it via em)
  fontWeight: number // 400 | 500 | 600
  wide: boolean // widen the reading column + lift the outer max-width on reading routes
}

const FS_KEY = 'rp_read_fs'
const FW_KEY = 'rp_read_fw'
const WIDE_KEY = 'rp_read_wide'

// Access via window.localStorage (jsdom provides it; a bare `localStorage` global
// is shadowed/undefined under some test runtimes) and tolerate it being blocked
// (private mode / opaque origin) by degrading to in-memory-only prefs.
function ls(): Storage | null {
  try {
    return typeof window !== 'undefined' ? window.localStorage : null
  } catch {
    return null
  }
}

export function clampFontSize(n: number): number {
  if (!Number.isFinite(n)) return FONT_DEFAULT
  return Math.min(FONT_MAX, Math.max(FONT_MIN, Math.round(n)))
}

export function normalizeWeight(n: number): number {
  return (WEIGHTS as readonly number[]).includes(n) ? n : 400
}

// Read a stored number, treating missing/blank/non-numeric as "unset" (null) so
// an absent key falls back to the default instead of being coerced to 0.
function readNum(key: string): number | null {
  const raw = ls()?.getItem(key)
  if (raw == null || raw === '') return null
  const n = Number(raw)
  return Number.isFinite(n) ? n : null
}

function loadInitial(): ReaderPrefs {
  const fs = readNum(FS_KEY)
  const fw = readNum(FW_KEY)
  return {
    fontSize: fs == null ? FONT_DEFAULT : clampFontSize(fs),
    fontWeight: fw == null ? 400 : normalizeWeight(fw),
    wide: ls()?.getItem(WIDE_KEY) === '1',
  }
}

let state: ReaderPrefs = loadInitial()
const subs = new Set<() => void>()

export function getReaderPrefs(): ReaderPrefs {
  return state
}

export function setReaderPrefs(p: Partial<ReaderPrefs>): void {
  const next: ReaderPrefs = { ...state }
  const writes: Array<[string, string]> = []
  if (p.fontSize !== undefined) {
    next.fontSize = clampFontSize(p.fontSize)
    writes.push([FS_KEY, String(next.fontSize)])
  }
  if (p.fontWeight !== undefined) {
    next.fontWeight = normalizeWeight(p.fontWeight)
    writes.push([FW_KEY, String(next.fontWeight)])
  }
  if (p.wide !== undefined) {
    next.wide = p.wide
    writes.push([WIDE_KEY, p.wide ? '1' : '0'])
  }
  // Commit + notify first, so a blocked or throwing Storage (Safari private mode,
  // quota exhausted) degrades to in-memory-only prefs rather than aborting the
  // update and leaking the error into the antd onChange handler.
  state = next
  subs.forEach((f) => f())
  const store = ls()
  if (store) {
    try {
      for (const [k, v] of writes) store.setItem(k, v)
    } catch {
      /* storage blocked/full: keep the applied in-memory value */
    }
  }
}

function subscribe(cb: () => void): () => void {
  subs.add(cb)
  return () => {
    subs.delete(cb)
  }
}

// Shared reactive hook — components re-render when any reading pref changes.
export function useReaderPrefs(): ReaderPrefs {
  return useSyncExternalStore(subscribe, getReaderPrefs, getReaderPrefs)
}
